// Package codegen 实现 protoc-gen-sqlitex 的代码生成逻辑。
//
// 核心流程：Protobuf Descriptor → IR（中间表示） → Go 源码生成。
// IR 层是承上启下的枢纽，隔离 protoc 协议细节与代码生成模板。
package codegen

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	sqlitexpb "github.com/mogumc/sqlitex/proto/sqlitex"
)

// TableIR 是单张表的中间表示，承载一个 Message 的完整结构描述。
type TableIR struct {
	// MessageName 是原始 Message 名称，如 "User"。
	MessageName string
	// StoreName 是生成的 Store 接口名，如 "UserStore"。
	StoreName string
	// PrimaryKey 是主键字段的 IR。
	PrimaryKey *FieldIR
	// Fields 按声明顺序排列的所有字段。
	Fields []*FieldIR
	// IndexedFields 需要二级索引的字段（不含主键）。
	IndexedFields []*FieldIR
	// CompressibleFields 需要压缩的变长字段。
	CompressibleFields []*FieldIR
	// TableCompress 表级压缩开关。
	TableCompress bool
	// CompressThreshold 压缩阈值（字节），默认 256。
	CompressThreshold int32
	// TableID 运行时表标识，由 Message 序号决定。
	TableID uint64
	// GoPackage 生成代码的目标 Go 包名。
	GoPackage string
}

// FieldIR 是单个字段的中间表示。
type FieldIR struct {
	// Name 是字段原始名称，如 "id"。
	Name string
	// GoName 是 Go 标识符名称，如 "Id"。
	GoName string
	// GoType 是对应的 Go 类型，如 "int64"、"string"、"[]byte"。
	GoType string
	// ProtoType 是 protobuf 字段类型枚举。
	ProtoType descriptorpb.FieldDescriptorProto_Type
	// Number 是字段编号。
	Number int32
	// Index 是索引类型（INDEX_NONE/INDEX_NORMAL/INDEX_UNIQUE）。
	Index sqlitexpb.IndexOption
	// Compress 字段级压缩开关。
	Compress bool
	// IsPrimaryKey 是否为主键字段。
	IsPrimaryKey bool
	// TTL 过期时间字符串。
	TTL string
	// IsRepeated 是否为 repeated 字段。
	IsRepeated bool
}

// BuildIR 从 protobuf FileDescriptor 构建 TableIR 列表。
// 仅处理标记了 (sqlitex.table) option 的 Message。
func BuildIR(files []*descriptorpb.FileDescriptorProto) ([]*TableIR, error) {
	var tables []*TableIR
	var tableID uint64

	for _, file := range files {
		goPkg := resolveGoPackage(file)

		for _, msg := range file.MessageType {
			// 跳过 protoc 内部类型和嵌套 Message（Phase 1 仅支持顶层）
			if isProtobufInternalMessage(msg.GetName()) {
				continue
			}

			tableOpt := extractTableOption(msg)
			if tableOpt == nil {
				continue
			}

			tableID++
			table, err := buildTableIR(msg, tableOpt, goPkg, tableID)
			if err != nil {
				return nil, fmt.Errorf("message %s: %w", msg.GetName(), err)
			}
			tables = append(tables, table)
		}
	}
	return tables, nil
}

// buildTableIR 从单个 Message 构建 TableIR。
func buildTableIR(
	msg *descriptorpb.DescriptorProto,
	tableOpt *sqlitexpb.TableOption,
	goPkg string,
	tableID uint64,
) (*TableIR, error) {
	table := &TableIR{
		MessageName:       msg.GetName(),
		StoreName:         msg.GetName() + "Store",
		GoPackage:         goPkg,
		TableID:           tableID,
		TableCompress:     tableOpt.GetCompress(),
		CompressThreshold: tableOpt.GetCompressThreshold(),
	}

	if table.CompressThreshold <= 0 {
		table.CompressThreshold = 256
	}

	// 解析所有字段
	for _, field := range msg.Field {
		fir, err := buildFieldIR(field)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", field.GetName(), err)
		}
		table.Fields = append(table.Fields, fir)

		// 识别主键
		if fir.IsPrimaryKey || fir.Name == tableOpt.GetPrimaryKey() {
			fir.IsPrimaryKey = true
			if table.PrimaryKey != nil {
				return nil, fmt.Errorf("duplicate primary key: %s and %s", table.PrimaryKey.Name, fir.Name)
			}
			table.PrimaryKey = fir
		}

		// 收集索引字段（不含主键）
		if fir.Index != sqlitexpb.IndexOption_INDEX_NONE && !fir.IsPrimaryKey {
			table.IndexedFields = append(table.IndexedFields, fir)
		}

		// 收集可压缩字段
		if shouldCompress(fir, table.TableCompress, table.CompressThreshold) {
			table.CompressibleFields = append(table.CompressibleFields, fir)
		}
	}

	if table.PrimaryKey == nil {
		return nil, fmt.Errorf("primary key %q not found in message fields", tableOpt.GetPrimaryKey())
	}

	return table, nil
}

// buildFieldIR 从 FieldDescriptor 构建 FieldIR。
func buildFieldIR(field *descriptorpb.FieldDescriptorProto) (*FieldIR, error) {
	goType, err := protoTypeToGo(field.GetType())
	if err != nil {
		return nil, err
	}

	fir := &FieldIR{
		Name:       field.GetName(),
		GoName:     field.GetName(), // 后续由模板层做首字母大写
		GoType:     goType,
		ProtoType:  field.GetType(),
		Number:     field.GetNumber(),
		Index:      sqlitexpb.IndexOption_INDEX_NONE,
		IsRepeated: field.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED,
	}

	// repeated 字段映射为 Go slice
	if fir.IsRepeated {
		fir.GoType = "[]" + goType
	}

	// 提取自定义 FieldOption
	if opt := extractFieldOption(field); opt != nil {
		fir.Index = opt.GetIndex()
		fir.Compress = opt.GetCompress()
		fir.IsPrimaryKey = opt.GetPrimaryKey()
		fir.TTL = opt.GetTtl()
	}

	return fir, nil
}

// protoTypeToGo 将 protobuf 类型映射为 Go 基础类型。
func protoTypeToGo(t descriptorpb.FieldDescriptorProto_Type) (string, error) {
	switch t {
	case descriptorpb.FieldDescriptorProto_TYPE_DOUBLE:
		return "float64", nil
	case descriptorpb.FieldDescriptorProto_TYPE_FLOAT:
		return "float32", nil
	case descriptorpb.FieldDescriptorProto_TYPE_INT64,
		descriptorpb.FieldDescriptorProto_TYPE_SINT64,
		descriptorpb.FieldDescriptorProto_TYPE_SFIXED64:
		return "int64", nil
	case descriptorpb.FieldDescriptorProto_TYPE_UINT64,
		descriptorpb.FieldDescriptorProto_TYPE_FIXED64:
		return "uint64", nil
	case descriptorpb.FieldDescriptorProto_TYPE_INT32,
		descriptorpb.FieldDescriptorProto_TYPE_SINT32,
		descriptorpb.FieldDescriptorProto_TYPE_SFIXED32:
		return "int32", nil
	case descriptorpb.FieldDescriptorProto_TYPE_UINT32,
		descriptorpb.FieldDescriptorProto_TYPE_FIXED32:
		return "uint32", nil
	case descriptorpb.FieldDescriptorProto_TYPE_BOOL:
		return "bool", nil
	case descriptorpb.FieldDescriptorProto_TYPE_STRING:
		return "string", nil
	case descriptorpb.FieldDescriptorProto_TYPE_BYTES:
		return "[]byte", nil
	case descriptorpb.FieldDescriptorProto_TYPE_ENUM:
		return "int32", nil // 枚举在 Go 中映射为 int32
	default:
		return "", fmt.Errorf("unsupported proto type: %v", t)
	}
}

// shouldCompress 判断字段是否需要压缩。
func shouldCompress(f *FieldIR, tableCompress bool, threshold int32) bool {
	if f.IsRepeated {
		return false
	}
	// 仅 string/bytes 可压缩
	if f.ProtoType != descriptorpb.FieldDescriptorProto_TYPE_STRING &&
		f.ProtoType != descriptorpb.FieldDescriptorProto_TYPE_BYTES {
		return false
	}
	// 字段级显式开关优先
	if f.Compress {
		return true
	}
	// 表级开关（字段未显式关闭时生效）
	return tableCompress
}

// extractTableOption 从 Message 的 Options 中提取 (sqlitex.table)。
func extractTableOption(msg *descriptorpb.DescriptorProto) *sqlitexpb.TableOption {
	opts := msg.GetOptions()
	if opts == nil {
		return nil
	}
	if !proto.HasExtension(opts, sqlitexpb.E_Table) {
		return nil
	}
	ext := proto.GetExtension(opts, sqlitexpb.E_Table)
	tableOpt, ok := ext.(*sqlitexpb.TableOption)
	if !ok {
		return nil
	}
	return tableOpt
}

// extractFieldOption 从 Field 的 Options 中提取 (sqlitex.field)。
func extractFieldOption(field *descriptorpb.FieldDescriptorProto) *sqlitexpb.FieldOption {
	opts := field.GetOptions()
	if opts == nil {
		return nil
	}
	if !proto.HasExtension(opts, sqlitexpb.E_Field) {
		return nil
	}
	ext := proto.GetExtension(opts, sqlitexpb.E_Field)
	fieldOpt, ok := ext.(*sqlitexpb.FieldOption)
	if !ok {
		return nil
	}
	return fieldOpt
}

// resolveGoPackage 从 FileDescriptor 中解析 Go 包名。
func resolveGoPackage(file *descriptorpb.FileDescriptorProto) string {
	opts := file.GetOptions()
	if opts == nil {
		return ""
	}
	gopkg := opts.GetGoPackage()
	if gopkg == "" {
		return ""
	}
	// go_package 可能包含路径信息，如 "github.com/foo/bar;pkgname"
	if idx := strings.LastIndex(gopkg, ";"); idx >= 0 {
		return gopkg[idx+1:]
	}
	// 或完整路径 "github.com/foo/bar"，取最后一段
	if idx := strings.LastIndex(gopkg, "/"); idx >= 0 {
		return gopkg[idx+1:]
	}
	return gopkg
}

// isProtobufInternalMessage 跳过 protobuf 内部类型（如 FileOptions 等）。
func isProtobufInternalMessage(name string) bool {
	// 跳过 protoc 自身生成的辅助类型
	// 实际上这些不会出现在用户 proto 文件中，保险起见做过滤
	return strings.HasPrefix(name, "_")
}
