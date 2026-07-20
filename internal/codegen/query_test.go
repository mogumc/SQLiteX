package codegen

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/types/descriptorpb"
)

func TestGenerateQuery(t *testing.T) {
	table := &TableIR{
		MessageName: "User",
		GoPackage:   "genpkg",
		TableID:     1,
		PrimaryKey: &FieldIR{
			Name:      "id",
			GoName:    "Id",
			GoType:    "int64",
			ProtoType: descriptorpb.FieldDescriptorProto_TYPE_INT64,
			Number:    1,
		},
		Fields: []*FieldIR{
			{Name: "id", GoType: "int64", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_INT64, Number: 1, IsPrimaryKey: true},
			{Name: "name", GoType: "string", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_STRING, Number: 2},
			{Name: "email", GoType: "string", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_STRING, Number: 3},
			{Name: "age", GoType: "int32", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_INT32, Number: 4},
			{Name: "score", GoType: "float64", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_DOUBLE, Number: 5},
			{Name: "active", GoType: "bool", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_BOOL, Number: 6},
			{Name: "tags", GoType: "[]string", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_STRING, Number: 7, IsRepeated: true},
		},
	}

	code, err := GenerateQuery(table)
	if err != nil {
		t.Fatalf("GenerateQuery failed: %v", err)
	}

	// 关键结构检查
	checks := []string{
		"type UserQuery struct",
		"func NewUserQuery(db *sqlitex.DB) *UserQuery",
		"func (q *UserQuery) WhereName(",
		"func (q *UserQuery) WhereEmail(",
		"func (q *UserQuery) WhereAge(",
		"func (q *UserQuery) WhereScore(",
		"func (q *UserQuery) WhereActive(",
		"func (q *UserQuery) Limit(n int) *UserQuery",
		"func (q *UserQuery) AfterKey(lastKey []byte) *UserQuery",
		"func (q *UserQuery) Exec() ([]*User, error)",
		"func (q *UserQuery) First() (*User, error)",
		"func (q *UserQuery) Count() (int, error)",
		// repeated 字段 tags 不应生成 WhereTags
	}

	for _, check := range checks {
		if !strings.Contains(code, check) {
			t.Errorf("generated code missing: %q", check)
		}
	}

	// repeated 字段不应生成 Where 方法
	if strings.Contains(code, "WhereTags(") {
		t.Error("repeated field 'tags' should not generate WhereTags method")
	}

	t.Logf("Generated query code (%d bytes):\n%s", len(code), code)
}

func TestGenerateQueryNumericOps(t *testing.T) {
	table := &TableIR{
		MessageName: "Record",
		GoPackage:   "genpkg",
		TableID:     2,
		PrimaryKey: &FieldIR{
			Name:      "id",
			GoType:    "int64",
			ProtoType: descriptorpb.FieldDescriptorProto_TYPE_INT64,
		},
		Fields: []*FieldIR{
			{Name: "id", GoType: "int64", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_INT64, IsPrimaryKey: true},
			{Name: "count", GoType: "int32", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_INT32, Number: 2},
		},
	}

	code, err := GenerateQuery(table)
	if err != nil {
		t.Fatalf("GenerateQuery failed: %v", err)
	}

	// 数值字段应生成 > / < / >= / <= 比较
	numericOps := []string{
		"case \">\":",
		"case \"<\":",
		"case \">=\":",
		"case \"<=\":",
	}
	for _, op := range numericOps {
		if !strings.Contains(code, op) {
			t.Errorf("numeric field should support %s comparison", op)
		}
	}
}

func TestGenerateQueryStringLike(t *testing.T) {
	table := &TableIR{
		MessageName: "Doc",
		GoPackage:   "genpkg",
		TableID:     3,
		PrimaryKey: &FieldIR{
			Name:      "id",
			GoType:    "string",
			ProtoType: descriptorpb.FieldDescriptorProto_TYPE_STRING,
		},
		Fields: []*FieldIR{
			{Name: "id", GoType: "string", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_STRING, IsPrimaryKey: true},
			{Name: "title", GoType: "string", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_STRING, Number: 2},
		},
	}

	code, err := GenerateQuery(table)
	if err != nil {
		t.Fatalf("GenerateQuery failed: %v", err)
	}

	// 字符串字段应支持 LIKE
	if !strings.Contains(code, "strings.Contains") {
		t.Error("string field should support LIKE with strings.Contains")
	}
}
