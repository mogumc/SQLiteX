package codegen

import (
	"bytes"
	"fmt"
	"text/template"
)

type queryData struct {
	PackageName  string
	StoreName    string
	EntityName   string
	QueryName    string
	TableID      uint64
	QueryFields  []*queryField
	IndexedField *queryField
	PKGoType     string
}

type queryField struct {
	GoName   string
	GoType   string
	FieldNum int32
	IsIndex  bool
}

func GenerateQuery(table *TableIR) (string, error) {
	tmpl, err := template.New("query").Funcs(template.FuncMap{
		"toGoName": toGoName,
	}).Parse(queryTemplate)
	if err != nil {
		return "", fmt.Errorf("parse query template: %w", err)
	}

	indexedFieldNums := make(map[string]int32)
	for _, f := range table.IndexedFields {
		indexedFieldNums[f.Name] = f.Number
	}

	data := &queryData{
		PackageName: table.GoPackage,
		StoreName:   table.StoreName,
		EntityName:  table.MessageName,
		QueryName:   table.MessageName + "Query",
		TableID:     table.TableID,
		PKGoType:    table.PrimaryKey.GoType,
	}

	for _, f := range table.Fields {
		if f.IsPrimaryKey || f.IsRepeated { continue }
		qf := &queryField{
			GoName: toGoName(f.Name), GoType: f.GoType,
			FieldNum: f.Number, IsIndex: indexedFieldNums[f.Name] > 0,
		}
		data.QueryFields = append(data.QueryFields, qf)
		if qf.IsIndex && data.IndexedField == nil { data.IndexedField = qf }
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute query template: %w", err)
	}
	return buf.String(), nil
}

var queryTemplate = `package {{.PackageName}}

import (
{{- $hasBytes := false}}
{{- range .QueryFields}}{{if eq .GoType "[]byte"}}{{$hasBytes = true}}{{end}}{{end}}
{{- if $hasBytes}}
	"bytes"
{{- end}}
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/mogumc/sqlitex"
	"github.com/mogumc/sqlitex/internal/encoding"
)

type {{.QueryName}} struct {
	db      *sqlitex.DB
	where   []string
	args    []interface{}
	limit   int
	lastKey []byte
}

func New{{.QueryName}}(db *sqlitex.DB) *{{.QueryName}} {
	return &{{.QueryName}}{db: db, where: make([]string, 0), args: make([]interface{}, 0)}
}

{{range .QueryFields}}
func (q *{{$.QueryName}}) Where{{.GoName}}(op string, value {{.GoType}}) *{{$.QueryName}} {
	q.where = append(q.where, "{{.GoName}} " + op + " ?")
	q.args = append(q.args, value)
	return q
}
{{end}}

func (q *{{.QueryName}}) Limit(n int) *{{.QueryName}} { q.limit = n; return q }
func (q *{{.QueryName}}) AfterKey(lastKey []byte) *{{.QueryName}} { q.lastKey = lastKey; return q }

{{- if .IndexedField}}
func (q *{{.QueryName}}) Exec() ([]*{{.EntityName}}, error) {
	if len(q.where) > 0 {
		parts := splitWhere(q.where[0])
		if len(parts) == 2 && parts[0] == "{{.IndexedField.GoName}}" && parts[1] == "=" {
			return q.execIndexed()
		}
	}
	return q.execFullScan()
}

func (q *{{.QueryName}}) execIndexed() ([]*{{.EntityName}}, error) {
	val := q.args[0]
	var fieldValue []byte
{{- if eq .IndexedField.GoType "string"}}
	fieldValue = []byte(val.(string))
{{- else if eq .IndexedField.GoType "int64"}}
	fieldValue = make([]byte, 8); binary.LittleEndian.PutUint64(fieldValue, uint64(val.(int64)))
{{- else if eq .IndexedField.GoType "int32"}}
	fieldValue = make([]byte, 4); binary.LittleEndian.PutUint32(fieldValue, uint32(val.(int32)))
{{- else}}
	fieldValue = []byte(fmt.Sprintf("%v", val))
{{- end}}
	prefix := encoding.EncodeIndexPrefix({{.TableID}}, {{.IndexedField.FieldNum}}, fieldValue)
	iter := q.db.Iterate(prefix)
	if iter == nil { return nil, nil }
	defer iter.Close()
	if len(q.lastKey) > 0 { iter.SeekLT(q.lastKey); if iter.Valid() { iter.Next() } }

	var results []*{{.EntityName}}
	for iter.Next() {
		pkBytes := iter.Value()
		dataKey := encoding.EncodeKey({{.TableID}}, pkBytes)
		value, err := q.db.Get(dataKey)
		if err != nil || value == nil { continue }
		m, err := Deserialize{{.EntityName}}(value)
		if err != nil { continue }
		if len(q.where) > 1 && !q.matchWhereTail(m) { continue }
		results = append(results, m)
		if q.limit > 0 && len(results) >= q.limit { break }
	}
	return results, nil
}
{{- else}}
func (q *{{.QueryName}}) Exec() ([]*{{.EntityName}}, error) { return q.execFullScan() }
{{- end}}

func (q *{{.QueryName}}) execFullScan() ([]*{{.EntityName}}, error) {
	prefix := encoding.EncodeKey({{.TableID}}, nil)
	iter := q.db.Iterate(prefix)
	if iter == nil { return nil, nil }
	defer iter.Close()
	if len(q.lastKey) > 0 { iter.SeekLT(q.lastKey); if iter.Valid() { iter.Next() } }

	var results []*{{.EntityName}}
	for iter.Next() {
		value := iter.Value()
		m, err := Deserialize{{.EntityName}}(value)
		if err != nil { continue }
		if q.matchWhere(m) {
			results = append(results, m)
			if q.limit > 0 && len(results) >= q.limit { break }
		}
	}
	return results, nil
}

func (q *{{.QueryName}}) First() (*{{.EntityName}}, error) {
	q.limit = 1; r, e := q.Exec(); if e != nil { return nil, e }
	if len(r) == 0 { return nil, nil }; return r[0], nil
}
func (q *{{.QueryName}}) Count() (int, error) {
	r, e := q.Exec(); if e != nil { return 0, e }; return len(r), nil
}

func (q *{{.QueryName}}) matchWhere(item *{{.EntityName}}) bool {
	for _, cond := range q.where {
		parts := splitWhere(cond)
		if len(parts) != 2 { continue }
		field, op := parts[0], parts[1]
		switch field {
		{{range .QueryFields}}case "{{.GoName}}": if !q.compare{{.GoName}}(item.{{.GoName}}, op) { return false }
		{{end}}
		}
	}
	return true
}

func (q *{{.QueryName}}) matchWhereTail(item *{{.EntityName}}) bool {
	for i, cond := range q.where {
		if i == 0 { continue }
		parts := splitWhere(cond)
		if len(parts) != 2 { continue }
		field, op := parts[0], parts[1]
		switch field {
		{{range .QueryFields}}case "{{.GoName}}": if !q.compare{{.GoName}}(item.{{.GoName}}, op) { return false }
		{{end}}
		}
	}
	return true
}

{{range .QueryFields}}
func (q *{{$.QueryName}}) compare{{.GoName}}(actual {{.GoType}}, op string) bool {
	targetIdx := -1
	{{$fname := .GoName}}
	for i, w := range q.where { parts := splitWhere(w); if len(parts) == 2 && parts[0] == "{{$fname}}" && parts[1] == op { targetIdx = i; break } }
	if targetIdx < 0 || targetIdx >= len(q.args) { return false }
	expected := q.args[targetIdx].({{.GoType}})
	switch op {
{{- if eq .GoType "[]byte"}}
	case "=": return bytes.Equal(actual, expected)
	case "!=": return !bytes.Equal(actual, expected)
{{- else}}
	case "=": return actual == expected
	case "!=": return actual != expected
{{- end}}
{{- if eq .GoType "string"}}
	case "LIKE": return strings.Contains(actual, expected)
{{- end}}
{{- if or (eq .GoType "int32") (eq .GoType "int64") (eq .GoType "uint32") (eq .GoType "uint64") (eq .GoType "float32") (eq .GoType "float64")}}
	case ">":  return actual > expected
	case "<":  return actual < expected
	case ">=": return actual >= expected
	case "<=": return actual <= expected
{{- end}}
	}
	return false
}
{{end}}

func splitWhere(cond string) []string {
	lastSpace := -1
	for i := len(cond) - 1; i >= 0; i-- { if cond[i] == ' ' { lastSpace = i; break } }
	if lastSpace < 0 { return nil }
	inner := cond[:lastSpace]
	for i := len(inner) - 1; i >= 0; i-- { if inner[i] == ' ' { return []string{inner[:i], inner[i+1:]} } }
	return nil
}
`
