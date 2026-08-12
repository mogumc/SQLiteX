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
	HasTTL        bool         // 是否声明记录级 TTL
	TTL           int64        // TTL 纳秒时长
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
		HasTTL:        table.HasTTL,
		TTL:           int64(table.TTL),
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
{{- if .HasTTL}}
	"time"
{{- end}}

	"github.com/mogumc/sqlitex"
	"github.com/mogumc/sqlitex/internal/encoding"
)

// {{.StoreName}} 是 {{.MessageName}} 的强类型存储接口。
type {{.StoreName}} interface {
	Create(m *{{.MessageName}}) error
	Update(m *{{.MessageName}}) error
	Delete({{.PKGoName}} {{.PKGoType}}) error
	Get({{.PKGoName}} {{.PKGoType}}) (*{{.MessageName}}, error)
{{- if .HasTTL}}
	// PurgeExpired 主动清理所有过期记录（含索引），返回删除条数。
	// 供用户后台定时调用，消除惰性删除窗口期的逻辑过期数据。
	PurgeExpired() (int, error)
{{- end}}
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
{{- if .HasTTL}}
// 记录级 TTL: 写入时计算 expiresAt = now + {{.TTL}}ns，过期后惰性删除。
{{- end}}
func (s *{{.StoreImpl}}) Create(m *{{.MessageName}}) error {
	if m == nil {
		return fmt.Errorf("sqlitex: cannot create nil {{.MessageName}}")
	}
	
	pkBytes := encode{{.MessageName}}PrimaryKey(m.{{.PKGoName}})
	dataKey := encoding.EncodeKey(s.tableID, pkBytes)
{{- if .HasTTL}}
	value := m.SerializeWithExpiry(time.Now().UnixNano() + {{.TTL}})
{{- else}}
	value := m.Serialize()
{{- end}}
	
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
{{- if .HasTTL}}
	value := m.SerializeWithExpiry(time.Now().UnixNano() + {{.TTL}})
{{- else}}
	value := m.Serialize()
{{- end}}
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
{{- if .HasTTL}}
// TTL 惰性删除: 记录已过期时物理删除并返回 nil。
{{- end}}
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
{{- if .HasTTL}}
	// 惰性删除: 检查 Meta Header 过期时间戳
	m, expiresAt, err := Deserialize{{.MessageName}}Meta(value)
	if err != nil {
		return nil, err
	}
	if expiresAt > 0 && time.Now().UnixNano() > expiresAt {
		// 过期: 原子删除数据行 + 索引行（产生 tombstone，Compaction 时物理回收）
		delete{{.MessageName}}WithIndexes(s.db, s.tableID, key, m)
		return nil, nil
	}
	return m, nil
{{- else}}
	return Deserialize{{.MessageName}}(value)
{{- end}}
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
{{- if .HasTTL}}

// delete{{.MessageName}}WithIndexes 原子删除记录及其全部索引条目。
// 供 TTL 惰性删除与 PurgeExpired 复用，保证数据行与索引行一致清理。
func delete{{.MessageName}}WithIndexes(db *sqlitex.DB, tableID uint64, dataKey []byte, m *{{.MessageName}}) error {
	_, pkBytes, err := encoding.DecodeKey(dataKey)
	if err != nil {
		return err
	}
{{- if .IndexedFields}}
	ops := make([]sqlitex.KVPair, 0, 1+{{len .IndexedFields}})
	ops = append(ops, sqlitex.KVPair{Key: dataKey, Delete: true})
	{{- range .IndexedFields}}
	ops = append(ops, sqlitex.KVPair{
		Key:    encoding.EncodeIndexKey(tableID, {{.FieldNum}}, encode{{$.MessageName}}Index{{.GoName}}Value(m.{{.GoName}}), pkBytes),
		Delete: true,
	})
	{{- end}}
	return db.WriteBatch(ops)
{{- else}}
	_ = pkBytes // 无索引字段，仅删除数据行
	return db.Delete(dataKey)
{{- end}}
}

// PurgeExpired 主动清理全部过期记录及其索引，返回删除条数。
// 调用时机由用户决定（如后台定时任务），用于消除惰性删除窗口期的逻辑过期数据，
// 同时提前释放墓碑空间，降低 Compaction 压力。
func (s *{{.StoreImpl}}) PurgeExpired() (int, error) {
	prefix := encoding.EncodeKey(s.tableID, nil)
	iter := s.db.Iterate(prefix)
	if iter == nil {
		return 0, nil
	}
	defer iter.Close()

	count := 0
	now := time.Now().UnixNano()
	for iter.Next() {
		key := iter.Key()
		m, expiresAt, err := Deserialize{{.MessageName}}Meta(iter.Value())
		if err != nil {
			continue // 跳过损坏数据
		}
		if expiresAt > 0 && now > expiresAt {
			if err := delete{{.MessageName}}WithIndexes(s.db, s.tableID, key, m); err == nil {
				count++
			}
		}
	}
	return count, nil
}
{{- end}}
`
