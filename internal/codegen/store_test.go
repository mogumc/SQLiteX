package codegen

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/types/descriptorpb"
)

func TestGenerateStore(t *testing.T) {
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
		},
	}

	code := GenerateStore(table)

	// 关键结构检查
	checks := []string{
		"type UserStore interface",
		"Create(m *User) error",
		"Update(m *User) error",
		"Delete(Id int64) error",
		"Get(Id int64) (*User, error)",
		"func NewUserStore(db *sqlitex.DB) UserStore",
		"func (s *userStore) Create(m *User) error",
		"encoding.EncodeKey",
		"Serialize()",
		"DeserializeUser",
	}

	for _, check := range checks {
		if !strings.Contains(code, check) {
			t.Errorf("generated code missing: %q", check)
		}
	}

	// 主键编码检查
	if !strings.Contains(code, "binary.LittleEndian.PutUint64(buf, uint64(Id))") {
		t.Error("missing primary key encoding for int64")
	}

	t.Logf("Generated store code (%d bytes):\n%s", len(code), code)
}

func TestGenerateStoreStringPK(t *testing.T) {
	table := &TableIR{
		MessageName: "Document",
		GoPackage:   "genpkg",
		TableID:     2,
		PrimaryKey: &FieldIR{
			Name:      "doc_id",
			GoName:    "DocId",
			GoType:    "string",
			ProtoType: descriptorpb.FieldDescriptorProto_TYPE_STRING,
			Number:    1,
		},
		Fields: []*FieldIR{
			{Name: "doc_id", GoType: "string", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_STRING, Number: 1, IsPrimaryKey: true},
			{Name: "content", GoType: "string", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_STRING, Number: 2},
		},
	}

	code := GenerateStore(table)

	// 字符串主键检查
	if !strings.Contains(code, "return []byte(DocId)") {
		t.Error("missing string primary key encoding")
	}

	if !strings.Contains(code, "Delete(DocId string) error") {
		t.Error("missing string parameter in Delete method")
	}
}

func TestLowerFirst(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"User", "user"},
		{"UserStore", "userStore"},
		{"ID", "iD"},
		{"already", "already"},
		{"", ""},
	}

	for _, tt := range tests {
		got := lowerFirst(tt.input)
		if got != tt.want {
			t.Errorf("lowerFirst(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
