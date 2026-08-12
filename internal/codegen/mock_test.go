package codegen

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/types/descriptorpb"
)

// TestGenerateMockNoTTL 验证无 TTL 表不生成 PurgeExpired。
func TestGenerateMockNoTTL(t *testing.T) {
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
	}

	code := GenerateMock(table)

	if !strings.Contains(code, "func NewMockUserStore() *mockUserStore") {
		t.Error("missing mock constructor")
	}
	if strings.Contains(code, "PurgeExpired") {
		t.Error("non-TTL mock should not generate PurgeExpired")
	}
}

// TestGenerateMockTTL 验证 TTL 表生成接口兼容的 PurgeExpired（恒 0）。
func TestGenerateMockTTL(t *testing.T) {
	table := &TableIR{
		MessageName: "Session",
		GoPackage:   "genpkg",
		TableID:     3,
		HasTTL:      true,
		PrimaryKey: &FieldIR{
			Name:      "id",
			GoName:    "Id",
			GoType:    "int64",
			ProtoType: descriptorpb.FieldDescriptorProto_TYPE_INT64,
			Number:    1,
		},
	}

	code := GenerateMock(table)

	if !strings.Contains(code, "func (m *mockSessionStore) PurgeExpired() (int, error)") {
		t.Error("TTL mock should implement PurgeExpired for interface compatibility")
	}
	if !strings.Contains(code, "return 0, nil") {
		t.Error("TTL mock PurgeExpired should return 0 (no TTL simulation)")
	}
}