package codegen

import (
	"bytes"
	"fmt"
	"text/template"
)

// GenerateMock 生成内存 Mock 实现，用于单元测试替换真实 Store。
func GenerateMock(table *TableIR) string {
	data := mockData{
		PackageName: table.GoPackage,
		EntityName:  table.MessageName,
		StoreImpl:   lowerFirst(table.MessageName) + "Store",
		PKGoName:    toGoName(table.PrimaryKey.Name),
		PKGoType:    table.PrimaryKey.GoType,
		TableID:     table.TableID,
	}

	var buf bytes.Buffer
	t := template.Must(template.New("mock").Parse(mockTemplate))
	if err := t.Execute(&buf, data); err != nil {
		panic(fmt.Sprintf("mock template execute: %v", err))
	}
	return buf.String()
}

type mockData struct {
	PackageName string
	EntityName  string
	StoreImpl   string // userStore (used as prefix)
	PKGoName    string
	PKGoType    string
	TableID     uint64
}

var mockTemplate = `package {{.PackageName}}

import (
	"fmt"
	"sync"
)

// mock{{.EntityName}}Store 实现 {{.EntityName}}Store 接口的内存版本，用于单元测试。
type mock{{.EntityName}}Store struct {
	mu   sync.RWMutex
	data map[{{.PKGoType}}]*{{.EntityName}}
}

// NewMock{{.EntityName}}Store 创建 Mock Store 实例。
func NewMock{{.EntityName}}Store() *mock{{.EntityName}}Store {
	return &mock{{.EntityName}}Store{
		data: make(map[{{.PKGoType}}]*{{.EntityName}}),
	}
}

// Create 创建记录。
func (m *mock{{.EntityName}}Store) Create(record *{{.EntityName}}) error {
	if record == nil {
		return fmt.Errorf("sqlitex: cannot create nil {{.EntityName}}")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.data[record.{{.PKGoName}}]; exists {
		return fmt.Errorf("sqlitex: {{.EntityName}} with {{.PKGoName}}=%v already exists", record.{{.PKGoName}})
	}
	// 深拷贝避免外部修改
	clone := *record
	m.data[record.{{.PKGoName}}] = &clone
	return nil
}

// Update 更新记录。
func (m *mock{{.EntityName}}Store) Update(record *{{.EntityName}}) error {
	if record == nil {
		return fmt.Errorf("sqlitex: cannot update nil {{.EntityName}}")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.data[record.{{.PKGoName}}]; !exists {
		return fmt.Errorf("sqlitex: {{.EntityName}} with {{.PKGoName}}=%v not found", record.{{.PKGoName}})
	}
	clone := *record
	m.data[record.{{.PKGoName}}] = &clone
	return nil
}

// Delete 删除记录。
func (m *mock{{.EntityName}}Store) Delete({{.PKGoName}} {{.PKGoType}}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, {{.PKGoName}})
	return nil
}

// Get 按主键查询。
func (m *mock{{.EntityName}}Store) Get({{.PKGoName}} {{.PKGoType}}) (*{{.EntityName}}, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	record, exists := m.data[{{.PKGoName}}]
	if !exists {
		return nil, nil
	}
	clone := *record
	return &clone, nil
}
`
