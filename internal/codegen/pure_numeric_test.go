package codegen

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	sqlitexpb "github.com/mogumc/sqlitex/proto/sqlitex"
)

// TestPureNumericTable 验证纯数值字段表生成代码无未使用 import/变量。
func TestPureNumericTable(t *testing.T) {
	// 构造纯数值表（计数器/积分表场景）
	files := []*descriptorpb.FileDescriptorProto{
		{
			Name:    strPtr("counter.proto"),
			Package: strPtr("test"),
			Options: &descriptorpb.FileOptions{
				GoPackage: strPtr("github.com/mogumc/sqlitex/test"),
			},
			MessageType: []*descriptorpb.DescriptorProto{
				{
					Name:    strPtr("Counter"),
					Options: &descriptorpb.MessageOptions{},
					Field: []*descriptorpb.FieldDescriptorProto{
						{
							Name:   strPtr("id"),
							Number: intPtr(1),
							Type:   typPtr(descriptorpb.FieldDescriptorProto_TYPE_INT64),
							Label:  labelPtr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
						},
						{
							Name:   strPtr("value"),
							Number: intPtr(2),
							Type:   typPtr(descriptorpb.FieldDescriptorProto_TYPE_INT64),
							Label:  labelPtr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
						},
						{
							Name:   strPtr("active"),
							Number: intPtr(3),
							Type:   typPtr(descriptorpb.FieldDescriptorProto_TYPE_BOOL),
							Label:  labelPtr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
						},
					},
				},
			},
		},
	}

	// 注入 table option
	tableOpt := &sqlitexpb.TableOption{PrimaryKey: "id"}
	proto.SetExtension(files[0].MessageType[0].Options, sqlitexpb.E_Table, tableOpt)

	// 构建 IR
	tables, err := BuildIR(files)
	if err != nil {
		t.Fatalf("BuildIR: %v", err)
	}
	if len(tables) != 1 {
		t.Fatalf("expected 1 table, got %d", len(tables))
	}

	// 生成代码
	serializerCode := GenerateSerializer(tables[0])
	queryCode, err := GenerateQuery(tables[0])
	if err != nil {
		t.Fatalf("GenerateQuery: %v", err)
	}

	// 验证 serializer 不含 vLen 声明（无变长字段）
	if strings.Contains(serializerCode, "var vLen int") {
		t.Error("serializer should not declare vLen for pure numeric table")
	}

	// 验证 query 不含 strings import（无 string 字段）
	if strings.Contains(queryCode, `"strings"`) {
		t.Error("query should not import strings for pure numeric table")
	}

	// 验证生成的代码可编译（通过 go build 检查）
	// 这里只做文本检查，实际编译验证在集成测试中
	t.Logf("Serializer code length: %d bytes", len(serializerCode))
	t.Logf("Query code length: %d bytes", len(queryCode))
}
