package generated_test

import (
	"fmt"
	"testing"

	"github.com/mogumc/sqlitex"
	"github.com/mogumc/sqlitex/example/generated"
)

// benchDB 创建临时数据库并预填 N 条记录。
// 每条记录: email 有唯一索引, name 无索引。
func benchDB(b *testing.B, n int) (*sqlitex.DB, *generated.UserStore) {
	b.Helper()
	db, err := sqlitex.Open(sqlitex.Config{
		Dir:         b.TempDir(),
		AsyncWAL:    true,
		CacheMaxMB:  -1, // 禁用 TinyLFU, 隔离缓存影响, 纯测存储扫描
	})
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	b.Cleanup(func() { db.Close() })

	store := generated.NewUserStore(db)
	for i := 1; i <= n; i++ {
		if err := store.Create(&generated.User{
			Id: int64(i), Name: fmt.Sprintf("user-%d", i),
			Email: fmt.Sprintf("mail-%d@test.com", i), Active: true,
		}); err != nil {
			b.Fatalf("create %d: %v", i, err)
		}
	}
	return db, &store
}

// BenchmarkIndexedQueryByEmail 走二级索引: WHERE email='mail-5000@test.com'
// 期望: 只扫描 1 条索引记录, 1 次 Get, 常量级时间 O(log N + 1)
func BenchmarkIndexedQueryByEmail(b *testing.B) {
	db, _ := benchDB(b, 10000)
	target := "mail-5000@test.com"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q := generated.NewUserQuery(db)
		users, err := q.WhereEmail("=", target).Exec()
		if err != nil {
			b.Fatalf("exec: %v", err)
		}
		if len(users) != 1 {
			b.Fatalf("expected 1, got %d", len(users))
		}
	}
}

// BenchmarkFullScanQueryByName 无索引字段: WHERE name='user-5000'
// 期望: 全表扫描 10000 条, O(N)
func BenchmarkFullScanQueryByName(b *testing.B) {
	db, _ := benchDB(b, 10000)
	target := "user-5000"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q := generated.NewUserQuery(db)
		users, err := q.WhereName("=", target).Exec()
		if err != nil {
			b.Fatalf("exec: %v", err)
		}
		if len(users) != 1 {
			b.Fatalf("expected 1, got %d", len(users))
		}
	}
}

// BenchmarkIndexedQueryLargeTable 放大数据量验证索引的扩展性
func BenchmarkIndexedQueryLargeTable(b *testing.B) {
	db, _ := benchDB(b, 100000)
	target := "mail-50000@test.com"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q := generated.NewUserQuery(db)
		users, err := q.WhereEmail("=", target).Exec()
		if err != nil {
			b.Fatalf("exec: %v", err)
		}
		if len(users) != 1 {
			b.Fatalf("expected 1, got %d", len(users))
		}
	}
}

// BenchmarkFullScanQueryLargeTable 放大数据量验证全表扫描的退化
func BenchmarkFullScanQueryLargeTable(b *testing.B) {
	db, _ := benchDB(b, 100000)
	target := "user-50000"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q := generated.NewUserQuery(db)
		users, err := q.WhereName("=", target).Exec()
		if err != nil {
			b.Fatalf("exec: %v", err)
		}
		if len(users) != 1 {
			b.Fatalf("expected 1, got %d", len(users))
		}
	}
}
