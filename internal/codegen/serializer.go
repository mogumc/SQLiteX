package codegen

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"google.golang.org/protobuf/types/descriptorpb"
)

// GenerateSerializer 生成零反射的 Serialize/Deserialize/Size 代码。
func GenerateSerializer(table *TableIR) string {
	data := buildSerializerData(table)

	var buf bytes.Buffer
	t := template.Must(template.New("serializer").Parse(serializerTmpl))
	if err := t.Execute(&buf, data); err != nil {
		panic(fmt.Sprintf("serializer template execute: %v", err))
	}
	return buf.String()
}

// serializerData 是序列化模板的数据输入。
type serializerData struct {
	MessageName string
	PackageName string
	Fields      []serFieldInfo
	MinSize     int // 最小合法数据长度（仅含固定长度字段）
}

// serFieldInfo 描述单个字段在序列化中的行为。
type serFieldInfo struct {
	GoName   string // Go 字段名（首字母大写）
	GoType   string // Go 类型
	IsVarLen bool   // 是否变长（string/bytes/repeated）
	FixedLen int    // 固定字节数（变长字段为 0）
	Compress bool   // 是否启用 zstd 压缩（仅变长字段生效）
}

func buildSerializerData(table *TableIR) serializerData {
	var fields []serFieldInfo
	minSize := 0

	// 构建压缩字段名集合，O(1) 查找
	compressSet := make(map[string]bool)
	for _, f := range table.CompressibleFields {
		compressSet[f.Name] = true
	}

	for _, f := range table.Fields {
		fi := serFieldInfo{
			GoName:   toGoName(f.Name),
			GoType:   f.GoType,
			Compress: compressSet[f.Name],
		}

		if f.IsRepeated {
			fi.IsVarLen = true
			fi.FixedLen = 0
		} else {
			fi.FixedLen = fixedSize(f)
			fi.IsVarLen = fi.FixedLen == 0
		}

		fields = append(fields, fi)
		if !fi.IsVarLen {
			minSize += fi.FixedLen
		}
		// 压缩字段额外 8 字节 headroom (dataLen + originalLen)
		if fi.Compress && fi.IsVarLen {
			minSize += 8
		}
	}

	return serializerData{
		MessageName: table.MessageName,
		PackageName: table.GoPackage,
		Fields:      fields,
		MinSize:     minSize,
	}
}

// fixedSize 返回固定长度字段的字节数，变长字段返回 0。
func fixedSize(f *FieldIR) int {
	switch f.ProtoType {
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
	case descriptorpb.FieldDescriptorProto_TYPE_STRING,
		descriptorpb.FieldDescriptorProto_TYPE_BYTES:
		return 0 // 变长
	default:
		return 0
	}
}

// toGoName 将 snake_case 转为 PascalCase。
func toGoName(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

var serializerTmpl = `package {{.PackageName}}

import (
	"encoding/binary"
	"fmt"
	"math"

	zstd "github.com/klauspost/compress/zstd"
)

var (
	_zstdEnc *zstd.Encoder
	_zstdDec *zstd.Decoder
)

func init() {
	var err error
	_zstdEnc, err = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil { panic(err) }
	_zstdDec, err = zstd.NewReader(nil, zstd.WithDecoderConcurrency(0))
	if err != nil { panic(err) }
}

func _compressZstd(src []byte) []byte {
	return _zstdEnc.EncodeAll(src, nil)
}

func _decompressZstd(src []byte) ([]byte, error) {
	return _zstdDec.DecodeAll(src, nil)
}

var _ = math.Float32bits

// Serialize 将 {{.MessageName}} 序列化为字节切片。
func (m *{{.MessageName}}) Serialize() []byte {
	size := m.Size()
	buf := make([]byte, size)
	off := 0
{{- range .Fields}}
{{- if .IsVarLen}}
{{- if .Compress}}
	// {{.GoName}} (compressible varlen): [uint32 dataLen][uint32 originalLen][data]
	raw := {{if eq .GoType "string"}}[]byte(m.{{.GoName}}){{else}}m.{{.GoName}}{{end}}
	compressed := _compressZstd(raw)
	if len(compressed) < len(raw) {
		binary.LittleEndian.PutUint32(buf[off:], uint32(len(compressed)))
		off += 4
		binary.LittleEndian.PutUint32(buf[off:], uint32(len(raw)))
		off += 4
		copy(buf[off:], compressed)
		off += len(compressed)
	} else {
		binary.LittleEndian.PutUint32(buf[off:], uint32(len(raw)))
		off += 4
		binary.LittleEndian.PutUint32(buf[off:], uint32(len(raw)))
		off += 4
		copy(buf[off:], raw)
		off += len(raw)
	}
{{- else}}
	// {{.GoName}} (varlen): uint32 length prefix + data
	binary.LittleEndian.PutUint32(buf[off:], uint32(len(m.{{.GoName}})))
	off += 4
	copy(buf[off:], m.{{.GoName}})
	off += len(m.{{.GoName}})
{{- end}}
{{- else if eq .GoType "bool"}}
	// {{.GoName}} (fixed {{.FixedLen}}B)
	if m.{{.GoName}} {
		buf[off] = 1
	} else {
		buf[off] = 0
	}
	off += {{.FixedLen}}
{{- else if eq .GoType "float32"}}
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(m.{{.GoName}}))
	off += {{.FixedLen}}
{{- else if eq .GoType "float64"}}
	binary.LittleEndian.PutUint64(buf[off:], math.Float64bits(m.{{.GoName}}))
	off += {{.FixedLen}}
{{- else if eq .GoType "int32" "uint32"}}
	binary.LittleEndian.PutUint32(buf[off:], uint32(m.{{.GoName}}))
	off += {{.FixedLen}}
{{- else if eq .GoType "int64" "uint64"}}
	binary.LittleEndian.PutUint64(buf[off:], uint64(m.{{.GoName}}))
	off += {{.FixedLen}}
{{- end}}
{{- end}}
	return buf
}

// Deserialize{{.MessageName}} 从字节切片反序列化为 {{.MessageName}}。
func Deserialize{{.MessageName}}(data []byte) (*{{.MessageName}}, error) {
	if len(data) < {{.MinSize}} {
		return nil, fmt.Errorf("sqlitex: {{$.MessageName}} data too short: %d < {{.MinSize}}", len(data))
	}
	m := &{{.MessageName}}{}
	off := 0
	var vLen int
{{- range .Fields}}
{{- if .IsVarLen}}
{{- if .Compress}}
	// {{.GoName}} (compressible varlen)
	if off+8 > len(data) {
		return nil, fmt.Errorf("sqlitex: {{$.MessageName}}.{{.GoName}} truncated")
	}
	vLen = int(binary.LittleEndian.Uint32(data[off:]))
	origLen := int(binary.LittleEndian.Uint32(data[off+4:]))
	off += 8
	if off+vLen > len(data) {
		return nil, fmt.Errorf("sqlitex: {{$.MessageName}}.{{.GoName}} data truncated: need %d, have %d", vLen, len(data)-off)
	}
	if vLen == origLen {
		m.{{.GoName}} = {{if eq .GoType "string"}}string(data[off:off+vLen]){{else}}append([]byte(nil), data[off:off+vLen]...){{end}}
	} else {
		dec, err := _decompressZstd(data[off:off+vLen])
		if err != nil {
			return nil, fmt.Errorf("sqlitex: {{$.MessageName}}.{{.GoName}} zstd decompress: %w", err)
		}
		m.{{.GoName}} = {{if eq .GoType "string"}}string(dec){{else}}dec{{end}}
	}
	off += vLen
{{- else}}
	// {{.GoName}} (varlen)
	if off+4 > len(data) {
		return nil, fmt.Errorf("sqlitex: {{$.MessageName}}.{{.GoName}} length prefix truncated")
	}
	vLen = int(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	if off+vLen > len(data) {
		return nil, fmt.Errorf("sqlitex: {{$.MessageName}}.{{.GoName}} data truncated: need %d, have %d", vLen, len(data)-off)
	}
	m.{{.GoName}} = {{if eq .GoType "string"}}string(data[off:off+vLen]){{else}}append([]byte(nil), data[off:off+vLen]...){{end}}
	off += vLen
{{- end}}
{{- else if eq .GoType "bool"}}
	if off+{{.FixedLen}} > len(data) {
		return nil, fmt.Errorf("sqlitex: {{$.MessageName}}.{{.GoName}} truncated")
	}
	m.{{.GoName}} = data[off] != 0
	off += {{.FixedLen}}
{{- else if eq .GoType "float32"}}
	if off+{{.FixedLen}} > len(data) {
		return nil, fmt.Errorf("sqlitex: {{$.MessageName}}.{{.GoName}} truncated")
	}
	m.{{.GoName}} = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
	off += {{.FixedLen}}
{{- else if eq .GoType "float64"}}
	if off+{{.FixedLen}} > len(data) {
		return nil, fmt.Errorf("sqlitex: {{$.MessageName}}.{{.GoName}} truncated")
	}
	m.{{.GoName}} = math.Float64frombits(binary.LittleEndian.Uint64(data[off:]))
	off += {{.FixedLen}}
{{- else if eq .GoType "int32"}}
	if off+{{.FixedLen}} > len(data) {
		return nil, fmt.Errorf("sqlitex: {{$.MessageName}}.{{.GoName}} truncated")
	}
	m.{{.GoName}} = int32(binary.LittleEndian.Uint32(data[off:]))
	off += {{.FixedLen}}
{{- else if eq .GoType "uint32"}}
	if off+{{.FixedLen}} > len(data) {
		return nil, fmt.Errorf("sqlitex: {{$.MessageName}}.{{.GoName}} truncated")
	}
	m.{{.GoName}} = binary.LittleEndian.Uint32(data[off:])
	off += {{.FixedLen}}
{{- else if eq .GoType "int64"}}
	if off+{{.FixedLen}} > len(data) {
		return nil, fmt.Errorf("sqlitex: {{$.MessageName}}.{{.GoName}} truncated")
	}
	m.{{.GoName}} = int64(binary.LittleEndian.Uint64(data[off:]))
	off += {{.FixedLen}}
{{- else if eq .GoType "uint64"}}
	if off+{{.FixedLen}} > len(data) {
		return nil, fmt.Errorf("sqlitex: {{$.MessageName}}.{{.GoName}} truncated")
	}
	m.{{.GoName}} = binary.LittleEndian.Uint64(data[off:])
	off += {{.FixedLen}}
{{- end}}
{{- end}}
	return m, nil
}

// Size 返回序列化所需的缓冲区大小（上限估计）。
func (m *{{.MessageName}}) Size() int {
	size := {{.MinSize}}
{{- range .Fields}}
{{- if .IsVarLen}}
	size += 4 + len(m.{{.GoName}})
{{- end}}
{{- end}}
	return size
}
`
