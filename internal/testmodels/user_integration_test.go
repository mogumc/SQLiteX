package testmodels_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/mogumc/sqlitex"
	"github.com/mogumc/sqlitex/internal/testmodels"
)

// openTestDB 打开临时数据库并注册清理。
func openTestDB(t *testing.T) *sqlitex.DB {
	t.Helper()
	db, err := sqlitex.Open(sqlitex.Config{
		Dir:      t.TempDir(),
		AsyncWAL: true,
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// ===== CRUD =====

func TestUserStoreIntegration(t *testing.T) {
	db, err := sqlitex.Open(sqlitex.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store := testmodels.NewUserStore(db)

	// 1. Create
	user := &testmodels.User{
		Id:    1,
		Name:  "Alice",
		Email: "alice@example.com",
	}
	if err := store.Create(user); err != nil {
		t.Fatalf("create: %v", err)
	}

	// 2. Get
	got, err := store.Get(1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected user, got nil")
	}
	if got.Name != "Alice" || got.Email != "alice@example.com" {
		t.Fatalf("unexpected user: %+v", got)
	}

	// 3. Update
	user.Name = "Alice Updated"
	if err := store.Update(user); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err = store.Get(1)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got.Name != "Alice Updated" {
		t.Fatalf("expected updated name, got %q", got.Name)
	}

	// 4. Delete
	if err := store.Delete(1); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, err = store.Get(1)
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil after delete")
	}

	// 5. Get nonexistent
	got, err = store.Get(999)
	if err != nil {
		t.Fatalf("get nonexistent: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil for nonexistent key")
	}
}

func TestUserMockStore(t *testing.T) {
	mock := testmodels.NewMockUserStore()

	user := &testmodels.User{
		Id:    1,
		Name:  "Bob",
		Email: "bob@example.com",
	}

	// Create
	if err := mock.Create(user); err != nil {
		t.Fatalf("mock create: %v", err)
	}

	// Duplicate create
	if err := mock.Create(user); err == nil {
		t.Fatal("expected duplicate error")
	}

	// Get
	got, err := mock.Get(1)
	if err != nil {
		t.Fatalf("mock get: %v", err)
	}
	if got.Name != "Bob" {
		t.Fatalf("expected Bob, got %q", got.Name)
	}

	// Delete
	if err := mock.Delete(1); err != nil {
		t.Fatalf("mock delete: %v", err)
	}
	got, err = mock.Get(1)
	if err != nil {
		t.Fatalf("mock get after delete: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil after delete")
	}
}

func TestUserSerializerRoundTrip(t *testing.T) {
	user := &testmodels.User{
		Id:    42,
		Name:  "Charlie",
		Email: "charlie@example.com",
	}

	data := user.Serialize()
	restored, err := testmodels.DeserializeUser(data)
	if err != nil {
		t.Fatalf("deserialize: %v", err)
	}

	if restored.Id != 42 {
		t.Errorf("Id: got %d, want 42", restored.Id)
	}
	if restored.Name != "Charlie" {
		t.Errorf("Name: got %q, want Charlie", restored.Name)
	}
	if restored.Email != "charlie@example.com" {
		t.Errorf("Email: got %q", restored.Email)
	}
}

// ===== Query =====

func TestUserQueryBuilder(t *testing.T) {
	db := openTestDB(t)
	store := testmodels.NewUserStore(db)

	users := []*testmodels.User{
		{Id: 1, Name: "Alice", Email: "alice@test.com"},
		{Id: 2, Name: "Bob", Email: "bob@test.com"},
		{Id: 3, Name: "Charlie", Email: "charlie@test.com"},
	}
	for _, u := range users {
		if err := store.Create(u); err != nil {
			t.Fatalf("create user %d: %v", u.Id, err)
		}
	}

	results, err := testmodels.NewUserQuery(db).Exec()
	if err != nil {
		t.Fatalf("query exec: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 users, got %d", len(results))
	}

	// Limit
	results, err = testmodels.NewUserQuery(db).Limit(1).Exec()
	if err != nil {
		t.Fatalf("limit query: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 user, got %d", len(results))
	}

	// First
	first, err := testmodels.NewUserQuery(db).First()
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first == nil {
		t.Fatal("expected first user")
	}

	// Count
	count, err := testmodels.NewUserQuery(db).Count()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected count 3, got %d", count)
	}
}

// ===== 二级索引 =====

func TestUserIndexQueryByEmail(t *testing.T) {
	db := openTestDB(t)
	store := testmodels.NewUserStore(db)

	for i := int64(1); i <= 5; i++ {
		store.Create(&testmodels.User{
			Id: i, Name: fmt.Sprintf("U%d", i),
			Email: fmt.Sprintf("u%d@test.com", i), Active: true,
		})
	}

	users, err := testmodels.NewUserQuery(db).WhereEmail("=", "u3@test.com").Exec()
	if err != nil {
		t.Fatalf("index exec: %v", err)
	}
	if len(users) != 1 || users[0].Id != 3 {
		t.Fatalf("expected 1 user id=3, got %d", len(users))
	}
}

func TestUserIndexQueryByCreatedAt(t *testing.T) {
	db := openTestDB(t)
	store := testmodels.NewUserStore(db)

	for i := int64(1); i <= 10; i++ {
		store.Create(&testmodels.User{
			Id: i, Name: fmt.Sprintf("U%d", i),
			Email: fmt.Sprintf("u%d@t.com", i), CreatedAt: 1000,
		})
	}

	users, err := testmodels.NewUserQuery(db).WhereCreatedAt("=", 1000).Exec()
	if err != nil {
		t.Fatalf("index exec: %v", err)
	}
	if len(users) != 10 {
		t.Fatalf("expected 10 users, got %d", len(users))
	}
}

func TestUserMultiConditionQuery(t *testing.T) {
	db := openTestDB(t)
	store := testmodels.NewUserStore(db)

	for i := int64(1); i <= 6; i++ {
		store.Create(&testmodels.User{
			Id: i, Name: fmt.Sprintf("U%d", i),
			Email:     fmt.Sprintf("u%d@t.com", i),
			CreatedAt: 1000 + i, Active: i%2 == 0,
		})
	}

	users, err := testmodels.NewUserQuery(db).WhereCreatedAt("=", 1002).WhereActive("=", true).Exec()
	if err != nil {
		t.Fatalf("multi exec: %v", err)
	}
	if len(users) != 1 || users[0].Id != 2 {
		t.Fatalf("expected 1 user id=2, got %d", len(users))
	}
}

func TestUserDeleteCleansIndex(t *testing.T) {
	db := openTestDB(t)
	store := testmodels.NewUserStore(db)

	store.Create(&testmodels.User{Id: 1, Name: "A", Email: "a@t.com", Active: true})
	store.Delete(1)

	users, err := testmodels.NewUserQuery(db).WhereEmail("=", "a@t.com").Exec()
	if err != nil {
		t.Fatalf("exec after delete: %v", err)
	}
	if len(users) != 0 {
		t.Fatalf("expected 0 after delete, got %d", len(users))
	}
}

// ===== 字段压缩 =====

func TestUserCompressionRoundTrip(t *testing.T) {
	db := openTestDB(t)
	store := testmodels.NewUserStore(db)

	bigBio := strings.Repeat("压缩测试文本用于验证zstd字段级压缩效果 ", 50) // >256 bytes
	store.Create(&testmodels.User{
		Id: 1, Name: "A", Email: "a@t.com", Bio: bigBio, Active: true,
	})

	u, err := store.Get(1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if u.Bio != bigBio {
		t.Fatalf("bio mismatch: got %d bytes, want %d", len(u.Bio), len(bigBio))
	}

	data := u.Serialize()
	t.Logf("compressed payload size: %d vs raw bio: %d", len(data), len(bigBio))
}

// ===== 游标分页 =====

func TestUserCursorPagination(t *testing.T) {
	db := openTestDB(t)
	store := testmodels.NewUserStore(db)

	for i := int64(1); i <= 5; i++ {
		store.Create(&testmodels.User{
			Id: i, Name: fmt.Sprintf("U%d", i),
			Email: fmt.Sprintf("u%d@t.com", i), Active: true,
		})
	}

	page1, err := testmodels.NewUserQuery(db).Limit(2).Exec()
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page1: expected 2, got %d", len(page1))
	}

	page2, err := testmodels.NewUserQuery(db).Limit(2).AfterKey(nil).Exec()
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("page2: expected 2, got %d", len(page2))
	}
}

// ===== LIKE 模糊查询 =====

func TestUserLikeQuery(t *testing.T) {
	db := openTestDB(t)
	store := testmodels.NewUserStore(db)

	store.Create(&testmodels.User{Id: 1, Name: "Alice", Email: "a@t.com"})
	store.Create(&testmodels.User{Id: 2, Name: "Bob", Email: "b@t.com"})
	store.Create(&testmodels.User{Id: 3, Name: "Alicia", Email: "c@t.com"})

	users, err := testmodels.NewUserQuery(db).WhereName("LIKE", "Ali").Exec()
	if err != nil {
		t.Fatalf("LIKE exec: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("LIKE 'Ali': expected 2 (Alice+Alicia), got %d", len(users))
	}
	for _, u := range users {
		if !strings.Contains(u.Name, "Ali") {
			t.Fatalf("name %q does not contain 'Ali'", u.Name)
		}
	}
}

// ===== 序列化兼容性（#1 修复验证）=====

// TestUserLegacyFormatCompat 验证无 TTL 表使用旧格式（无 8B header），
// 序列化数据与 Phase 2 时代的历史格式完全一致。
func TestUserLegacyFormatCompat(t *testing.T) {
	// 旧格式：直接拼 [id(8B)][name: len(4)+data][email: len(4)+data][created_at(8B)][active(1B)][bio: compressible]
	user := &testmodels.User{
		Id:        7,
		Name:      "legacy",
		Email:     "legacy@t.com",
		CreatedAt: 12345,
		Active:    true,
		Bio:       "short-bio", // 小于阈值，不压缩
	}

	data := user.Serialize()

	// 无 8B header：首 8 字节直接是 Id（旧格式 off:=0）
	if id := int64(leUint64(data[0:8])); id != 7 {
		t.Fatalf("legacy format: first 8 bytes should be Id=7, got %d", id)
	}

	// 反序列化仍正确
	restored, err := testmodels.DeserializeUser(data)
	if err != nil {
		t.Fatalf("deserialize legacy: %v", err)
	}
	if restored.Id != 7 || restored.Name != "legacy" || restored.Email != "legacy@t.com" {
		t.Fatalf("roundtrip mismatch: %+v", restored)
	}

	// 长度 = MinSize(25) + 变长。bio 短则不压缩，总长 = 25 + 4+6 + 4+12 + (4+4+9) = 64
	if len(data) != 64 {
		t.Errorf("legacy payload size = %d, want 64 (no 8B header)", len(data))
	}
}

func leUint64(b []byte) uint64 {
	return uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 |
		uint64(b[4])<<32 | uint64(b[5])<<40 | uint64(b[6])<<48 | uint64(b[7])<<56
}
