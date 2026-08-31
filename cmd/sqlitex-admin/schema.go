// Schema 导入与 Key/Value 结构化解码（Phase 3 admin 增强）。
//
// 用户在面板导入业务 .proto 后，后端用 protoparse 解析源码并复用
// internal/codegen.BuildIR（与 protoc-gen-sqlitex 完全同源的 IR 提取逻辑），
// 得到真实的库表结构（表/字段/主键/索引/TTL/压缩），
// 再据此把存储层的物理 Key/Value 解码回业务语义。
//
// 物理布局（与生成代码严格一致，详见 encoding/serializer 模板）：
//
//	数据 Key: [0x00][TableHash 8B 定宽][PK]
//	索引 Key: [0xFF][TableHash 8B 定宽][FieldNum][FieldValue]
//	索引 Value: PK bytes
//	Value(无TTL): 字段顺序扁平拼接（定长 LE / u32 前缀变长 / 压缩变长）
//	Value(有TTL): [8B expiresAt UnixNano] + 字段扁平拼接
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/desc/protoparse"
	"github.com/klauspost/compress/zstd"
	"github.com/mogumc/sqlitex/internal/codegen"
	sqlitexpb "github.com/mogumc/sqlitex/proto/sqlitex"
)

// dataKeyPrefix 与 encoding.DataPrefix 一致（0x00），此处独立声明避免 internal 依赖扩散。
const dataKeyPrefix = 0x00

// indexKeyPrefix 与 encoding.IndexPrefix 一致（0xFF），此处独立声明避免 internal 依赖扩散。
const indexKeyPrefix = 0xFF

// schemaStore 保存当前会话导入的库表结构（可整体替换/清空）。
type schemaStore struct {
	mu     sync.RWMutex
	source string
	tables map[uint64]*tableSchema // 按 TableID 索引
	list   []*tableSchema          // 保持导入顺序，供 UI 渲染
}

// tableSchema 是一张表（Message）的完整结构描述。
type tableSchema struct {
	TableID    uint64        `json:"table_id"`
	Message    string        `json:"message"`
	PrimaryKey pkSchema      `json:"primary_key"`
	HasTTL     bool          `json:"has_ttl"`
	TTL        string        `json:"ttl,omitempty"`
	Fields     []fieldSchema `json:"fields"`
	byFieldNum map[int32]int // proto 字段编号 → Fields 下标
	pkProto    descriptorpb.FieldDescriptorProto_Type
}

// pkSchema 主键描述。
type pkSchema struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// fieldSchema 单字段描述。
type fieldSchema struct {
	Name      string `json:"name"`
	Type      string `json:"type"`  // Go 类型（int64/string/[]byte...）
	Index     string `json:"index"` // none/normal/unique
	Compress  bool   `json:"compress"`
	Repeated  bool   `json:"repeated"`
	Primary   bool   `json:"primary"`
	fieldNum  int32
	protoType descriptorpb.FieldDescriptorProto_Type
}

// newSchemaStore 创建空 schema 存储。
func newSchemaStore() *schemaStore {
	return &schemaStore{tables: map[uint64]*tableSchema{}}
}

// importProto 解析 .proto 源码并替换当前 schema。
// 与代码生成走同一条路：protoparse 解析 → codegen.BuildIR 提取表结构，
// 保证面板展示的 TableID 与生成代码写入数据的物理编码完全一致。
func (s *schemaStore) importProto(filename string, src string) error {
	// 文件访问器：上传文件 + 内嵌的 sqlitex/options.proto；
	// google/protobuf/*.proto 由 protoparse 内置兜底。
	files := map[string]string{
		filename:                src,
		"sqlitex/options.proto": sqlitexpb.OptionsProtoText,
	}
	p := protoparse.Parser{
		Accessor: func(name string) (io.ReadCloser, error) {
			content, ok := files[name]
			if !ok {
				return nil, fmt.Errorf("file not found: %s", name)
			}
			return io.NopCloser(strings.NewReader(content)), nil
		},
	}
	parsed, err := p.ParseFiles(filename)
	if err != nil {
		return fmt.Errorf("parse proto: %w", err)
	}

	// 收集目标文件 + 全部传递依赖（与 protoc CodeGeneratorRequest 同域），
	// 保证 BuildIR 的 TableID 编号与编译期一致。
	seen := map[string]bool{}
	var fdps []*descriptorpb.FileDescriptorProto
	var collect func(fd *desc.FileDescriptor)
	collect = func(fd *desc.FileDescriptor) {
		if seen[fd.GetName()] {
			return
		}
		seen[fd.GetName()] = true
		for _, dep := range fd.GetDependencies() {
			collect(dep)
		}
		if fd.GetName() != filename {
			fdps = append(fdps, fd.AsFileDescriptorProto())
		}
	}
	for _, fd := range parsed {
		collect(fd)
		if fd.GetName() == filename {
			fdps = append(fdps, fd.AsFileDescriptorProto())
		}
	}

	tables, err := codegen.BuildIR(fdps)
	if err != nil {
		return err
	}

	newTables := map[uint64]*tableSchema{}
	var newList []*tableSchema
	for _, t := range tables {
		ts := buildTableSchema(t)
		newTables[ts.TableID] = ts
		newList = append(newList, ts)
	}

	s.mu.Lock()
	s.source = filename
	s.tables = newTables
	s.list = newList
	s.mu.Unlock()
	return nil
}

// buildTableSchema 将 codegen 的 TableIR 转为面板 schema。
func buildTableSchema(t *codegen.TableIR) *tableSchema {
	ts := &tableSchema{
		TableID: t.TableID,
		Message: t.MessageName,
		PrimaryKey: pkSchema{
			Name: t.PrimaryKey.GoName,
			Type: t.PrimaryKey.GoType,
		},
		HasTTL:     t.HasTTL,
		byFieldNum: map[int32]int{},
	}
	if t.HasTTL {
		ts.TTL = t.TTL.String()
	}
	for i, f := range t.Fields {
		fs := fieldSchema{
			Name:      f.GoName,
			Type:      f.GoType,
			Compress:  f.Compress,
			Repeated:  f.IsRepeated,
			Primary:   f.IsPrimaryKey,
			fieldNum:  f.Number,
			protoType: f.ProtoType,
		}
		switch f.Index {
		case sqlitexpb.IndexOption_INDEX_UNIQUE:
			fs.Index = "unique"
		case sqlitexpb.IndexOption_INDEX_NORMAL:
			fs.Index = "normal"
		default:
			fs.Index = "none"
		}
		ts.Fields = append(ts.Fields, fs)
		ts.byFieldNum[f.Number] = i
	}
	ts.pkProto = t.PrimaryKey.ProtoType
	return ts
}

// clear 清空 schema。
func (s *schemaStore) clear() {
	s.mu.Lock()
	s.source = ""
	s.tables = map[uint64]*tableSchema{}
	s.list = nil
	s.mu.Unlock()
}

// snapshot 返回 schema 概览（source + 表列表）。
func (s *schemaStore) snapshot() (source string, tables []*tableSchema) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.source, s.list
}

// table 按 TableID 查表。
func (s *schemaStore) table(id uint64) *tableSchema {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tables[id]
}

// tableOf 从物理 Key 提取 TableHash 并查表；非数据键或未知表返回 nil。
func (s *schemaStore) tableOf(raw []byte) *tableSchema {
	if len(raw) < 9 || raw[0] != dataKeyPrefix {
		return nil
	}
	tableHash := binary.BigEndian.Uint64(raw[1:9])
	return s.table(tableHash)
}

// tableOfIndex 从索引键提取 TableHash 并查表；非索引键或未知表返回 nil。
func (s *schemaStore) tableOfIndex(raw []byte) *tableSchema {
	if len(raw) < 9 || raw[0] != indexKeyPrefix {
		return nil
	}
	tableHash := binary.BigEndian.Uint64(raw[1:9])
	return s.table(tableHash)
}

// ---- Key 解码 ----

// decodedKey 是物理 Key 的语义化解码结果。
type decodedKey struct {
	Kind       string `json:"kind"`                  // data / index / unknown
	Table      string `json:"table,omitempty"`       // 表名（未知表为空）
	PK         any    `json:"pk,omitempty"`          // 解码后的主键值
	IndexField string `json:"index_field,omitempty"` // 索引字段名
	IndexValue any    `json:"index_value,omitempty"` // 索引字段值
}

// decodeKey 按当前 schema 解码物理 Key。
// 无法识别（未导入 schema / 未知 TableHash / 非法编码）时 Kind=unknown。
func (s *schemaStore) decodeKey(raw []byte) decodedKey {
	d := decodedKey{Kind: "unknown"}
	if len(raw) < 9 {
		return d
	}
	if raw[0] == indexKeyPrefix {
		d.Kind = "index"
		tableHash := binary.BigEndian.Uint64(raw[1:9])
		ts := s.table(tableHash)
		if ts == nil {
			return d // index 类型已知但表未知
		}
		d.Table = ts.Message
		if len(raw) < 10 {
			d.Kind = "unknown"
			return d
		}
		fieldNum := int32(raw[9])
		rest := raw[10:]
		// 索引键布局 [ValueLen Uvarint][FieldValue][PK]：
		// 长度前缀切出无歧义的 FieldValue/PK 边界（与 encoding.EncodeIndexKey 一致）。
		vLen, nVal := binary.Uvarint(rest)
		if nVal <= 0 || uint64(len(rest)-nVal) < vLen {
			d.Kind = "unknown"
			return d
		}
		fieldValue := rest[nVal : nVal+int(vLen)]
		if idx, ok := ts.byFieldNum[fieldNum]; ok {
			d.IndexField = ts.Fields[idx].Name
			d.IndexValue = decodeScalarByType(ts.Fields[idx].protoType, fieldValue)
		} else {
			d.IndexValue = printable(fieldValue)
		}
		return d
	}

	if raw[0] != dataKeyPrefix {
		return d
	}
	tableHash := binary.BigEndian.Uint64(raw[1:9])
	ts := s.table(tableHash)
	if ts == nil {
		return d
	}
	d.Kind = "data"
	d.Table = ts.Message
	d.PK = decodePK(ts, raw[9:])
	return d
}

// comparePK 比较两个同表数据 Key 的主键顺序（数值感知）。
// 物理 Key 中 int 主键为小端编码，字节序不等于数值序（如 pk=256 字节序先于 pk=1），
// 表格视图需要按数值序排列时使用本函数；string/bytes 主键退化为字典序。
func comparePK(ts *tableSchema, a, b []byte) int {
	pa, pb := pkBytesOf(ts, a), pkBytesOf(ts, b)
	switch ts.PrimaryKey.Type {
	case "int64", "sint64":
		if len(pa) == 8 && len(pb) == 8 {
			return cmpInt64(int64(binary.LittleEndian.Uint64(pa)), int64(binary.LittleEndian.Uint64(pb)))
		}
	case "uint64":
		if len(pa) == 8 && len(pb) == 8 {
			return cmpUint64(binary.LittleEndian.Uint64(pa), binary.LittleEndian.Uint64(pb))
		}
	case "int32", "sint32":
		if len(pa) == 4 && len(pb) == 4 {
			return cmpInt64(int64(int32(binary.LittleEndian.Uint32(pa))), int64(int32(binary.LittleEndian.Uint32(pb))))
		}
	case "uint32":
		if len(pa) == 4 && len(pb) == 4 {
			return cmpInt64(int64(binary.LittleEndian.Uint32(pa)), int64(binary.LittleEndian.Uint32(pb)))
		}
	}
	return bytes.Compare(pa, pb)
}

// pkBytesOf 从数据 Key 中剥离 [0x00][TableHash 8B] 前缀，返回 PK bytes。
func pkBytesOf(ts *tableSchema, raw []byte) []byte {
	if len(raw) < 9 || raw[0] != dataKeyPrefix {
		return nil
	}
	return raw[9:]
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpUint64(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// decodePK 按主键类型解码 PK bytes（与生成代码 encodePrimaryKey 互逆）。
func decodePK(ts *tableSchema, b []byte) any {
	switch ts.PrimaryKey.Type {
	case "string":
		return string(b)
	case "int64", "uint64":
		if len(b) != 8 {
			return printable(b)
		}
		v := binary.LittleEndian.Uint64(b)
		if ts.PrimaryKey.Type == "int64" {
			return int64(v)
		}
		return v
	case "int32", "uint32":
		if len(b) != 4 {
			return printable(b)
		}
		v := binary.LittleEndian.Uint32(b)
		if ts.PrimaryKey.Type == "int32" {
			return int32(v)
		}
		return v
	default: // []byte 等变长主键
		return printable(b)
	}
}

// decodeScalarByType 按字段类型解码索引 Key 中的字段值（无长度前缀）。
func decodeScalarByType(t descriptorpb.FieldDescriptorProto_Type, b []byte) any {
	switch t {
	case descriptorpb.FieldDescriptorProto_TYPE_STRING:
		return string(b)
	case descriptorpb.FieldDescriptorProto_TYPE_INT64,
		descriptorpb.FieldDescriptorProto_TYPE_UINT64,
		descriptorpb.FieldDescriptorProto_TYPE_SINT64,
		descriptorpb.FieldDescriptorProto_TYPE_FIXED64,
		descriptorpb.FieldDescriptorProto_TYPE_SFIXED64:
		if len(b) == 8 {
			return int64(binary.LittleEndian.Uint64(b))
		}
	case descriptorpb.FieldDescriptorProto_TYPE_INT32,
		descriptorpb.FieldDescriptorProto_TYPE_UINT32,
		descriptorpb.FieldDescriptorProto_TYPE_SINT32,
		descriptorpb.FieldDescriptorProto_TYPE_FIXED32,
		descriptorpb.FieldDescriptorProto_TYPE_SFIXED32,
		descriptorpb.FieldDescriptorProto_TYPE_ENUM:
		if len(b) == 4 {
			return int32(binary.LittleEndian.Uint32(b))
		}
	}
	return printable(b)
}

// ---- Value 解码 ----

// decodedField 是单字段的解码值（字符串化表示）。
type decodedField struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Value     string `json:"value"`
	Truncated bool   `json:"truncated,omitempty"`
}

// decodedValue 是 Value 的语义化解码结果。
type decodedValue struct {
	Fields    []decodedField `json:"fields,omitempty"`
	ExpiresAt string         `json:"expires_at,omitempty"` // RFC3339，TTL 表专用
	Expired   bool           `json:"expired,omitempty"`
}

const maxFieldValueLen = 256 // 单字段展示上限，超长截断

var zstdDecoder, _ = zstd.NewReader(nil)

// decodeValue 按表结构顺序遍历解码扁平 Value。
// 任一字段解码失败即中止（后续字段偏移已不可信），已解出的部分保留。
func (s *schemaStore) decodeValue(ts *tableSchema, data []byte) decodedValue {
	var dv decodedValue
	off := 0
	if ts.HasTTL {
		if len(data) < 8 {
			return dv
		}
		exp := int64(binary.LittleEndian.Uint64(data))
		if exp > 0 {
			dv.ExpiresAt = time.Unix(0, exp).Format(time.RFC3339Nano)
			dv.Expired = exp < time.Now().UnixNano()
		}
		off = 8
	}
	for _, f := range ts.Fields {
		val, n, trunc, err := decodeOneField(f, data, off)
		if err != nil {
			dv.Fields = append(dv.Fields, decodedField{
				Name: f.Name, Type: f.Type,
				Value: fmt.Sprintf("«解码失败: %v»", err),
			})
			return dv
		}
		off = n
		dv.Fields = append(dv.Fields, decodedField{
			Name: f.Name, Type: f.Type, Value: val, Truncated: trunc,
		})
	}
	return dv
}

// decodeOneField 解码单个字段，返回（展示值, 新偏移, 是否截断, 错误）。
func decodeOneField(f fieldSchema, data []byte, off int) (string, int, bool, error) {
	if !f.Repeated && !f.Compress {
		if size := fixedProtoSize(f.protoType); size > 0 {
			if off+size > len(data) {
				return "", 0, false, fmt.Errorf("need %dB, have %dB", size, len(data)-off)
			}
			return decodeFixed(f.protoType, data[off:off+size]), off + size, false, nil
		}
	}
	// 变长字段（含压缩/repeated）：[u32 dataLen]([u32 origLen])[data]
	if off+4 > len(data) {
		return "", 0, false, fmt.Errorf("length prefix truncated")
	}
	vLen := int(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	if f.Compress {
		if off+4 > len(data) {
			return "", 0, false, fmt.Errorf("compress header truncated")
		}
		origLen := int(binary.LittleEndian.Uint32(data[off:]))
		off += 4
		if off+vLen > len(data) {
			return "", 0, false, fmt.Errorf("data truncated: need %dB", vLen)
		}
		raw := data[off : off+vLen]
		if vLen != origLen {
			dec, err := zstdDecoder.DecodeAll(raw, nil)
			if err != nil {
				return "", 0, false, fmt.Errorf("zstd: %v", err)
			}
			raw = dec
		}
		return previewBytes(raw), off + vLen, len(raw) > maxFieldValueLen, nil
	}
	if off+vLen > len(data) {
		return "", 0, false, fmt.Errorf("data truncated: need %dB, have %dB", vLen, len(data)-off)
	}
	raw := data[off : off+vLen]
	return previewBytes(raw), off + vLen, vLen > maxFieldValueLen, nil
}

// decodeFixed 解码定长标量的可读表示。
func decodeFixed(t descriptorpb.FieldDescriptorProto_Type, b []byte) string {
	switch t {
	case descriptorpb.FieldDescriptorProto_TYPE_BOOL:
		if b[0] != 0 {
			return "true"
		}
		return "false"
	case descriptorpb.FieldDescriptorProto_TYPE_FLOAT:
		return fmt.Sprintf("%v", math.Float32frombits(binary.LittleEndian.Uint32(b)))
	case descriptorpb.FieldDescriptorProto_TYPE_DOUBLE:
		return fmt.Sprintf("%v", math.Float64frombits(binary.LittleEndian.Uint64(b)))
	case descriptorpb.FieldDescriptorProto_TYPE_INT64,
		descriptorpb.FieldDescriptorProto_TYPE_SINT64,
		descriptorpb.FieldDescriptorProto_TYPE_SFIXED64:
		return fmt.Sprintf("%d", int64(binary.LittleEndian.Uint64(b)))
	case descriptorpb.FieldDescriptorProto_TYPE_UINT64,
		descriptorpb.FieldDescriptorProto_TYPE_FIXED64:
		return fmt.Sprintf("%d", binary.LittleEndian.Uint64(b))
	case descriptorpb.FieldDescriptorProto_TYPE_INT32,
		descriptorpb.FieldDescriptorProto_TYPE_SINT32,
		descriptorpb.FieldDescriptorProto_TYPE_SFIXED32,
		descriptorpb.FieldDescriptorProto_TYPE_ENUM:
		return fmt.Sprintf("%d", int32(binary.LittleEndian.Uint32(b)))
	case descriptorpb.FieldDescriptorProto_TYPE_UINT32,
		descriptorpb.FieldDescriptorProto_TYPE_FIXED32:
		return fmt.Sprintf("%d", binary.LittleEndian.Uint32(b))
	}
	return printable(b)
}

// fixedProtoSize 定长字段的物理字节数（与 codegen.fixedSize 一致）。
func fixedProtoSize(t descriptorpb.FieldDescriptorProto_Type) int {
	switch t {
	case descriptorpb.FieldDescriptorProto_TYPE_BOOL:
		return 1
	case descriptorpb.FieldDescriptorProto_TYPE_INT32,
		descriptorpb.FieldDescriptorProto_TYPE_UINT32,
		descriptorpb.FieldDescriptorProto_TYPE_SINT32,
		descriptorpb.FieldDescriptorProto_TYPE_FIXED32,
		descriptorpb.FieldDescriptorProto_TYPE_SFIXED32,
		descriptorpb.FieldDescriptorProto_TYPE_FLOAT,
		descriptorpb.FieldDescriptorProto_TYPE_ENUM:
		return 4
	case descriptorpb.FieldDescriptorProto_TYPE_INT64,
		descriptorpb.FieldDescriptorProto_TYPE_UINT64,
		descriptorpb.FieldDescriptorProto_TYPE_SINT64,
		descriptorpb.FieldDescriptorProto_TYPE_FIXED64,
		descriptorpb.FieldDescriptorProto_TYPE_SFIXED64,
		descriptorpb.FieldDescriptorProto_TYPE_DOUBLE:
		return 8
	}
	return 0
}

// previewBytes 变长字段的尽力可读展示：UTF-8 干净则直接展示，否则退化为 \xNN。
func previewBytes(b []byte) string {
	if utf8Clean(b) {
		s := string(b)
		if len(s) > maxFieldValueLen {
			return s[:maxFieldValueLen] + "…"
		}
		return s
	}
	return printable(b)
}

// utf8Clean 判断是否全部为可打印 UTF-8 文本。
func utf8Clean(b []byte) bool {
	for _, r := range string(b) {
		if r == 0xFFFD || (r < 0x20 && r != '\n' && r != '\t' && r != '\r') {
			return false
		}
	}
	return true
}

// encodeDataKeyPrefix 构造某表数据 Key 的物理前缀（用于表级过滤扫描）。
// 格式: [0x00][TableHash 8B 定宽]
func encodeDataKeyPrefix(tableHash uint64) []byte {
	buf := make([]byte, 9)
	buf[0] = dataKeyPrefix
	binary.BigEndian.PutUint64(buf[1:9], tableHash)
	return buf
}
