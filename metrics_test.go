package sqlitex

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// openMetricsDB 打开带自定义参数的测试库。
func openMetricsDB(t *testing.T, mut func(*Config)) *DB {
	t.Helper()
	cfg := Config{Dir: t.TempDir()}
	if mut != nil {
		mut(&cfg)
	}
	db, err := Open(cfg)
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

// TestMetricsDisabledByDefault 未启用 Metrics 时 Handler/Registry 均为 nil，
// 且常规操作不受影响（零开销路径）。
func TestMetricsDisabledByDefault(t *testing.T) {
	db := openMetricsDB(t, nil)
	if db.MetricsHandler() != nil {
		t.Fatal("MetricsHandler should be nil when Metrics=false")
	}
	if db.MetricsRegistry() != nil {
		t.Fatal("MetricsRegistry should be nil when Metrics=false")
	}
	if _, err := db.MetricsText(); err == nil {
		t.Fatal("MetricsText should error when Metrics=false")
	}
	if err := db.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("put: %v", err)
	}
}

// TestMetricsOperations 验证读写操作后指标计数与文本输出。
func TestMetricsOperations(t *testing.T) {
	db := openMetricsDB(t, func(c *Config) { c.Metrics = true })

	for i := 0; i < 2; i++ {
		if err := db.Put([]byte("mk1"), []byte("value")); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := db.Get([]byte("mk1")); err != nil {
			t.Fatalf("get: %v", err)
		}
	}
	if err := db.Delete([]byte("mk1")); err != nil {
		t.Fatalf("delete: %v", err)
	}

	text, err := db.MetricsText()
	if err != nil {
		t.Fatalf("MetricsText: %v", err)
	}
	for _, want := range []string{
		`ops_total{op="put",result="ok"} 2`,
		`ops_total{op="get",result="ok"} 3`,
		`ops_total{op="delete",result="ok"} 1`,
		`op_duration_seconds_bucket{op="get",`,
		`cache_requests_total{result="miss"}`,
		`queue_depth `,
		`disk_usage_bytes `,
		`write_throttled_total 0`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics text missing %q", want)
		}
	}
}

// TestMetricsHandler 验证 HTTP 端点可被抓取。
func TestMetricsHandler(t *testing.T) {
	db := openMetricsDB(t, func(c *Config) {
		c.Metrics = true
		c.MetricsNamespace = "sqlitex_test"
	})

	if err := db.PutSync([]byte("hk"), []byte("hv")); err != nil {
		t.Fatalf("putsync: %v", err)
	}

	h := db.MetricsHandler()
	if h == nil {
		t.Fatal("MetricsHandler is nil")
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `sqlitex_test_ops_total{op="put_sync",result="ok"} 1`) {
		t.Errorf("scrape body missing put_sync counter:\n%s", body)
	}
}

// TestMetricsThrottle 验证背压拒绝计入 throttled 计数。
// 用 Sync WAL（每写 fsync）制造慢消费者 + 并发生产者，可靠触发队列满。
func TestMetricsThrottle(t *testing.T) {
	db := openMetricsDB(t, func(c *Config) {
		c.Metrics = true
		c.MaxQueueLen = 2
	})

	big := make([]byte, 1<<20)
	var mu sync.Mutex
	throttled := false
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				if err := db.Put([]byte("tk"), big); err == ErrWriteThrottled {
					mu.Lock()
					throttled = true
					mu.Unlock()
					return
				} else if err != nil {
					t.Errorf("put: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if !throttled {
		t.Skip("throttle not triggered in this run")
	}
	text, err := db.MetricsText()
	if err != nil {
		t.Fatalf("MetricsText: %v", err)
	}
	if !strings.Contains(text, "write_throttled_total") || strings.Contains(text, "write_throttled_total 0") {
		t.Errorf("write_throttled_total not recorded:\n%s", text)
	}
}
