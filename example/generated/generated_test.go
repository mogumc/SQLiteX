package generated_test

import (
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
