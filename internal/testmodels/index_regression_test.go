package testmodels_test

import (
	"sort"
	"testing"

	"github.com/mogumc/sqlitex/internal/testmodels"
)

// TestUserIndexEqualityNoPrefixFalsePositive 回归测试：变长索引值的前缀歧义。
//
// 修复前索引键布局为 [FieldValue][PK]（无长度分隔）：
//   - 索引键 ("ab", pk=1) 与 ("a", pk=354) 字节前缀重叠（"a"+LE64(354) 以 "ab" 开头），
//     WhereEmail("=", "ab") 会误命中 email="a" 的记录，反之亦然；
//   - ("ab", pk=短) 与 ("a", pk=长) 甚至可能产生同一物理 Key，索引条目互相覆盖。
//
// 修复后索引键携带 ValueLen 长度前缀，等值扫描只命中 FieldValue 完全相等的条目。
// 注：UNIQUE 索引的强制语义由后续修复单独覆盖，本测试只断言等值扫描的正确性。
func TestUserIndexEqualityNoPrefixFalsePositive(t *testing.T) {
	db := openTestDB(t)
	store := testmodels.NewUserStore(db)

	users := []*testmodels.User{
		{Id: 1, Email: "ab", Name: "u1"},               // "a"+LE64(354) 旧编码下与 "ab" 前缀重叠
		{Id: 354, Email: "a", Name: "u2"},              // 354 = 0x0162，LE 编码首字节 'b'
		{Id: 3, Email: "alice@example.com", Name: "u3"},
		{Id: 4, Email: "al", Name: "u4"},
		{Id: 5, Email: "a", Name: "u5"},                // 与 Id=354 同值：等值扫描应全部返回
	}
	for _, u := range users {
		if err := store.Create(u); err != nil {
			t.Fatalf("create %d: %v", u.Id, err)
		}
	}

	cases := []struct {
		email   string
		wantIDs []int64
	}{
		{"a", []int64{5, 354}}, // wantIDs 升序（与 gotIDs 排序后对比）
		{"ab", []int64{1}},
		{"al", []int64{4}},
		{"alice@example.com", []int64{3}},
		{"b", nil},
		{"zzz-none@example.com", nil},
	}
	for _, tc := range cases {
		rs, err := testmodels.NewUserQuery(db).WhereEmail("=", tc.email).Exec()
		if err != nil {
			t.Fatalf("query email=%q: %v", tc.email, err)
		}
		var gotIDs []int64
		for _, r := range rs {
			if r.Email != tc.email {
				t.Errorf("query email=%q returned id=%d with email=%q (false positive)", tc.email, r.Id, r.Email)
			}
			gotIDs = append(gotIDs, r.Id)
		}
		sort.Slice(gotIDs, func(i, j int) bool { return gotIDs[i] < gotIDs[j] })
		if len(gotIDs) != len(tc.wantIDs) {
			t.Errorf("query email=%q: got ids %v, want %v", tc.email, gotIDs, tc.wantIDs)
			continue
		}
		for i := range gotIDs {
			if gotIDs[i] != tc.wantIDs[i] {
				t.Errorf("query email=%q: got ids %v, want %v", tc.email, gotIDs, tc.wantIDs)
				break
			}
		}
	}
}

// TestUserIndexSurvivesUpdate 验证索引条目在 Update 换值后同步迁移，
// 旧值查询不再命中、新值查询命中（配合长度前缀无假阳性）。
func TestUserIndexSurvivesUpdate(t *testing.T) {
	db := openTestDB(t)
	store := testmodels.NewUserStore(db)

	if err := store.Create(&testmodels.User{Id: 1, Email: "old@example.com"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	u, err := store.Get(1)
	if err != nil || u == nil {
		t.Fatalf("get: %v, %v", u, err)
	}
	u.Email = "new@example.com"
	if err := store.Update(u); err != nil {
		t.Fatalf("update: %v", err)
	}

	for email, want := range map[string]bool{
		"old@example.com": false,
		"new@example.com": true,
	} {
		rs, err := testmodels.NewUserQuery(db).WhereEmail("=", email).Exec()
		if err != nil {
			t.Fatalf("query email=%q: %v", email, err)
		}
		if got := len(rs) > 0; got != want {
			t.Errorf("query email=%q: found=%v, want %v", email, got, want)
		}
	}
}
