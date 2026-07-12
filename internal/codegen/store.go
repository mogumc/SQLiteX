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
	MessageName  string
	PackageName  string
	StoreName    string // UserStore
	StoreImpl    string // userStore
	PKField      *FieldIR
	PKGoName     string // Id
	PKGoType     string // int64
	TableID      uint64
}

func buildStoreData(table *TableIR) storeData {
	pk := table.PrimaryKey
	return storeData{
		MessageName: table.MessageName,
		PackageName: table.GoPackage,
		StoreName:   table.MessageName + "Store",
		StoreImpl:   lowerFirst(table.MessageName) + "Store",
		PKField:     pk,
		PKGoName:    toGoName(pk.Name),
		PKGoType:    pk.GoType,
		TableID:     table.TableID,
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
	"fmt"

	"github.com/mogumc/sqlitex"
	"github.com/mogumc/sqlitex/internal/encoding"
)

// {{.StoreName}} 是 {{.MessageName}} 的强类型存储接口。
type {{.StoreName}} interface {
	// Create 创建新的 {{.MessageName}} 记录。
	Create(m *{{.MessageName}}) error

	// Update 更新已存在的 {{.MessageName}} 记录。
	Update(m *{{.MessageName}}) error

	// Delete 删除指定主键的 {{.MessageName}} 记录。
	Delete({{.PKGoName}} {{.PKGoType}}) error

	// Get 根据主键查询 {{.MessageName}} 记录。
	// 记录不存在时返回 (nil, nil)。
	Get({{.PKGoName}} {{.PKGoType}}) (*{{.MessageName}}, error)
}

// {{.StoreImpl}} 实现 {{.StoreName}} 接口。
type {{.StoreImpl}} struct {
	db      *sqlitex.DB
	tableID uint64
}

// New{{.StoreName}} 创建 {{.StoreName}} 实例。
func New{{.StoreName}}(db *sqlitex.DB) {{.StoreName}} {
	return &{{.StoreImpl}}{
		db:      db,
		tableID: {{.TableID}},
	}
}

// Create 创建新的 {{.MessageName}} 记录。
func (s *{{.StoreImpl}}) Create(m *{{.MessageName}}) error {
	if m == nil {
		return fmt.Errorf("sqlitex: cannot create nil {{.MessageName}}")
	}
	
	pkBytes := encode{{.MessageName}}PrimaryKey(m.{{.PKGoName}})
	key := encoding.EncodeKey(s.tableID, pkBytes)
	value := m.Serialize()
	
	return s.db.Put(key, value)
}

// Update 更新已存在的 {{.MessageName}} 记录。
func (s *{{.StoreImpl}}) Update(m *{{.MessageName}}) error {
	if m == nil {
		return fmt.Errorf("sqlitex: cannot update nil {{.MessageName}}")
	}
	
	pkBytes := encode{{.MessageName}}PrimaryKey(m.{{.PKGoName}})
	key := encoding.EncodeKey(s.tableID, pkBytes)
	value := m.Serialize()
	
	return s.db.Put(key, value)
}

// Delete 删除指定主键的 {{.MessageName}} 记录。
func (s *{{.StoreImpl}}) Delete({{.PKGoName}} {{.PKGoType}}) error {
	pkBytes := encode{{.MessageName}}PrimaryKey({{.PKGoName}})
	key := encoding.EncodeKey(s.tableID, pkBytes)
	
	return s.db.Delete(key)
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
		return nil, nil // 记录不存在
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
	// 默认回退：转为字符串后编码
	return []byte(fmt.Sprintf("%v", {{.PKGoName}}))
	{{- end}}
}
`
