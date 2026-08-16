package sqlitex

import (
	"bytes"
	"fmt"
	"os"
	"testing"
)

// TestCheckpointRoundtrip 备份目录可直接用 Open 打开，且数据与源库一致。
func TestCheckpointRoundtrip(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Config{Dir: dir})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	want := map[string]string{}
	for i := 0; i < 50; i++ {
		k, v := fmt.Sprintf("key-%03d", i), fmt.Sprintf("value-%03d-payload", i)
		if err := db.PutSync([]byte(k), []byte(v)); err != nil {
			t.Fatalf("putsync: %v", err)
		}
		want[k] = v
	}

	backupDir := t.TempDir() + "/backup"
	if err := db.Checkpoint(backupDir); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 打开备份并逐 key 校验
	bdb, err := Open(Config{Dir: backupDir})
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer bdb.Close()
	verifyAll(t, bdb, want)
}

// TestCheckpointNonEmptyDest 备份目录非空时拒绝，防止覆盖已有备份。
func TestCheckpointNonEmptyDest(t *testing.T) {
	db := newTestDB(t)
	dest := t.TempDir() + "/backup"
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(dest+"/existing.txt", []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := db.Checkpoint(dest); err == nil {
		t.Fatal("checkpoint into non-empty dir should fail")
	}
}

// TestCheckpointIsPointInTime 备份是调用时点的快照：
// Checkpoint 之后的写入不出现在备份中，之前的 PutSync 数据全部存在。
func TestCheckpointIsPointInTime(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Config{Dir: dir})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	for i := 0; i < 10; i++ {
		k := fmt.Sprintf("pre-%02d", i)
		if err := db.PutSync([]byte(k), []byte("before")); err != nil {
			t.Fatalf("putsync: %v", err)
		}
	}

	backupDir := t.TempDir() + "/backup"
	if err := db.BackupTo(backupDir); err != nil {
		t.Fatalf("backup: %v", err)
	}

	// 备份之后的写入
	if err := db.PutSync([]byte("post-00"), []byte("after")); err != nil {
		t.Fatalf("putsync post: %v", err)
	}

	bdb, err := Open(Config{Dir: backupDir})
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer bdb.Close()

	if _, err := bdb.Get([]byte("post-00")); err != nil {
		t.Fatalf("get post: %v", err)
	}
	if v, _ := bdb.Get([]byte("post-00")); v != nil {
		t.Error("post-checkpoint write leaked into backup")
	}
	for i := 0; i < 10; i++ {
		k := fmt.Sprintf("pre-%02d", i)
		v, err := bdb.Get([]byte(k))
		if err != nil || v == nil {
			t.Errorf("pre key %s missing in backup (v=%v err=%v)", k, v, err)
		}
	}
}

// TestCheckpointDuringConcurrentWrites 并发写入下 Checkpoint 不阻塞、不丢已提交数据。
func TestCheckpointDuringConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Config{Dir: dir})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// 先落一批基线数据（PutSync 保证持久）
	const baseline = 100
	for i := 0; i < baseline; i++ {
		if err := db.PutSync([]byte(fmt.Sprintf("base-%03d", i)), []byte("b")); err != nil {
			t.Fatalf("putsync: %v", err)
		}
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
				db.Put([]byte(fmt.Sprintf("hot-%06d", i)), []byte("h"))
				i++
			}
		}
	}()

	backupDir := t.TempDir() + "/backup"
	if err := db.Checkpoint(backupDir); err != nil {
		t.Fatalf("checkpoint during writes: %v", err)
	}
	close(stop)
	<-done

	bdb, err := Open(Config{Dir: backupDir})
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer bdb.Close()
	for i := 0; i < baseline; i++ {
		k := fmt.Sprintf("base-%03d", i)
		if v, _ := bdb.Get([]byte(k)); v == nil {
			t.Errorf("baseline key %s missing in concurrent backup", k)
		}
	}
}

// TestCheckpointClosedDB 关闭后备份返回 ErrDBClosed。
func TestCheckpointClosedDB(t *testing.T) {
	dir := t.TempDir()
	db, _ := Open(Config{Dir: dir})
	db.Close()
	if err := db.Checkpoint(t.TempDir() + "/b"); err != ErrDBClosed {
		t.Fatalf("want ErrDBClosed, got %v", err)
	}
}

// verifyAll 逐 key 校验 db 内容与 want 完全一致。
func verifyAll(t *testing.T, db *DB, want map[string]string) {
	t.Helper()
	for k, v := range want {
		got, err := db.Get([]byte(k))
		if err != nil {
			t.Fatalf("get %s: %v", k, err)
		}
		if got == nil {
			t.Errorf("key %s missing", k)
			continue
		}
		if !bytes.Equal(got, []byte(v)) {
			t.Errorf("key %s = %q, want %q", k, got, v)
		}
	}
}
