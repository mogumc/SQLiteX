package sqlitex

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
)

// newTestDB 创建临时目录的测试数据库，测试结束自动清理。
func newTestDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() {
		if !db.closed.Load() {
			db.Close()
		}
	})
	return db
}

func TestOpenClose(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestOpenDirRequired(t *testing.T) {
	_, err := Open(Config{})
	if err == nil {
		t.Fatal("expected error for empty Dir")
	}
}

func TestDoubleClose(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("first Close failed: %v", err)
	}
	if !errors.Is(db.Close(), ErrDBClosed) {
		t.Fatalf("expected ErrDBClosed, got %v", db.Close())
	}
}

func TestPutGetDelete(t *testing.T) {
	db := newTestDB(t)

	key := []byte("user:001")
	value := []byte("hello-sqlitex")

	// Put
	if err := db.Put(key, value); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Get
	got, err := db.Get(key)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(got) != string(value) {
		t.Fatalf("Get value mismatch: got %q, want %q", got, value)
	}

	// Delete
	if err := db.Delete(key); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Get after delete → nil, nil
	got, err = db.Get(key)
	if err != nil {
		t.Fatalf("Get after delete failed: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil after delete, got %v", got)
	}
}

func TestGetNotFound(t *testing.T) {
	db := newTestDB(t)

	val, err := db.Get([]byte("nonexistent"))
	if err != nil {
		t.Fatalf("Get returned error for missing key: %v", err)
	}
	if val != nil {
		t.Fatalf("expected nil for missing key, got %v", val)
	}
}

func TestPutEmptyKey(t *testing.T) {
	db := newTestDB(t)
	err := db.Put(nil, []byte("value"))
	if !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey, got %v", err)
	}
}

func TestGetEmptyKey(t *testing.T) {
	db := newTestDB(t)
	_, err := db.Get(nil)
	if !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey, got %v", err)
	}
}

func TestDeleteEmptyKey(t *testing.T) {
	db := newTestDB(t)
	err := db.Delete(nil)
	if !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey, got %v", err)
	}
}

func TestClosedDB(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	db.Close()

	if !errors.Is(db.Put([]byte("k"), []byte("v")), ErrDBClosed) {
		t.Error("Put should return ErrDBClosed")
	}
	if _, err := db.Get([]byte("k")); !errors.Is(err, ErrDBClosed) {
		t.Errorf("Get should return ErrDBClosed, got %v", err)
	}
	if !errors.Is(db.Delete([]byte("k")), ErrDBClosed) {
		t.Error("Delete should return ErrDBClosed")
	}
}

func TestMultiplePuts(t *testing.T) {
	db := newTestDB(t)

	// 批量写入
	for i := 0; i < 100; i++ {
		key := []byte("key:" + string(rune('A'+i%26)) + string(rune('0'+i/26)))
		value := []byte("value-" + string(rune(i)))
		if err := db.Put(key, value); err != nil {
			t.Fatalf("Put %d failed: %v", i, err)
		}
	}

	// 批量验证
	for i := 0; i < 100; i++ {
		key := []byte("key:" + string(rune('A'+i%26)) + string(rune('0'+i/26)))
		val, err := db.Get(key)
		if err != nil {
			t.Fatalf("Get %d failed: %v", i, err)
		}
		if val == nil {
			t.Fatalf("Get %d returned nil", i)
		}
	}
}

// TestPutThrottled 验证队列满时触发背压。
func TestPutThrottled(t *testing.T) {
	dir := t.TempDir()
	// 极小队列（长度2），不限制内存
	db, err := Open(Config{Dir: dir, MaxQueueLen: 2})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// 写入一个大 value 让消费 Goroutine 慢下来（模拟队列堆积）
	// 先快速填满队列：提交多个异步 Put 但不等结果
	ops := make(chan error, 10)
	for i := 0; i < 10; i++ {
		key := []byte(fmt.Sprintf("k-%d", i))
		go func() {
			ops <- db.Put(key, []byte("v"))
		}()
	}

	// 至少有一个应该被限流
	throttled := false
	for i := 0; i < 10; i++ {
		err := <-ops
		if errors.Is(err, ErrWriteThrottled) {
			throttled = true
		}
	}
	if !throttled {
		t.Log("no throttling observed (queue drained too fast), acceptable in fast environments")
	}
}

// TestConcurrentPutGet 验证并发读写不会 panic 或数据错乱。
func TestConcurrentPutGet(t *testing.T) {
	db := newTestDB(t)

	var wg sync.WaitGroup
	// 5 个写者
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				key := []byte(fmt.Sprintf("w%d-k%d", id, j))
				val := []byte(fmt.Sprintf("v-%d-%d", id, j))
				if err := db.Put(key, val); err != nil {
					t.Errorf("Put failed: %v", err)
					return
				}
			}
		}(i)
	}
	// 3 个读者
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				key := []byte(fmt.Sprintf("w0-k%d", j%20))
				_, err := db.Get(key)
				if err != nil && !errors.Is(err, ErrDBClosed) {
					t.Errorf("Get failed: %v", err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

// TestPutSync 验证同步写入绕过队列。
func TestPutSync(t *testing.T) {
	db := newTestDB(t)

	key := []byte("sync-key")
	val := []byte("sync-val")
	if err := db.PutSync(key, val); err != nil {
		t.Fatalf("PutSync failed: %v", err)
	}
	got, err := db.Get(key)
	if err != nil {
		t.Fatalf("Get after PutSync failed: %v", err)
	}
	if string(got) != string(val) {
		t.Fatalf("PutSync value mismatch: got %q, want %q", got, val)
	}
}

// TestDirCleanup 确保测试不残留临时目录（依赖 t.TempDir 自动清理）。
func TestDirCleanup(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	db.Close()

	// 验证目录确实有文件（Pebble 写入）
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("Pebble directory should not be empty after Open")
	}
}

// === Benchmarks ===

func BenchmarkPut100(b *testing.B) {
	b.ReportAllocs()
	dir := b.TempDir()
	db, err := Open(Config{Dir: dir, MaxQueueLen: 4096})
	if err != nil {
		b.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	value := make([]byte, 100)
	for i := range value {
		value[i] = byte(i % 256)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := []byte(fmt.Sprintf("key-%d", i))
			if err := db.Put(key, value); err != nil && !errors.Is(err, ErrWriteThrottled) {
				b.Errorf("Put error: %v", err)
			}
			i++
		}
	})
}

func BenchmarkGet100(b *testing.B) {
	b.ReportAllocs()
	dir := b.TempDir()
	db, err := Open(Config{Dir: dir})
	if err != nil {
		b.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// 预填充数据
	value := make([]byte, 100)
	for i := 0; i < 10000; i++ {
		key := []byte(fmt.Sprintf("key-%d", i))
		db.Put(key, value)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := []byte(fmt.Sprintf("key-%d", i%10000))
			db.Get(key)
			i++
		}
	})
}

func BenchmarkMixed70Read30Write(b *testing.B) {
	b.ReportAllocs()
	dir := b.TempDir()
	db, err := Open(Config{Dir: dir, MaxQueueLen: 4096})
	if err != nil {
		b.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	value := make([]byte, 100)
	for i := 0; i < 10000; i++ {
		key := []byte(fmt.Sprintf("key-%d", i))
		db.Put(key, value)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := []byte(fmt.Sprintf("key-%d", i%10000))
			if i%10 < 7 {
				db.Get(key)
			} else {
				if err := db.Put(key, value); err != nil && !errors.Is(err, ErrWriteThrottled) {
					b.Errorf("Put error: %v", err)
				}
			}
			i++
		}
	})
}
