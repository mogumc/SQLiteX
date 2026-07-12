package codegen

import (
	"bytes"
	"fmt"
	"text/template"
)

// GenerateQuery 生成 Fluent Query Builder 代码。
func GenerateQuery(table *TableIR) (string, error) {
	tmpl, err := template.New("query").Parse(queryTemplate)
	if err != nil {
		return "", fmt.Errorf("parse query template: %w", err)
	}

	data := &queryData{
		PackageName: table.GoPackage,
		StoreName:   table.StoreName,
		EntityName:  table.MessageName,
		QueryName:   table.MessageName + "Query",
		TableID:     table.TableID,
	}

	// 收集可查询字段（排除主键和 repeated 字段）
	for _, f := range table.Fields {
		if f.IsPrimaryKey || f.IsRepeated {
			continue
		}
		data.QueryFields = append(data.QueryFields, &queryField{
			GoName: toGoName(f.Name),
			GoType: f.GoType,
		})
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute query template: %w", err)
	}

	return buf.String(), nil
}

type queryData struct {
	PackageName string
	StoreName   string // UserStore
	EntityName  string // User
	QueryName   string // UserQuery
	TableID     uint64
	QueryFields []*queryField
}

type queryField struct {
	GoName string
	GoType string
}

var queryTemplate = `package {{.PackageName}}

import (
	"github.com/mogumc/sqlitex"
	"github.com/mogumc/sqlitex/internal/encoding"
)

// {{.QueryName}} 提供链式查询构建器。
type {{.QueryName}} struct {
	db     *sqlitex.DB
	where  []string
	args   []interface{}
	limit  int
	offset int
}

// New{{.QueryName}} 创建查询构建器实例。
func New{{.QueryName}}(db *sqlitex.DB) *{{.QueryName}} {
	return &{{.QueryName}}{
		db:    db,
		where: make([]string, 0),
		args:  make([]interface{}, 0),
	}
}

{{range .QueryFields}}
// Where{{.GoName}} 添加 {{.GoName}} 字段条件。
func (q *{{$.QueryName}}) Where{{.GoName}}(op string, value {{.GoType}}) *{{$.QueryName}} {
	q.where = append(q.where, "{{.GoName}} " + op + " ?")
	q.args = append(q.args, value)
	return q
}
{{end}}

// Limit 设置返回结果数量限制。
func (q *{{.QueryName}}) Limit(n int) *{{.QueryName}} {
	q.limit = n
	return q
}

// Offset 设置结果偏移量。
func (q *{{.QueryName}}) Offset(n int) *{{.QueryName}} {
	q.offset = n
	return q
}

// Exec 扫描全表前缀，反序列化后应用过滤条件，返回匹配结果。
func (q *{{.QueryName}}) Exec() ([]*{{.EntityName}}, error) {
	// 构造表前缀
	prefix := encoding.EncodeKey({{.TableID}}, nil)

	// 扫描所有记录
	var results []*{{.EntityName}}
	iter := q.db.Iterate(prefix)
	if iter == nil {
		return nil, nil
	}
	defer iter.Close()

	for iter.Next() {
		_ = iter.Key()
		value := iter.Value()
		m, err := Deserialize{{.EntityName}}(value)
		if err != nil {
			continue // 跳过损坏数据
		}
		if q.matchWhere(m) {
			results = append(results, m)
		}
	}

	// 应用 offset
	if q.offset > 0 {
		if q.offset >= len(results) {
			return []*{{.EntityName}}{}, nil
		}
		results = results[q.offset:]
	}

	// 应用 limit
	if q.limit > 0 && q.limit < len(results) {
		results = results[:q.limit]
	}

	return results, nil
}

// First 执行查询并返回第一条结果。
func (q *{{.QueryName}}) First() (*{{.EntityName}}, error) {
	q.limit = 1
	results, err := q.Exec()
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}
	return results[0], nil
}

// Count 执行查询并返回结果数量。
func (q *{{.QueryName}}) Count() (int, error) {
	results, err := q.Exec()
	if err != nil {
		return 0, err
	}
	return len(results), nil
}

// matchWhere 检查记录是否满足所有 where 条件。
func (q *{{.QueryName}}) matchWhere(item *{{.EntityName}}) bool {
	for _, cond := range q.where {
		// 解析条件字符串 "FieldName op ?"
		parts := splitWhere(cond)
		if len(parts) != 2 {
			continue
		}
		field, op := parts[0], parts[1]

		switch field {
		{{range .QueryFields}}
		case "{{.GoName}}":
			if !q.compare{{.GoName}}(item.{{.GoName}}, op) {
				return false
			}
		{{end}}
		}
	}
	return true
}

{{range .QueryFields}}
// compare{{.GoName}} 比较 {{.GoName}} 字段值。
func (q *{{$.QueryName}}) compare{{.GoName}}(actual {{.GoType}}, op string) bool {
	// 根据 where 顺序找到对应 args
	targetIdx := -1
	{{$fname := .GoName}}
	for i, w := range q.where {
		parts := splitWhere(w)
		if len(parts) == 2 && parts[0] == "{{$fname}}" && parts[1] == op {
			targetIdx = i
			break
		}
	}
	if targetIdx < 0 || targetIdx >= len(q.args) {
		return false
	}
	expected := q.args[targetIdx].({{.GoType}})

	switch op {
	case "=":
		return actual == expected
	case "!=":
		return actual != expected
	{{if eq .GoType "string"}}
	case "LIKE":
		return false // Phase 2: strings.Contains
	{{end}}
	{{if or (eq .GoType "int32") (eq .GoType "int64") (eq .GoType "uint32") (eq .GoType "uint64") (eq .GoType "float32") (eq .GoType "float64")}}
	case ">":
		return actual > expected
	case "<":
		return actual < expected
	case ">=":
		return actual >= expected
	case "<=":
		return actual <= expected
	{{end}}
	}
	return false
}
{{end}}

// splitWhere 简单切分条件字符串 "FieldName op ?" → ["FieldName", "op"]
func splitWhere(cond string) []string {
	// 从末尾跳过 " ?" 部分
	lastSpace := -1
	for i := len(cond) - 1; i >= 0; i-- {
		if cond[i] == ' ' {
			lastSpace = i
			break
		}
	}
	if lastSpace < 0 {
		return nil
	}
	// cond[:lastSpace] = "FieldName op"
	inner := cond[:lastSpace]
	for i := len(inner) - 1; i >= 0; i-- {
		if inner[i] == ' ' {
			return []string{inner[:i], inner[i+1:]}
		}
	}
	return nil
}
`
