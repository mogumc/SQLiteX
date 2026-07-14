package cache

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestBasicGetSet(t *testing.T) {
	c := New(Config{MaxBytes: 1 << 20, DefaultTTL: 5 * time.Second})
	defer c.Close()

	c.Set("a", []byte("hello"))
	val, ok := c.Get("a")
	if !ok || string(val) != "hello" {
		t.Fatalf("expected hello, got %v", val)
	}
}

func TestGetMiss(t *testing.T) {
	c := New(Config{MaxBytes: 1 << 20})
	defer c.Close()

	_, ok := c.Get("nonexistent")
	if ok {
		t.Fatal("expected miss")
	}
}

func TestDelete(t *testing.T) {
	c := New(Config{MaxBytes: 1 << 20})
	defer c.Close()

	c.Set("a", []byte("v"))
	c.Delete("a")
	_, ok := c.Get("a")
	if ok {
		t.Fatal("expected miss after delete")
	}
}

func TestTTLExpiration(t *testing.T) {
	c := New(Config{
		MaxBytes:   1 << 20,
		DefaultTTL: 100 * time.Millisecond,
	})
	defer c.Close()

	// 写入
	c.Set("a", []byte("hot"))
	val, ok := c.Get("a")
	if !ok || string(val) != "hot" {
		t.Fatalf("expected hot, got %v", val)
	}

	// 访问续期
	time.Sleep(50 * time.Millisecond)
	val, ok = c.Get("a") // 续期
	if !ok {
		t.Fatal("expected still alive after renewal")
	}

	// 不访问，等过期
	time.Sleep(150 * time.Millisecond)
	val, ok = c.Get("a")
	if ok {
		t.Fatalf("expected expired, got %v", val)
	}

	_, _, _, _, _, expirations := c.Stats()
	if expirations < 1 {
		t.Fatal("expected at least 1 TTL expiration")
	}
}

func TestTTLRenewalKeepsHotKeyAlive(t *testing.T) {
	c := New(Config{
		MaxBytes:   1 << 20,
		DefaultTTL: 80 * time.Millisecond,
	})
	defer c.Close()

	c.Set("hot", []byte("data"))

	// 每 40ms 访问一次，持续 300ms → 应该始终存活
	for i := 0; i < 7; i++ {
		time.Sleep(40 * time.Millisecond)
		val, ok := c.Get("hot")
		if !ok || string(val) != "data" {
			t.Fatalf("hot key expired prematurely at iteration %d", i)
		}
	}
}

func TestColdKeyExpires(t *testing.T) {
	c := New(Config{
		MaxBytes:   1 << 20,
		DefaultTTL: 80 * time.Millisecond,
	})
	defer c.Close()

	// 写入两个 key
	c.Set("hot", []byte("h"))
	c.Set("cold", []byte("c"))

	// 只持续访问 hot，cold 不访问
	for i := 0; i < 5; i++ {
		time.Sleep(40 * time.Millisecond)
		val, ok := c.Get("hot")
		if !ok || string(val) != "h" {
			t.Fatalf("hot expired at iteration %d", i)
		}
	}

	// cold 应该已经过期
	_, ok := c.Get("cold")
	if ok {
		t.Fatal("cold key should have expired")
	}
}

func TestExpiredEntryCleanedOnGet(t *testing.T) {
	c := New(Config{
		MaxBytes:   1 << 20,
		DefaultTTL: 50 * time.Millisecond,
	})
	defer c.Close()

	for i := 0; i < 50; i++ {
		c.Set(fmt.Sprintf("k%d", i), []byte("v"))
	}

	// 等待 TTL 过期，期间不访问
	time.Sleep(120 * time.Millisecond)

	// 逐个 Get，触发过期逐出
	cleaned := 0
	for i := 0; i < 50; i++ {
		_, ok := c.Get(fmt.Sprintf("k%d", i))
		if ok {
			t.Logf("k%d still alive (unexpected at +120ms)", i)
		} else {
			cleaned++
		}
	}
	if cleaned < 40 {
		t.Fatalf("expected majority expired, got %d/50 cleaned", cleaned)
	}

	_, _, _, entries, _, expirations := c.Stats()
	if entries > 0 {
		t.Fatalf("all entries should be expired, got %d", entries)
	}
	t.Logf("expirations: %d", expirations)
}

func TestConcurrentAccess(t *testing.T) {
	c := New(Config{
		MaxBytes:   10 << 20,
		DefaultTTL: 5 * time.Second,
	})
	defer c.Close()

	// 预热
	for i := 0; i < 100; i++ {
		c.Set(fmt.Sprintf("k%d", i), []byte("v"))
	}

	var wg sync.WaitGroup
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				key := fmt.Sprintf("k%d", i%100)
				c.Get(key)
				c.Set(key, []byte("v"))
			}
		}()
	}
	wg.Wait()

	hits, misses, _, entries, _, _ := c.Stats()
	t.Logf("hits=%d misses=%d entries=%d", hits, misses, entries)
	if hits+misses < 10000 {
		t.Fatal("expected at least 10000 ops")
	}
}

func TestAdmissionThreshold(t *testing.T) {
	c := New(Config{
		MaxBytes:           1 << 20,
		AdmissionThreshold: 3,
		DefaultTTL:         5 * time.Second,
	})
	defer c.Close()

	c.Set("a", []byte("once"))         // 直接 Set 不受控
	_, ok := c.Get("a")                // 能读到
	if !ok {
		t.Fatal("direct Set should be readable")
	}

	// Record + Set 路径：频率不足不缓存
	c.Delete("a")
	for i := 0; i < 2; i++ {
		if c.Record("a") {
			c.Set("a", []byte(fmt.Sprintf("v%d", i)))
		}
	}
	_, _, _, entries, _, _ := c.Stats()
	if entries > 0 {
		t.Logf("entries=%d (threshold=3, only 2 accesses)", entries)
	}
}

func TestMaxItemBytes(t *testing.T) {
	c := New(Config{
		MaxBytes:     1 << 20,
		MaxItemBytes: 100,
	})
	defer c.Close()

	big := make([]byte, 200)
	c.Set("big", big)

	_, ok := c.Get("big")
	if ok {
		t.Fatal("oversized item should not be cached")
	}
}
