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

// TestGenerateMockCloneDeepCopy 验证克隆函数的深拷贝语义：
// repeated bytes 需内层逐元素拷贝，repeated 标量与普通 bytes 需外层重建。
func TestGenerateMockCloneDeepCopy(t *testing.T) {
	table := &TableIR{
		MessageName: "Blob",
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
			{Name: "chunks", GoType: "[][]byte", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_BYTES, Number: 2, IsRepeated: true},
			{Name: "tags", GoType: "[]string", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_STRING, Number: 3, IsRepeated: true},
			{Name: "avatar", GoType: "[]byte", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_BYTES, Number: 4},
			{Name: "size", GoType: "int64", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_INT64, Number: 5},
		},
	}

	code := GenerateMock(table)

	// repeated bytes：外层 make + 内层 append，杜绝内层切片共享
	for _, want := range []string{
		"dst.Chunks = make([][]byte, len(src.Chunks))",
		"for i := range src.Chunks {",
		"dst.Chunks[i] = append([]byte(nil), src.Chunks[i]...)",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("clone missing repeated-bytes deep copy: %q", want)
		}
	}
	// repeated string：外层 append 即可（元素不可变）
	if !strings.Contains(code, "dst.Tags = append(([]string)(nil), src.Tags...)") {
		t.Error("clone missing repeated-string outer copy")
	}
	// 普通 bytes：单层深拷贝
	if !strings.Contains(code, "dst.Avatar = append(([]byte)(nil), src.Avatar...)") {
		t.Error("clone missing bytes deep copy")
	}
	// 标量：直接赋值
	if !strings.Contains(code, "dst.Size = src.Size") {
		t.Error("clone missing scalar assign")
	}
}
