package codegen

import (
	"testing"

	"google.golang.org/protobuf/types/descriptorpb"
)

func TestGenerateSerializer(t *testing.T) {
	table := &TableIR{
		MessageName: "User",
		GoPackage:   "genpkg",
		TableID:     1,
		Fields: []*FieldIR{
			{Name: "id", GoType: "int64", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_INT64, Number: 1, IsPrimaryKey: true},
			{Name: "name", GoType: "string", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_STRING, Number: 2},
			{Name: "score", GoType: "float64", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_DOUBLE, Number: 3},
			{Name: "active", GoType: "bool", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_BOOL, Number: 4},
			{Name: "avatar", GoType: "[]byte", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_BYTES, Number: 5},
			{Name: "age", GoType: "int32", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_INT32, Number: 6},
		},
	}

	code := GenerateSerializer(table)

	// 基本结构验证
	if len(code) == 0 {
		t.Fatal("generated code is empty")
	}

	// 关键函数存在性检查
	checks := []string{
		"func (m *User) Serialize() []byte",
		"func DeserializeUser(data []byte) (*User, error)",
		"func (m *User) Size() int",
		"binary.LittleEndian",
		"package genpkg",
	}
	for _, check := range checks {
		if !containsStr(code, check) {
			t.Errorf("generated code missing: %q", check)
		}
	}

	// 变长字段应有长度前缀写入
	if !containsStr(code, "uint32(len(m.Name))") {
		t.Error("missing varlen length prefix for Name field")
	}
	if !containsStr(code, "uint32(len(m.Avatar))") {
		t.Error("missing varlen length prefix for Avatar field")
	}

	// 固定字段应直接 LittleEndian 写入
	if !containsStr(code, "binary.LittleEndian.PutUint64(buf[off:], uint64(m.Id))") {
		t.Error("missing fixed-size write for Id field")
	}
	if !containsStr(code, "math.Float64bits(m.Score)") {
		t.Error("missing float64 write for Score field")
	}

	// 反序列化应有截断检查
	if !containsStr(code, "data too short") {
		t.Error("missing data-too-short check in Deserialize")
	}

	t.Logf("Generated serializer code (%d bytes):\n%s", len(code), code)
}

func TestGenerateSerializerFixedOnly(t *testing.T) {
	table := &TableIR{
		MessageName: "Point",
		GoPackage:   "genpkg",
		TableID:     2,
		Fields: []*FieldIR{
			{Name: "x", GoType: "float64", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_DOUBLE, Number: 1, IsPrimaryKey: true},
			{Name: "y", GoType: "float64", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_DOUBLE, Number: 2},
			{Name: "z", GoType: "float32", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_FLOAT, Number: 3},
		},
	}

	code := GenerateSerializer(table)

	// 全固定长度：MinSize = 8+8+4 = 20
	if !containsStr(code, "size := 20") {
		t.Error("expected fixed total size 20 in Size()")
	}
	if !containsStr(code, "len(data) < 20") {
		t.Error("expected min size check 20 in Deserialize")
	}
}

func TestToGoName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"id", "Id"},
		{"user_name", "UserName"},
		{"created_at", "CreatedAt"},
		{"a_b_c", "ABC"},
		{"Name", "Name"},
	}
	for _, tt := range tests {
		got := toGoName(tt.input)
		if got != tt.want {
			t.Errorf("toGoName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
