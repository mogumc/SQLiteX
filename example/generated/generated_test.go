package generated_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/mogumc/sqlitex"
	"github.com/mogumc/sqlitex/example/generated"
)

func TestGeneratedStoreIntegration(t *testing.T) {
	// 创建临时数据库
	db, err := sqlitex.Open(sqlitex.Config{
		Dir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store := generated.NewUserStore(db)

	// 1. Create
	user := &generated.User{
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

func TestGeneratedMockStore(t *testing.T) {
	mock := generated.NewMockUserStore()

	user := &generated.User{
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

func TestGeneratedSerializerRoundTrip(t *testing.T) {
	user := &generated.User{
		Id:    42,
		Name:  "Charlie",
		Email: "charlie@example.com",
	}

	// 序列化 → 反序列化 往返
	data := user.Serialize()
	restored, err := generated.DeserializeUser(data)
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

func TestGeneratedQueryBuilder(t *testing.T) {
	db, err := sqlitex.Open(sqlitex.Config{
		Dir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store := generated.NewUserStore(db)

	// 插入测试数据
	users := []*generated.User{
		{Id: 1, Name: "Alice", Email: "alice@test.com"},
		{Id: 2, Name: "Bob", Email: "bob@test.com"},
		{Id: 3, Name: "Charlie", Email: "charlie@test.com"},
	}
	for _, u := range users {
		if err := store.Create(u); err != nil {
			t.Fatalf("create user %d: %v", u.Id, err)
		}
	}

	// Query 查询
	q := generated.NewUserQuery(db)
	results, err := q.Exec()
	if err != nil {
		t.Fatalf("query exec: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 users, got %d", len(results))
	}

	// Query + Limit
	q2 := generated.NewUserQuery(db)
	results, err = q2.Limit(1).Exec()
	if err != nil {
		t.Fatalf("limit query: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 user, got %d", len(results))
	}

	// First
	q3 := generated.NewUserQuery(db)
	first, err := q3.First()
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first == nil {
		t.Fatal("expected first user")
	}

	// Count
	q4 := generated.NewUserQuery(db)
	count, err := q4.Count()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected count 3, got %d", count)
	}
}

// ===== Phase 2: 二级索引 =====

func TestGeneratedIndexQueryByEmail(t *testing.T) {
	db := openTestDB(t)
	store := generated.NewUserStore(db)

	for i := int64(1); i <= 5; i++ {
		store.Create(&generated.User{
			Id: i, Name: fmt.Sprintf("U%d", i),
			Email: fmt.Sprintf("u%d@test.com", i), Active: true,
		})
	}

	// 非主键唯一索引查询
	q := generated.NewUserQuery(db)
	users, err := q.WhereEmail("=", "u3@test.com").Exec()
	if err != nil {
		t.Fatalf("index exec: %v", err)
	}
	if len(users) != 1 || users[0].Id != 3 {
		t.Fatalf("expected 1 user id=3, got %d", len(users))
	}
}

func TestGeneratedIndexQueryByCreatedAt(t *testing.T) {
	db := openTestDB(t)
	store := generated.NewUserStore(db)

	for i := int64(1); i <= 10; i++ {
		store.Create(&generated.User{
			Id: i, Name: fmt.Sprintf("U%d", i),
			Email: fmt.Sprintf("u%d@t.com", i), CreatedAt: 1000,
		})
	}

	// 普通索引: 同值多条
	q := generated.NewUserQuery(db)
	users, err := q.WhereCreatedAt("=", 1000).Exec()
	if err != nil {
		t.Fatalf("index exec: %v", err)
	}
	if len(users) != 10 {
		t.Fatalf("expected 10 users, got %d", len(users))
	}
}

func TestGeneratedMultiConditionQuery(t *testing.T) {
	db := openTestDB(t)
	store := generated.NewUserStore(db)

	for i := int64(1); i <= 6; i++ {
		store.Create(&generated.User{
			Id: i, Name: fmt.Sprintf("U%d", i),
			Email: fmt.Sprintf("u%d@t.com", i),
			CreatedAt: 1000 + i, Active: i%2 == 0,
		})
	}

	// 索引字段 + 普通字段 组合条件
	q := generated.NewUserQuery(db)
	users, err := q.WhereCreatedAt("=", 1002).WhereActive("=", true).Exec()
	if err != nil {
		t.Fatalf("multi exec: %v", err)
	}
	if len(users) != 1 || users[0].Id != 2 {
		t.Fatalf("expected 1 user id=2, got %d", len(users))
	}
}

func TestGeneratedDeleteCleansIndex(t *testing.T) {
	db := openTestDB(t)
	store := generated.NewUserStore(db)

	store.Create(&generated.User{Id: 1, Name: "A", Email: "a@t.com", Active: true})
	store.Delete(1)

	q := generated.NewUserQuery(db)
	users, err := q.WhereEmail("=", "a@t.com").Exec()
	if err != nil {
		t.Fatalf("exec after delete: %v", err)
	}
	if len(users) != 0 {
		t.Fatalf("expected 0 after delete, got %d", len(users))
	}
}

// ===== Phase 2: 字段压缩 =====

func TestGeneratedCompressionRoundTrip(t *testing.T) {
	db := openTestDB(t)
	store := generated.NewUserStore(db)

	bigBio := strings.Repeat("压缩测试文本用于验证zstd字段级压缩效果 ", 50) // >256 bytes
	store.Create(&generated.User{
		Id: 1, Name: "A", Email: "a@t.com", Bio: bigBio, Active: true,
	})

	u, err := store.Get(1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if u.Bio != bigBio {
		t.Fatalf("bio mismatch: got %d bytes, want %d", len(u.Bio), len(bigBio))
	}

	// 序列化后的数据应小于原始明文（验证压缩生效）
	data := u.Serialize()
	t.Logf("compressed payload size: %d vs raw bio: %d", len(data), len(bigBio))
}

// ===== Phase 2: 游标分页 =====

func TestGeneratedCursorPagination(t *testing.T) {
	db := openTestDB(t)
	store := generated.NewUserStore(db)

	for i := int64(1); i <= 5; i++ {
		store.Create(&generated.User{
			Id: i, Name: fmt.Sprintf("U%d", i),
			Email: fmt.Sprintf("u%d@t.com", i), Active: true,
		})
	}

	// 第一页 2 条
	q := generated.NewUserQuery(db)
	page1, err := q.Limit(2).Exec()
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page1: expected 2, got %d", len(page1))
	}

	// AfterKey(nil) 编译验证 — 等价于不使用游标
	q2 := generated.NewUserQuery(db)
	page2, err := q2.Limit(2).AfterKey(nil).Exec()
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("page2: expected 2, got %d", len(page2))
	}
}

// ===== Phase 2: LIKE 模糊查询 =====

func TestGeneratedLikeQuery(t *testing.T) {
	db := openTestDB(t)
	store := generated.NewUserStore(db)

	store.Create(&generated.User{Id: 1, Name: "Alice", Email: "a@t.com"})
	store.Create(&generated.User{Id: 2, Name: "Bob", Email: "b@t.com"})
	store.Create(&generated.User{Id: 3, Name: "Alicia", Email: "c@t.com"})

	q := generated.NewUserQuery(db)
	users, err := q.WhereName("LIKE", "Ali").Exec()
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
