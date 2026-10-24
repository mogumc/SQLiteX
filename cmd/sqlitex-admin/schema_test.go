package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cockroachdb/pebble"
	sqlitexencoding "github.com/mogumc/sqlitex/internal/encoding"
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

// TestImportProtoAndTableIDs 验证解析出的表结构与 TableHash 稳定性
// （与 protoc 编译路径同源，哈希必须一致）。
func TestImportProtoAndTableIDs(t *testing.T) {
	s := newSchemaStore()
	if err := s.importProto("test.proto", demoSchemaText); err != nil {
		t.Fatalf("importProto: %v", err)
	}
	_, tables := s.snapshot()
	if len(tables) != 2 {
		t.Fatalf("want 2 tables, got %d", len(tables))
	}
	// TableID 现在是消息全名的 FNV-1a 64 位哈希，不再是顺序编号
	// test.User -> fnv1a64("test.User") = 10771054291938890206
	// test.Session -> fnv1a64("test.Session") = 16788397375209213395
	if tables[0].Message != "User" {
		t.Errorf("table[0].Message = %s, want User", tables[0].Message)
	}
	if tables[1].Message != "Session" || !tables[1].HasTTL {
		t.Errorf("table[1] = %s hasTTL=%v, want Session hasTTL=true", tables[1].Message, tables[1].HasTTL)
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

	// 获取 User 表的 TableHash
	_, tables := s.snapshot()
	userTable := tables[0] // User
	if userTable.Message != "User" {
		t.Fatalf("first table is not User: %s", userTable.Message)
	}
	userTableHash := userTable.TableID

	// 数据键: [0x00][TableHash 8B 定宽][PK int64 保序大端]
	pk := sqlitexencoding.EncodeOrderedInt64(42)
	dataKey := make([]byte, 1+8+8)
	dataKey[0] = 0x00 // DataPrefix
	binary.BigEndian.PutUint64(dataKey[1:9], userTableHash)
	copy(dataKey[9:], pk)
	d := s.decodeKey(dataKey)
	if d.Kind != "data" || d.Table != "User" || d.PK != int64(42) {
		t.Errorf("data decode = %+v, want User/42", d)
	}

	// 索引键: [0xFF][TableHash 8B 定宽][fieldNum=3][ValueLen][email][PK]
	// ValueLen 为 email 长度的 Uvarint（与 encoding.EncodeIndexKey 布局一致）。
	email := []byte("a@b.com")
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], userTableHash)
	idxKey := []byte{0xFF}
	idxKey = append(idxKey, tmp[:]...)
	idxKey = append(idxKey, 3) // fieldNum
	idxKey = append(idxKey, byte(len(email)))
	idxKey = append(idxKey, email...)
	idxKey = append(idxKey, pk...)
	d = s.decodeKey(idxKey)
	if d.Kind != "index" || d.Table != "User" || d.IndexField != "email" || d.IndexValue != "a@b.com" {
		t.Errorf("index decode = %+v, want User/email/a@b.com", d)
	}

	// 未知表（错误的 tag 字节）
	d = s.decodeKey([]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09})
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
	_, tables := s.snapshot()
	ts := tables[0] // User
	if ts.Message != "User" {
		t.Fatalf("first table is not User: %s", ts.Message)
	}

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

func TestDecodePKOrderedSigned(t *testing.T) {
	ts := &tableSchema{PrimaryKey: pkSchema{Type: "int64"}}
	for _, want := range []int64{-1 << 63, -257, -1, 0, 1, 256, 1<<63 - 1} {
		if got := decodePK(ts, sqlitexencoding.EncodeOrderedInt64(want)); got != want {
			t.Errorf("decodePK(%d) = %v", want, got)
		}
	}
}

func newPaginationTestServer(t *testing.T) (*adminServer, *tableSchema) {
	t.Helper()
	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s := &adminServer{db: db, schema: newSchemaStore()}
	if err := s.schema.importProto("test.proto", demoSchemaText); err != nil {
		t.Fatalf("import proto: %v", err)
	}
	_, tables := s.schema.snapshot()
	return s, tables[0]
}

func setPaginationTestRow(t *testing.T, s *adminServer, tableID uint64, id int64, value []byte) {
	t.Helper()
	key := sqlitexencoding.EncodeKey(tableID, sqlitexencoding.EncodeOrderedInt64(id))
	if err := s.db.Set(key, value, pebble.NoSync); err != nil {
		t.Fatalf("set id %d: %v", id, err)
	}
}

func deletePaginationTestRow(t *testing.T, s *adminServer, tableID uint64, id int64) {
	t.Helper()
	key := sqlitexencoding.EncodeKey(tableID, sqlitexencoding.EncodeOrderedInt64(id))
	if err := s.db.Delete(key, pebble.NoSync); err != nil {
		t.Fatalf("delete id %d: %v", id, err)
	}
}

func getPaginationTestPage(t *testing.T, s *adminServer, tableID uint64, cursor string, limit int) keysResponse {
	t.Helper()
	url := fmt.Sprintf("/api/keys?table_id=%d&limit=%d", tableID, limit)
	if cursor != "" {
		url += "&cursor=" + cursor // RawURLEncoding 无需再次转义
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	recorder := httptest.NewRecorder()
	s.handleKeys(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("handleKeys status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var resp keysResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

func assertPaginationTestIDs(t *testing.T, ts *tableSchema, resp keysResponse, want ...int64) {
	t.Helper()
	if len(resp.Entries) != len(want) {
		t.Fatalf("got %d rows, want %d", len(resp.Entries), len(want))
	}
	for i, entry := range resp.Entries {
		raw, err := base64.RawURLEncoding.DecodeString(entry.KeyB64)
		if err != nil {
			t.Fatalf("decode row key: %v", err)
		}
		if got := decodePK(ts, raw[9:]); got != want[i] {
			t.Fatalf("row %d id = %v, want %d", i, got, want[i])
		}
	}
}

func TestAdminTableCursorStableAfterMutations(t *testing.T) {
	s, ts := newPaginationTestServer(t)
	for _, id := range []int64{256, -1, 65536, 1, 257, 2} {
		setPaginationTestRow(t, s, ts.TableID, id, []byte("value"))
	}

	page1 := getPaginationTestPage(t, s, ts.TableID, "", 2)
	assertPaginationTestIDs(t, ts, page1, -1, 1)
	if page1.NextCursor == "" {
		t.Fatal("page1 missing next cursor")
	}

	deletePaginationTestRow(t, s, ts.TableID, -1) // 游标前删除
	deletePaginationTestRow(t, s, ts.TableID, 1)  // 删除游标本体
	setPaginationTestRow(t, s, ts.TableID, -2, []byte("inserted"))
	setPaginationTestRow(t, s, ts.TableID, 2, []byte("updated"))

	page2 := getPaginationTestPage(t, s, ts.TableID, page1.NextCursor, 2)
	assertPaginationTestIDs(t, ts, page2, 2, 256)
	page3 := getPaginationTestPage(t, s, ts.TableID, page2.NextCursor, 2)
	assertPaginationTestIDs(t, ts, page3, 257, 65536)
	if page3.NextCursor != "" {
		t.Fatal("last page should not have next cursor")
	}
}

func TestAdminTableCursorBeyondLegacyScanLimit(t *testing.T) {
	s, ts := newPaginationTestServer(t)
	batch := s.db.NewBatch()
	defer batch.Close()
	for id := int64(0); id <= 10000; id++ {
		key := sqlitexencoding.EncodeKey(ts.TableID, sqlitexencoding.EncodeOrderedInt64(id))
		if err := batch.Set(key, []byte("v"), pebble.NoSync); err != nil {
			t.Fatalf("batch set id %d: %v", id, err)
		}
	}
	if err := batch.Commit(pebble.NoSync); err != nil {
		t.Fatalf("commit rows: %v", err)
	}

	cursor := base64.RawURLEncoding.EncodeToString(
		sqlitexencoding.EncodeKey(ts.TableID, sqlitexencoding.EncodeOrderedInt64(9990)),
	)
	page := getPaginationTestPage(t, s, ts.TableID, cursor, 20)
	want := make([]int64, 0, 10)
	for id := int64(9991); id <= 10000; id++ {
		want = append(want, id)
	}
	assertPaginationTestIDs(t, ts, page, want...)
	if page.NextCursor != "" {
		t.Fatal("page after last row should not have next cursor")
	}
}

// BenchmarkAdminTableFirstPage 对比旧“全表扫描+排序”和新 keyset 首页读取成本。
// 小端数据仅用于让旧版本基准也能构造同样的 1 万行输入，不断言本基准中的主键值。
func BenchmarkAdminTableFirstPage(b *testing.B) {
	db, err := pebble.Open(b.TempDir(), &pebble.Options{})
	if err != nil {
		b.Fatalf("open pebble: %v", err)
	}
	b.Cleanup(func() { db.Close() })
	s := &adminServer{db: db, schema: newSchemaStore()}
	if err := s.schema.importProto("test.proto", demoSchemaText); err != nil {
		b.Fatalf("import proto: %v", err)
	}
	_, tables := s.schema.snapshot()
	tableID := tables[0].TableID

	batch := db.NewBatch()
	for id := uint64(0); id < 10000; id++ {
		key := make([]byte, 17)
		key[0] = dataKeyPrefix
		binary.BigEndian.PutUint64(key[1:9], tableID)
		binary.LittleEndian.PutUint64(key[9:], id)
		if err := batch.Set(key, []byte("v"), pebble.NoSync); err != nil {
			b.Fatalf("batch set: %v", err)
		}
	}
	if err := batch.Commit(pebble.NoSync); err != nil {
		b.Fatalf("commit rows: %v", err)
	}
	if err := batch.Close(); err != nil {
		b.Fatalf("close batch: %v", err)
	}

	url := fmt.Sprintf("/api/keys?table_id=%d&limit=50", tableID)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		recorder := httptest.NewRecorder()
		s.handleKeys(recorder, req)
		if recorder.Code != http.StatusOK {
			b.Fatalf("status = %d", recorder.Code)
		}
		var resp keysResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
			b.Fatalf("decode response: %v", err)
		}
		if len(resp.Entries) != 50 {
			b.Fatalf("got %d entries, want 50", len(resp.Entries))
		}
	}
}
