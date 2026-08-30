package main

import (
	"encoding/binary"
	"testing"
)

// demoSchemaText 与 example/demo.proto 同构的最小 schema（避免跨目录读文件）。
const demoSchemaText = `syntax = "proto3";
package test;
option go_package = "github.com/mogumc/sqlitex/cmd/sqlitex-admin/test";
import "sqlitex/options.proto";

message User {
  option (sqlitex.table) = {primary_key: "id"};
  int64 id = 1;
  string name = 2;
  string email = 3 [(sqlitex.field).index = INDEX_UNIQUE];
  int64 created_at = 4 [(sqlitex.field).index = INDEX_NORMAL];
  bool active = 5;
  string bio = 6 [(sqlitex.field).compress = true];
}

message Session {
  option (sqlitex.table) = {primary_key: "id"};
  int64 id = 1;
  string token = 2 [(sqlitex.field).ttl = "1h"];
  string user_id = 3 [(sqlitex.field).index = INDEX_NORMAL];
  bool active = 4;
}
`

// TestImportProtoAndTableIDs 验证解析出的表结构与 TableID 编号
// （与 protoc 编译路径同源，编号必须一致）。
func TestImportProtoAndTableIDs(t *testing.T) {
	s := newSchemaStore()
	if err := s.importProto("test.proto", demoSchemaText); err != nil {
		t.Fatalf("importProto: %v", err)
	}
	_, tables := s.snapshot()
	if len(tables) != 2 {
		t.Fatalf("want 2 tables, got %d", len(tables))
	}
	if tables[0].TableID != 1 || tables[0].Message != "User" {
		t.Errorf("table[0] = %s#%d, want User#1", tables[0].Message, tables[0].TableID)
	}
	if tables[1].TableID != 2 || !tables[1].HasTTL {
		t.Errorf("table[1] = %s#%d hasTTL=%v, want Session#2 hasTTL=true", tables[1].Message, tables[1].TableID, tables[1].HasTTL)
	}

	// 索引/压缩/TTL 选项提取
	var email, bio fieldSchema
	for _, f := range tables[0].Fields {
		switch f.Name {
		case "email":
			email = f
		case "bio":
			bio = f
		}
	}
	if email.Index != "unique" {
		t.Errorf("email index = %q, want unique", email.Index)
	}
	if !bio.Compress {
		t.Error("bio should be compressed")
	}
}

// TestDecodeKeyDataAndIndex 验证物理 Key 的语义化解码（数据行/索引键互逆）。
func TestDecodeKeyDataAndIndex(t *testing.T) {
	s := newSchemaStore()
	if err := s.importProto("test.proto", demoSchemaText); err != nil {
		t.Fatalf("importProto: %v", err)
	}

	// 数据键: [1][PK int64 LE]
	pk := make([]byte, 8)
	binary.LittleEndian.PutUint64(pk, 42)
	dataKey := append([]byte{1}, pk...)
	d := s.decodeKey(dataKey)
	if d.Kind != "data" || d.Table != "User" || d.PK != int64(42) {
		t.Errorf("data decode = %+v, want User/42", d)
	}

	// 索引键: [0xFF][1][fieldNum=3][ValueLen][email][PK]
	// ValueLen 为 email 长度的 Uvarint（与 encoding.EncodeIndexKey 布局一致）。
	email := []byte("a@b.com")
	idxKey := []byte{0xFF, 1, 3, byte(len(email))}
	idxKey = append(idxKey, email...)
	idxKey = append(idxKey, pk...)
	d = s.decodeKey(idxKey)
	if d.Kind != "index" || d.Table != "User" || d.IndexField != "email" || d.IndexValue != "a@b.com" {
		t.Errorf("index decode = %+v, want User/email/a@b.com", d)
	}

	// 未知表
	d = s.decodeKey([]byte{9, 1, 2, 3})
	if d.Kind != "unknown" {
		t.Errorf("unknown table decode = %+v, want kind=unknown", d)
	}
}

// TestDecodeValueFields 验证扁平 Value 逐字段解码（定长/变长/布尔）。
func TestDecodeValueFields(t *testing.T) {
	s := newSchemaStore()
	if err := s.importProto("test.proto", demoSchemaText); err != nil {
		t.Fatalf("importProto: %v", err)
	}
	ts := s.table(1)

	// 手工构造 User value: id(8) + [len]name + [len]email + created_at(8) + active(1) + [len][len]bio
	buf := make([]byte, 0, 64)
	var tmp [8]byte
	binary.LittleEndian.PutUint64(tmp[:], 7)
	buf = append(buf, tmp[:]...) // id
	buf = appendU32Len(buf, "alice")
	buf = appendU32Len(buf, "a@b.com")
	binary.LittleEndian.PutUint64(tmp[:], 123)
	buf = append(buf, tmp[:]...) // created_at
	buf = append(buf, 1)         // active
	bio := "hello world"
	var l [4]byte
	binary.LittleEndian.PutUint32(l[:], uint32(len(bio)))
	buf = append(buf, l[:]...)
	binary.LittleEndian.PutUint32(l[:], uint32(len(bio)))
	buf = append(buf, l[:]...)
	buf = append(buf, bio...)

	dv := s.decodeValue(ts, buf)
	if len(dv.Fields) != 6 {
		t.Fatalf("want 6 fields, got %d: %+v", len(dv.Fields), dv)
	}
	want := map[string]string{
		"id": "7", "name": "alice", "email": "a@b.com",
		"created_at": "123", "active": "true", "bio": "hello world",
	}
	for _, f := range dv.Fields {
		if want[f.Name] != f.Value {
			t.Errorf("field %s = %q, want %q", f.Name, f.Value, want[f.Name])
		}
	}
}

func appendU32Len(buf []byte, s string) []byte {
	var l [4]byte
	binary.LittleEndian.PutUint32(l[:], uint32(len(s)))
	buf = append(buf, l[:]...)
	return append(buf, s...)
}

// TestTruncateForDump 验证大 value 转储截断：小值全量、超限截断并标记。
func TestTruncateForDump(t *testing.T) {
	// 小值：全量返回
	small := []byte("hello")
	h, p, trunc := truncateForDump(small)
	if h != "68656c6c6f" || p != "hello" || trunc {
		t.Errorf("small = %q %q %v, want full dump", h, p, trunc)
	}

	// 中值（>4KB 打印上限，<256KB hex 上限）：hex 全量、print 截断
	mid := make([]byte, 8<<10)
	for i := range mid {
		mid[i] = 'a'
	}
	h, p, trunc = truncateForDump(mid)
	if len(h) != 2*len(mid) || trunc {
		t.Errorf("mid hex len = %d, trunc = %v, want full hex", len(h), trunc)
	}
	if len(p) != maxValuePrintBytes {
		t.Errorf("mid print len = %d, want %d", len(p), maxValuePrintBytes)
	}

	// 大值（>256KB）：hex 截断到上限并标记（用可打印字节，printable 不转义）
	big := make([]byte, maxValueDumpBytes+123)
	for i := range big {
		big[i] = 'a'
	}
	h, p, trunc = truncateForDump(big)
	if !trunc {
		t.Error("big value should be truncated")
	}
	if len(h) != 2*maxValueDumpBytes {
		t.Errorf("big hex len = %d, want %d", len(h), 2*maxValueDumpBytes)
	}
	if len(p) != maxValuePrintBytes {
		t.Errorf("big print len = %d, want %d", len(p), maxValuePrintBytes)
	}
}

// TestComparePKInt64Order 验证主键数值序比较：
// 小端编码下字节序≠数值序（256 的字节序先于 1），排序视图依赖本函数纠偏。
func TestComparePKInt64Order(t *testing.T) {
	ts := &tableSchema{PrimaryKey: pkSchema{Type: "int64"}}
	mk := func(id int64) []byte {
		k := append([]byte{1}, make([]byte, 8)...) // [TableID=1][PK]
		binary.LittleEndian.PutUint64(k[1:], uint64(id))
		return k
	}

	// 数值递增序列；其字节序本身是乱序的（256 < 1 字节序）
	ordered := []int64{-100, -1, 0, 1, 2, 3, 100, 255, 256, 257, 65535, 65536}
	for i := 0; i+1 < len(ordered); i++ {
		a, b := mk(ordered[i]), mk(ordered[i+1])
		if got := comparePK(ts, a, b); got >= 0 {
			t.Errorf("comparePK(%d, %d) = %d, want <0", ordered[i], ordered[i+1], got)
		}
		if got := comparePK(ts, b, a); got <= 0 {
			t.Errorf("comparePK(%d, %d) = %d, want >0", ordered[i+1], ordered[i], got)
		}
	}
	if got := comparePK(ts, mk(5), mk(5)); got != 0 {
		t.Errorf("comparePK(5,5) = %d, want 0", got)
	}
}

// TestComparePKStringOrder 字符串主键退化为字典序。
func TestComparePKStringOrder(t *testing.T) {
	ts := &tableSchema{PrimaryKey: pkSchema{Type: "string"}}
	mk := func(pk string) []byte { return append([]byte{2}, []byte(pk)...) }
	if comparePK(ts, mk("abc"), mk("abd")) >= 0 {
		t.Error("string PK should compare lexicographically")
	}
}
