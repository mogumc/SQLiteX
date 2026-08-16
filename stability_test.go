// 稳定性与混沌测试（Phase 3）。
//
// 覆盖三类故障场景，验证 WAL 崩溃恢复与数据一致性：
//  1. 干净重启：正常 Close → Open，全部数据可见；
//  2. 混合负载重启：交错的 Put/Delete/PutSync 后重启，终态与内存模型一致；
//  3. 进程级崩溃（真混沌）：子进程写入后未 Close 直接被 Kill，
//     父进程重新打开目录，验证 PutSync 数据经 WAL 回放完整恢复。
//
// 崩溃子进程通过 re-exec 本测试二进制实现（go test 惯用法），
// 运行方式：SQLITEX_CRASH_CHILD=1 时当前进程退化为子进程工作模式。
package sqlitex

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

// TestCleanRestart 干净关闭后重开，数据完整。
func TestCleanRestart(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Config{Dir: dir})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	want := map[string]string{}
	for i := 0; i < 200; i++ {
		k, v := fmt.Sprintf("rk-%04d", i), fmt.Sprintf("value-%04d", i)
		if err := db.PutSync([]byte(k), []byte(v)); err != nil {
			t.Fatalf("putsync: %v", err)
		}
		want[k] = v
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db2, err := Open(Config{Dir: dir})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	verifyAll(t, db2, want)
}

// TestRestartAfterMixedWorkload 混合读写删负载后重启，终态与预期一致。
// 内存模型：last-write-wins，删除即移除。
func TestRestartAfterMixedWorkload(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Config{Dir: dir})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	const n = 300
	want := map[string]string{}
	key := func(i int) string { return fmt.Sprintf("mk-%04d", i%n) }

	// 写入
	for i := 0; i < n; i++ {
		k, v := key(i), fmt.Sprintf("v1-%04d", i)
		if err := db.Put([]byte(k), []byte(v)); err != nil {
			t.Fatalf("put: %v", err)
		}
		want[k] = v
	}
	// 覆盖一半
	for i := 0; i < n/2; i++ {
		k, v := key(i), fmt.Sprintf("v2-%04d", i)
		if err := db.PutSync([]byte(k), []byte(v)); err != nil {
			t.Fatalf("putsync: %v", err)
		}
		want[k] = v
	}
	// 删除三分之一
	for i := 0; i < n/3; i++ {
		k := key(i * 2)
		if err := db.Delete([]byte(k)); err != nil {
			t.Fatalf("delete: %v", err)
		}
		delete(want, k)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	db2, err := Open(Config{Dir: dir})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	verifyAll(t, db2, want)

	// 已删除的 key 不得复活
	for i := 0; i < n/3; i++ {
		k := key(i * 2)
		if _, ok := want[k]; ok {
			continue
		}
		if v, _ := db2.Get([]byte(k)); v != nil {
			t.Errorf("deleted key %s resurrected after restart", k)
		}
	}
}

// TestConcurrentRestartConsistency 并发写 + 重启 + 全量校验，无丢失/错乱。
func TestConcurrentRestartConsistency(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Config{Dir: dir, BatchCommitSize: 32})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	const workers, perWorker = 8, 100
	want := sync.Map{}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				k := fmt.Sprintf("cw-%d-%04d", w, i)
				v := fmt.Sprintf("val-%d-%04d", w, i)
				if err := db.Put([]byte(k), []byte(v)); err != nil {
					t.Errorf("worker %d put: %v", w, err)
					return
				}
				want.Store(k, v)
			}
		}(w)
	}
	wg.Wait()

	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	db2, err := Open(Config{Dir: dir})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()

	count := 0
	want.Range(func(k, v any) bool {
		got, err := db2.Get([]byte(k.(string)))
		if err != nil {
			t.Errorf("get %v: %v", k, err)
			return false
		}
		if string(got) != v.(string) {
			t.Errorf("key %v = %q, want %q", k, got, v)
			return false
		}
		count++
		return true
	})
	if count != workers*perWorker {
		t.Errorf("verified %d keys, want %d", count, workers*perWorker)
	}
}

// TestCrashRecoverySubprocess 进程级崩溃恢复（混沌测试核心）。
//
// 流程：子进程写入 PutSync 数据 → 写 ready 标记 → 阻塞等待；
// 父进程看到标记后 Kill 子进程（无 Close、无优雅退出），
// 重新打开数据目录，WAL 回放后数据必须完整。
func TestCrashRecoverySubprocess(t *testing.T) {
	if os.Getenv("SQLITEX_CRASH_CHILD") == "1" {
		crashChildMain()
		return
	}
	if testing.Short() {
		t.Skip("skip crash-recovery subprocess test in -short mode")
	}

	dir := t.TempDir()
	ready := dir + "/READY"

	cmd := exec.Command(os.Args[0], "-test.run=TestCrashRecoverySubprocess", "-test.v")
	cmd.Env = append(os.Environ(),
		"SQLITEX_CRASH_CHILD=1",
		"SQLITEX_CRASH_DIR="+dir,
		"SQLITEX_CRASH_READY="+ready,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}

	// 等待 ready 标记（子进程已 PutSync 完全部数据）
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			t.Fatal("timeout waiting for child ready marker")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 硬杀子进程：无 Close、无优雅退出（若子进程已自行退出则继续验证恢复）
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("kill child: %v", err)
	}
	_ = cmd.Wait()

	db, err := Open(Config{Dir: dir})
	if err != nil {
		t.Fatalf("reopen after crash: %v", err)
	}
	defer db.Close()

	const n = 100
	for i := 0; i < n; i++ {
		k := fmt.Sprintf("crash-%04d", i)
		v, err := db.Get([]byte(k))
		if err != nil {
			t.Fatalf("get %s: %v", k, err)
		}
		if string(v) != fmt.Sprintf("val-%04d", i) {
			t.Errorf("crash key %s = %q, expected synced value", k, v)
		}
	}
}

// crashChildMain 子进程入口：写入 PutSync 数据、落 ready 标记、永久阻塞等待被杀。
func crashChildMain() {
	dir := os.Getenv("SQLITEX_CRASH_DIR")
	ready := os.Getenv("SQLITEX_CRASH_READY")
	db, err := Open(Config{Dir: dir})
	if err != nil {
		os.Exit(2)
	}
	for i := 0; i < 100; i++ {
		if err := db.PutSync([]byte(fmt.Sprintf("crash-%04d", i)), []byte(fmt.Sprintf("val-%04d", i))); err != nil {
			os.Exit(3)
		}
	}
	// PutSync 每次 fsync，ready 标记本身落盘即可保证先于 Kill 可见
	if err := os.WriteFile(ready, []byte("ok"), 0o644); err != nil {
		os.Exit(4)
	}
	// 挂起等待被杀。用 Sleep 而非 select{}：后者会触发 runtime 全局死锁检测
	// 直接 fatal 退出（虽然同样不执行清理，但行为不可控）。
	for {
		time.Sleep(time.Hour)
	}
}
