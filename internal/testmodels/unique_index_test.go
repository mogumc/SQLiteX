package testmodels_test

import (
	"errors"
	"testing"

	"github.com/mogumc/sqlitex"
	"github.com/mogumc/sqlitex/internal/testmodels"
)

// TestUniqueIndexEnforcedOnCreate 验证 Create 拒绝唯一索引字段值重复的写入。
func TestUniqueIndexEnforcedOnCreate(t *testing.T) {
	db := openTestDB(t)
	store := testmodels.NewUserStore(db)

	if err := store.Create(&testmodels.User{Id: 1, Email: "a@x.com"}); err != nil {
		t.Fatalf("create first: %v", err)
	}
	err := store.Create(&testmodels.User{Id: 2, Email: "a@x.com"})
	if !errors.Is(err, sqlitex.ErrDuplicateKey) {
		t.Fatalf("duplicate create err = %v, want ErrDuplicateKey", err)
	}

	// 冲突记录未落盘，首条记录完好，唯一条目仍指向首条
	if got, _ := store.Get(2); got != nil {
		t.Errorf("rejected record leaked: id=2 exists")
	}
	got, err := store.Get(1)
	if err != nil || got == nil || got.Email != "a@x.com" {
		t.Fatalf("first record corrupted: %v, %v", got, err)
	}
	rs, err := testmodels.NewUserQuery(db).WhereEmail("=", "a@x.com").Exec()
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 || rs[0].Id != 1 {
		t.Errorf("query after conflict: got %d rows, want [id=1]", len(rs))
	}
}

// TestUniqueIndexEnforcedOnUpdate 验证 Update 改为他人已占用的唯一值时被拒绝，
// 且拒绝不落盘（原值保持不变）。
func TestUniqueIndexEnforcedOnUpdate(t *testing.T) {
	db := openTestDB(t)
	store := testmodels.NewUserStore(db)

	if err := store.Create(&testmodels.User{Id: 1, Email: "a@x.com"}); err != nil {
		t.Fatalf("create u1: %v", err)
	}
	if err := store.Create(&testmodels.User{Id: 2, Email: "b@x.com"}); err != nil {
		t.Fatalf("create u2: %v", err)
	}

	u, _ := store.Get(2)
	u.Email = "a@x.com"
	err := store.Update(u)
	if !errors.Is(err, sqlitex.ErrDuplicateKey) {
		t.Fatalf("conflicting update err = %v, want ErrDuplicateKey", err)
	}
	got, _ := store.Get(2)
	if got.Email != "b@x.com" {
		t.Errorf("rejected update leaked: id=2 email=%q, want b@x.com", got.Email)
	}
}

// TestUniqueIndexUpdateSelfAndChange 验证 Update 值未变（自身持有）放行，
// 改为新值后旧值条目释放、新值生效，且新值立即对其他记录生效（不可被占用）。
func TestUniqueIndexUpdateSelfAndChange(t *testing.T) {
	db := openTestDB(t)
	store := testmodels.NewUserStore(db)

	if err := store.Create(&testmodels.User{Id: 1, Email: "a@x.com"}); err != nil {
		t.Fatalf("create u1: %v", err)
	}

	// 值未变：自身持有，放行
	u, _ := store.Get(1)
	if err := store.Update(u); err != nil {
		t.Fatalf("self update: %v", err)
	}

	// 改新值：旧条目释放、新条目生效
	u.Email = "c@x.com"
	if err := store.Update(u); err != nil {
		t.Fatalf("update to new value: %v", err)
	}
	for email, want := range map[string]int{
		"a@x.com": 0,
		"c@x.com": 1,
	} {
		rs, err := testmodels.NewUserQuery(db).WhereEmail("=", email).Exec()
		if err != nil {
			t.Fatal(err)
		}
		if len(rs) != want {
			t.Errorf("query %q: got %d rows, want %d", email, len(rs), want)
		}
	}

	// 新值立即不可被其他记录占用
	err := store.Create(&testmodels.User{Id: 2, Email: "c@x.com"})
	if !errors.Is(err, sqlitex.ErrDuplicateKey) {
		t.Errorf("create with freshly taken email err = %v, want ErrDuplicateKey", err)
	}
}

// TestUniqueIndexDeleteReleasesValue 验证 Delete 释放唯一条目后同值可重新创建。
func TestUniqueIndexDeleteReleasesValue(t *testing.T) {
	db := openTestDB(t)
	store := testmodels.NewUserStore(db)

	if err := store.Create(&testmodels.User{Id: 1, Email: "a@x.com"}); err != nil {
		t.Fatalf("create u1: %v", err)
	}
	if err := store.Delete(1); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rs, err := testmodels.NewUserQuery(db).WhereEmail("=", "a@x.com").Exec()
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 0 {
		t.Errorf("index entry leaked after delete: %d rows", len(rs))
	}
	if err := store.Create(&testmodels.User{Id: 2, Email: "a@x.com"}); err != nil {
		t.Fatalf("re-create with released email: %v", err)
	}
}

// TestUniqueIndexOverwriteCreateCleansOldEntry 验证覆盖创建（同主键 Create）
// 清理旧唯一条目：旧值不再阻塞其他记录，新值正确登记。
func TestUniqueIndexOverwriteCreateCleansOldEntry(t *testing.T) {
	db := openTestDB(t)
	store := testmodels.NewUserStore(db)

	if err := store.Create(&testmodels.User{Id: 1, Email: "old@x.com"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// 同主键覆盖创建，换邮箱
	if err := store.Create(&testmodels.User{Id: 1, Email: "new@x.com"}); err != nil {
		t.Fatalf("overwrite create: %v", err)
	}

	// 旧值必须已被释放（否则其他记录无法使用 old@x.com）
	if err := store.Create(&testmodels.User{Id: 2, Email: "old@x.com"}); err != nil {
		t.Errorf("stale unique entry blocked re-use of old value: %v", err)
	}
	// 新值已登记，不可再被占用
	err := store.Create(&testmodels.User{Id: 3, Email: "new@x.com"})
	if !errors.Is(err, sqlitex.ErrDuplicateKey) {
		t.Errorf("create with taken email err = %v, want ErrDuplicateKey", err)
	}
}
