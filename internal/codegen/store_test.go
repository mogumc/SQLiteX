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

// TestGenerateStoreNoTTL 验证未声明 TTL 的表不生成任何 TTL 相关代码。
func TestGenerateStoreNoTTL(t *testing.T) {
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

	// 无 TTL: 不生成 PurgeExpired / deleteWithIndexes / SerializeWithExpiry
	if strings.Contains(code, "PurgeExpired") {
		t.Error("non-TTL store should not generate PurgeExpired")
	}
	if strings.Contains(code, "deleteUserWithIndexes") {
		t.Error("non-TTL store should not generate deleteWithIndexes")
	}
	if strings.Contains(code, "SerializeWithExpiry") {
		t.Error("non-TTL store should use plain Serialize()")
	}
	if !strings.Contains(code, "value := m.Serialize()") {
		t.Error("non-TTL store should serialize with plain Serialize()")
	}
}

// TestGenerateStoreTTL 验证声明 TTL 的表生成完整生命周期管理代码。
func TestGenerateStoreTTL(t *testing.T) {
	table := &TableIR{
		MessageName: "Session",
		GoPackage:   "genpkg",
		TableID:     3,
		HasTTL:      true,
		TTL:         1_000_000_000, // 1s
		PrimaryKey: &FieldIR{
			Name:      "id",
			GoName:    "Id",
			GoType:    "int64",
			ProtoType: descriptorpb.FieldDescriptorProto_TYPE_INT64,
			Number:    1,
		},
		IndexedFields: []*FieldIR{
			{Name: "user_id", GoName: "UserId", GoType: "string", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_STRING, Number: 3},
		},
		Fields: []*FieldIR{
			{Name: "id", GoType: "int64", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_INT64, Number: 1, IsPrimaryKey: true},
			{Name: "token", GoType: "string", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_STRING, Number: 2},
			{Name: "user_id", GoType: "string", ProtoType: descriptorpb.FieldDescriptorProto_TYPE_STRING, Number: 3},
		},
	}

	code := GenerateStore(table)

	// 接口含 PurgeExpired
	if !strings.Contains(code, "PurgeExpired() (int, error)") {
		t.Error("TTL store interface should declare PurgeExpired")
	}

	// Create/Update 使用 SerializeWithExpiry(now + TTL)
	if !strings.Contains(code, "value := m.SerializeWithExpiry(time.Now().UnixNano() + 1000000000)") {
		t.Error("Create should serialize with expiresAt = now + TTL")
	}
	if strings.Count(code, "SerializeWithExpiry(time.Now().UnixNano() + 1000000000)") < 2 {
		t.Error("Update should also refresh expiresAt = now + TTL")
	}

	// Get 惰性删除: DeserializeMeta + 过期判断 + deleteWithIndexes
	if !strings.Contains(code, "m, expiresAt, err := DeserializeSessionMeta(value)") {
		t.Error("TTL Get should deserialize with Meta to read expiresAt")
	}
	if !strings.Contains(code, "time.Now().UnixNano() > expiresAt") {
		t.Error("TTL Get should check expiry against now")
	}
	if !strings.Contains(code, "deleteSessionWithIndexes(s.db, s.tableID, key, m)") {
		t.Error("TTL Get should lazy-delete with index cleanup")
	}

	// deleteSessionWithIndexes 生成: 数据行 + 索引行原子删除
	if !strings.Contains(code, "func deleteSessionWithIndexes(db *sqlitex.DB, tableID uint64, dataKey []byte, m *Session) error") {
		t.Error("missing deleteWithIndexes helper")
	}
	if !strings.Contains(code, "encoding.DecodeKey(dataKey)") {
		t.Error("deleteWithIndexes should decode pk from dataKey")
	}
	if !strings.Contains(code, "encoding.EncodeIndexKey(tableID, 3, encodeSessionIndexUserIdValue(m.UserId), pkBytes)") {
		t.Error("deleteWithIndexes should remove index rows for UserId")
	}
	if !strings.Contains(code, "db.WriteBatch(ops)") {
		t.Error("deleteWithIndexes should atomically batch data+index deletes")
	}

	// PurgeExpired 实现: 遍历表前缀 + 过期判断
	if !strings.Contains(code, "func (s *sessionStore) PurgeExpired() (int, error)") {
		t.Error("missing PurgeExpired implementation")
	}
	if !strings.Contains(code, "prefix := encoding.EncodeKey(s.tableID, nil)") {
		t.Error("PurgeExpired should iterate table prefix")
	}
	if !strings.Contains(code, "count++") {
		t.Error("PurgeExpired should count purged records")
	}
}
