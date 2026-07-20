package codegen

import (
	"bytes"
	"fmt"
	"text/template"
)

// GenerateStore 生成强类型的 Store 接口和实现。
func GenerateStore(table *TableIR) string {
	data := buildStoreData(table)

	var buf bytes.Buffer
	t := template.Must(template.New("store").Funcs(template.FuncMap{
		"toGoName": toGoName,
	}).Parse(storeTmpl))
	if err := t.Execute(&buf, data); err != nil {
		panic(fmt.Sprintf("store template execute: %v", err))
	}
	return buf.String()
}

// storeData 是 Store 模板的数据输入。
type storeData struct {
	MessageName   string
	PackageName   string
	StoreName     string // UserStore
	StoreImpl     string // userStore
	PKField       *FieldIR
	PKGoName      string // Id
	PKGoType      string // int64
	TableID       uint64
	IndexedFields []indexField // 二级索引字段列表
}

// indexField 描述一个二级索引字段，供模板生成索引维护代码。
type indexField struct {
	GoName     string // 字段 Go 名
	ProtoName  string // 字段原始名
	GoType     string // Go 类型
	FieldNum   int32  // proto 字段编号
	IsUnique   bool   // 是否唯一索引
}

func buildStoreData(table *TableIR) storeData {
	pk := table.PrimaryKey
	var idxFields []indexField
	for _, f := range table.IndexedFields {
		idxFields = append(idxFields, indexField{
			GoName:    toGoName(f.Name),
			ProtoName: f.Name,
			GoType:    f.GoType,
			FieldNum:  f.Number,
			IsUnique:  f.Index == 2, // INDEX_UNIQUE
		})
	}
	return storeData{
		MessageName:   table.MessageName,
		PackageName:   table.GoPackage,
		StoreName:     table.MessageName + "Store",
		StoreImpl:     lowerFirst(table.MessageName) + "Store",
		PKField:       pk,
		PKGoName:      toGoName(pk.Name),
		PKGoType:      pk.GoType,
		TableID:       table.TableID,
		IndexedFields: idxFields,
	}
}

// lowerFirst 将首字母转为小写。
func lowerFirst(s string) string {
	if len(s) == 0 {
		return s
	}
	first := s[0]
	if first >= 'A' && first <= 'Z' {
		return string(first+32) + s[1:]
	}
	return s
}

var storeTmpl = `package {{.PackageName}}

import (
	"encoding/binary"
	"fmt"

	"github.com/mogumc/sqlitex"
	"github.com/mogumc/sqlitex/internal/encoding"
)

// {{.StoreName}} 是 {{.MessageName}} 的强类型存储接口。
type {{.StoreName}} interface {
	Create(m *{{.MessageName}}) error
	Update(m *{{.MessageName}}) error
	Delete({{.PKGoName}} {{.PKGoType}}) error
	Get({{.PKGoName}} {{.PKGoType}}) (*{{.MessageName}}, error)
}

// {{.StoreImpl}} 实现 {{.StoreName}} 接口。
type {{.StoreImpl}} struct {
	db      *sqlitex.DB
	tableID uint64
}

// New{{.StoreName}} 创建 {{.StoreName}} 实例。
func New{{.StoreName}}(db *sqlitex.DB) {{.StoreName}} {
	return &{{.StoreImpl}}{db: db, tableID: {{.TableID}}}
}

// Create 创建新的 {{.MessageName}} 记录。
// 通过 WriteBatch 原子写入主数据行 + 所有二级索引。
func (s *{{.StoreImpl}}) Create(m *{{.MessageName}}) error {
	if m == nil {
		return fmt.Errorf("sqlitex: cannot create nil {{.MessageName}}")
	}
	
	pkBytes := encode{{.MessageName}}PrimaryKey(m.{{.PKGoName}})
	dataKey := encoding.EncodeKey(s.tableID, pkBytes)
	value := m.Serialize()
	
	ops := make([]sqlitex.KVPair, 0, 1+{{len .IndexedFields}})
	ops = append(ops, sqlitex.KVPair{Key: dataKey, Value: value})
	{{- range .IndexedFields}}
	ops = append(ops, sqlitex.KVPair{
		Key:   encoding.EncodeIndexKey(s.tableID, {{.FieldNum}}, encode{{$.MessageName}}Index{{.GoName}}Value(m.{{.GoName}}), pkBytes),
		Value: pkBytes,
	})
	{{- end}}
	return s.db.WriteBatch(ops)
}

// Update 更新已存在的 {{.MessageName}} 记录。
// 先 Get 旧值删除旧索引，再原子写入新数据+新索引。
func (s *{{.StoreImpl}}) Update(m *{{.MessageName}}) error {
	if m == nil {
		return fmt.Errorf("sqlitex: cannot update nil {{.MessageName}}")
	}
	
	pkBytes := encode{{.MessageName}}PrimaryKey(m.{{.PKGoName}})
	dataKey := encoding.EncodeKey(s.tableID, pkBytes)
	value := m.Serialize()
	
	ops := make([]sqlitex.KVPair, 0, 1+{{len .IndexedFields}}*2)
	ops = append(ops, sqlitex.KVPair{Key: dataKey, Value: value})
	{{- if .IndexedFields}}
	
	// 获取旧值，删除旧索引条目
	old, _ := s.Get(m.{{.PKGoName}})
	if old != nil {
		{{- range .IndexedFields}}
		ops = append(ops, sqlitex.KVPair{
			Key:    encoding.EncodeIndexKey(s.tableID, {{.FieldNum}}, encode{{$.MessageName}}Index{{.GoName}}Value(old.{{.GoName}}), pkBytes),
			Delete: true,
		})
		{{- end}}
	}
	{{- end}}{{- range .IndexedFields}}
	ops = append(ops, sqlitex.KVPair{
		Key:   encoding.EncodeIndexKey(s.tableID, {{.FieldNum}}, encode{{$.MessageName}}Index{{.GoName}}Value(m.{{.GoName}}), pkBytes),
		Value: pkBytes,
	})
	{{- end}}
	return s.db.WriteBatch(ops)
}

// Delete 删除指定主键的 {{.MessageName}} 记录及其所有索引。
// 先 Get 获取旧值以构造正确的索引 Key，再原子删除。
func (s *{{.StoreImpl}}) Delete({{.PKGoName}} {{.PKGoType}}) error {
	pkBytes := encode{{.MessageName}}PrimaryKey({{.PKGoName}})
	dataKey := encoding.EncodeKey(s.tableID, pkBytes)
	
	ops := make([]sqlitex.KVPair, 0, 1+{{len .IndexedFields}})
	ops = append(ops, sqlitex.KVPair{Key: dataKey, Delete: true})
	{{- if .IndexedFields}}
	
	// 获取旧值以确定要删除的索引 Key
	old, _ := s.Get({{.PKGoName}})
	if old != nil {
		{{- range .IndexedFields}}
		ops = append(ops, sqlitex.KVPair{
			Key:    encoding.EncodeIndexKey(s.tableID, {{.FieldNum}}, encode{{$.MessageName}}Index{{.GoName}}Value(old.{{.GoName}}), pkBytes),
			Delete: true,
		})
		{{- end}}
	}
	{{- end}}
	return s.db.WriteBatch(ops)
}

// Get 根据主键查询 {{.MessageName}} 记录。
func (s *{{.StoreImpl}}) Get({{.PKGoName}} {{.PKGoType}}) (*{{.MessageName}}, error) {
	pkBytes := encode{{.MessageName}}PrimaryKey({{.PKGoName}})
	key := encoding.EncodeKey(s.tableID, pkBytes)
	
	value, err := s.db.Get(key)
	if err != nil {
		return nil, fmt.Errorf("sqlitex: get {{.MessageName}}: %w", err)
	}
	if value == nil {
		return nil, nil
	}
	return Deserialize{{.MessageName}}(value)
}

// encode{{.MessageName}}PrimaryKey 编码主键为字节切片。
func encode{{.MessageName}}PrimaryKey({{.PKGoName}} {{.PKGoType}}) []byte {
	{{- if eq .PKGoType "string"}}
	return []byte({{.PKGoName}})
	{{- else if eq .PKGoType "[]byte"}}
	return {{.PKGoName}}
	{{- else if eq .PKGoType "int64"}}
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64({{.PKGoName}}))
	return buf
	{{- else if eq .PKGoType "uint64"}}
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, {{.PKGoName}})
	return buf
	{{- else if eq .PKGoType "int32"}}
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, uint32({{.PKGoName}}))
	return buf
	{{- else if eq .PKGoType "uint32"}}
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, {{.PKGoName}})
	return buf
	{{- else}}
	return []byte(fmt.Sprintf("%v", {{.PKGoName}}))
	{{- end}}
}
{{- range .IndexedFields}}
{{- if eq .GoType "string"}}
func encode{{$.MessageName}}Index{{.GoName}}Value(v string) []byte { return []byte(v) }
{{- else if eq .GoType "[]byte"}}
func encode{{$.MessageName}}Index{{.GoName}}Value(v []byte) []byte { return v }
{{- else if eq .GoType "int64"}}
func encode{{$.MessageName}}Index{{.GoName}}Value(v int64) []byte { buf := make([]byte, 8); binary.LittleEndian.PutUint64(buf, uint64(v)); return buf }
{{- else if eq .GoType "int32"}}
func encode{{$.MessageName}}Index{{.GoName}}Value(v int32) []byte { buf := make([]byte, 4); binary.LittleEndian.PutUint32(buf, uint32(v)); return buf }
{{- else}}
func encode{{$.MessageName}}Index{{.GoName}}Value(v {{.GoType}}) []byte { return []byte(fmt.Sprintf("%v", v)) }
{{- end}}
{{- end}}
`
