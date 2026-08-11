package ttluser_test

import (
	"testing"
	"time"

	"github.com/mogumc/sqlitex"
	"github.com/mogumc/sqlitex/example/ttluser"
)

func newDB(t *testing.T) *sqlitex.DB {
	t.Helper()
	db, err := sqlitex.Open(sqlitex.Config{
		Dir: t.TempDir(), AsyncWAL: true,
	})
	if err != nil { t.Fatalf("Open: %v", err) }
	t.Cleanup(func() { db.Close() })
	return db
}

// TestTTLLazyDeletionOnGet 验证 Get 路径的惰性删除:
// 记录 1s 后过期 → Get 返回 nil 且物理删除。
func TestTTLLazyDeletionOnGet(t *testing.T) {
	db := newDB(t)
	store := ttluser.NewSessionStore(db)

	// 写入记录 (TTL=1s)
	if err := store.Create(&ttluser.Session{Id: 1, Token: "tok-1", UserId: "u1", Active: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 未过期: 应能读到
	s, err := store.Get(1)
	if err != nil { t.Fatalf("Get before expiry: %v", err) }
	if s == nil { t.Fatal("expected session before expiry") }

	// 等待 TTL 过期
	time.Sleep(1500 * time.Millisecond)

	// 过期后: Get 返回 nil（惰性删除触发）
	s, err = store.Get(1)
	if err != nil { t.Fatalf("Get after expiry: %v", err) }
	if s != nil { t.Fatal("expected nil after TTL expiry (lazy deletion)") }

	// 再次 Get 确认已物理删除（第二次走 Pebble 直接返回不存在）
	s, err = store.Get(1)
	if err != nil { t.Fatalf("Get after physical delete: %v", err) }
	if s != nil { t.Fatal("expected nil after physical delete") }
}

// TestTTLQuerySkipsExpired 验证 Query 扫描路径跳过过期记录并惰性删除。
func TestTTLQuerySkipsExpired(t *testing.T) {
	db := newDB(t)
	store := ttluser.NewSessionStore(db)

	// 写入 id=1（TTL 1s），等待其过期
	if err := store.Create(&ttluser.Session{Id: 1, Token: "old", UserId: "u1", Active: true}); err != nil {
		t.Fatalf("Create 1: %v", err)
	}
	time.Sleep(1200 * time.Millisecond) // id=1 过期

	// 再写入 id=2（新鲜，未过期）
	if err := store.Create(&ttluser.Session{Id: 2, Token: "fresh", UserId: "u2", Active: true}); err != nil {
		t.Fatalf("Create 2: %v", err)
	}

	// 查询: 应只返回 id=2（id=1 过期被惰性删除）
	q := ttluser.NewSessionQuery(db)
	after, err := q.Exec()
	if err != nil { t.Fatalf("Exec after expiry: %v", err) }
	if len(after) != 1 || after[0].Id != 2 {
		t.Fatalf("expected only id=2 after expiry, got %d entries", len(after))
	}

	// 底层已物理删除: Get(1) 返回 nil
	s, err := store.Get(1)
	if err != nil { t.Fatalf("Get(1): %v", err) }
	if s != nil { t.Fatal("expected nil for expired id=1 (physically deleted)") }
}

// TestTTLNonExpiredSurvives 验证未过期记录在 TTL 窗口内正常存活。
func TestTTLNonExpiredSurvives(t *testing.T) {
	db := newDB(t)
	store := ttluser.NewSessionStore(db)

	if err := store.Create(&ttluser.Session{Id: 1, Token: "tok", UserId: "u1"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 500ms 内（TTL 1s）应存活
	time.Sleep(400 * time.Millisecond)
	s, err := store.Get(1)
	if err != nil { t.Fatalf("Get: %v", err) }
	if s == nil { t.Fatal("expected session alive within TTL window") }
}

// TestTTLUpdateRefreshes 验证 Update 会刷新过期时间戳。
func TestTTLUpdateRefreshes(t *testing.T) {
	db := newDB(t)
	store := ttluser.NewSessionStore(db)

	if err := store.Create(&ttluser.Session{Id: 1, Token: "tok", UserId: "u1"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 更新 → 刷新 TTL（重新从 now 起算 1s）
	if err := store.Update(&ttluser.Session{Id: 1, Token: "tok-v2", UserId: "u1", Active: true}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// 900ms 后: 若 Update 未刷新 TTL，这里应该已过期（创建后 1.9s）
	// 若刷新了，仍存活
	time.Sleep(900 * time.Millisecond)
	s, err := store.Get(1)
	if err != nil { t.Fatalf("Get: %v", err) }
	if s == nil { t.Fatal("expected session alive after update refresh") }
}

// TestTTLLazyDeleteCleansIndex 验证惰性删除同步清理索引条目:
// 过期记录通过 Get 触发删除后，索引查询不应再命中。
func TestTTLLazyDeleteCleansIndex(t *testing.T) {
	db := newDB(t)
	store := ttluser.NewSessionStore(db)

	// 写入 id=1 (TTL 1s, user_id 有索引)
	if err := store.Create(&ttluser.Session{Id: 1, Token: "tok", UserId: "u1", Active: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 过期前: 索引查询命中
	q := ttluser.NewSessionQuery(db)
	before, err := q.WhereUserId("=", "u1").Exec()
	if err != nil { t.Fatalf("index query before: %v", err) }
	if len(before) != 1 { t.Fatalf("expected 1 before expiry, got %d", len(before)) }

	// 等待过期，触发 Get 惰性删除
	time.Sleep(1200 * time.Millisecond)
	if s, _ := store.Get(1); s != nil {
		t.Fatal("expected nil after expiry")
	}

	// 索引查询: 应返回 0（索引行已同步清理，而非靠 value==nil 兜底）
	q2 := ttluser.NewSessionQuery(db)
	after, err := q2.WhereUserId("=", "u1").Exec()
	if err != nil { t.Fatalf("index query after: %v", err) }
	if len(after) != 0 {
		t.Fatalf("expected 0 after expiry (index cleaned), got %d", len(after))
	}
}

// TestPurgeExpired 验证主动清理接口:
// PurgeExpired 遍历表范围，删除全部过期记录及其索引。
func TestPurgeExpired(t *testing.T) {
	db := newDB(t)
	store := ttluser.NewSessionStore(db)

	// 写入两条, 等两者都过期
	if err := store.Create(&ttluser.Session{Id: 1, Token: "a", UserId: "u1"}); err != nil {
		t.Fatalf("Create 1: %v", err)
	}
	if err := store.Create(&ttluser.Session{Id: 2, Token: "b", UserId: "u2"}); err != nil {
		t.Fatalf("Create 2: %v", err)
	}
	time.Sleep(1200 * time.Millisecond)

	// 主动清理
	n, err := store.PurgeExpired()
	if err != nil { t.Fatalf("PurgeExpired: %v", err) }
	if n != 2 { t.Fatalf("expected 2 purged, got %d", n) }

	// 全部不可见
	s, _ := store.Get(1)
	if s != nil { t.Fatal("expected nil for purged id=1") }
	s, _ = store.Get(2)
	if s != nil { t.Fatal("expected nil for purged id=2") }

	// 索引同步清理
	q := ttluser.NewSessionQuery(db)
	all, err := q.Exec()
	if err != nil { t.Fatalf("Exec: %v", err) }
	if len(all) != 0 { t.Fatalf("expected 0 after purge, got %d", len(all)) }
}

// TestPurgeExpiredSkippsFresh 验证 PurgeExpired 不清除未过期记录。
func TestPurgeExpiredSkippsFresh(t *testing.T) {
	db := newDB(t)
	store := ttluser.NewSessionStore(db)

	if err := store.Create(&ttluser.Session{Id: 1, Token: "fresh", UserId: "u1"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 未过期时 Purge 应返回 0 且记录保留
	n, err := store.PurgeExpired()
	if err != nil { t.Fatalf("PurgeExpired: %v", err) }
	if n != 0 { t.Fatalf("expected 0 purged for fresh records, got %d", n) }

	s, err := store.Get(1)
	if err != nil { t.Fatalf("Get: %v", err) }
	if s == nil { t.Fatal("expected fresh record to survive purge") }
}
