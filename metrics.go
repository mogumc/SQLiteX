// Package sqlitex 内建可观测性（Phase 3）。
//
// 通过 Config.Metrics=true 启用后，DB 在独立 prometheus.Registry 上注册
// 全套运行指标，并通过 MetricsHandler 暴露标准 /metrics 端点，
// 可直接被 promhttp.Handler 或任意 HTTPmux 挂载。
package sqlitex

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/expfmt"
)

// metrics 持有一个 DB 实例的全套 Prometheus 指标。
// 所有字段在 Open 时一次性创建，热路径仅做原子加减，禁用 Metrics 时整个结构为 nil。
type metrics struct {
	registry *prometheus.Registry

	opsTotal   *prometheus.CounterVec   // 按 操作类型 + 结果 计数
	opDuration *prometheus.HistogramVec // 操作延迟分布
	cacheTotal *prometheus.CounterVec   // 热缓存命中/未命中
	queueDepth prometheus.GaugeFunc     // MPSC 队列瞬时深度
	diskBytes  prometheus.GaugeFunc     // 数据目录磁盘占用（SST + WAL）
	memBytes   prometheus.GaugeFunc     // Go 进程堆内存（HeapAlloc）
	cacheBytes prometheus.GaugeFunc     // TinyLFU 当前内存占用
	cacheItems prometheus.GaugeFunc     // TinyLFU 条目数
	throttled  prometheus.Counter       // 背压拒绝总数
	batchSize  prometheus.Histogram     // WriteBatch 单批操作数分布
}

// newMetrics 创建并注册全套指标。
// 延迟类 GaugeFunc 在抓取时实时采样（Pebble Metrics / runtime / TinyLFU Stats），
// 避免后台轮询 Goroutine 的常驻开销。
func newMetrics(db *DB, namespace string) *metrics {
	m := &metrics{
		registry: prometheus.NewRegistry(),
		opsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "ops_total",
			Help:      "Total operations by type and result.",
		}, []string{"op", "result"}),
		opDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "op_duration_seconds",
			Help:      "Operation latency distribution.",
			Buckets:   prometheus.ExponentialBuckets(0.000005, 2, 14), // 5us ~ 40ms
		}, []string{"op"}),
		cacheTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "cache_requests_total",
			Help:      "TinyLFU hot cache requests by result.",
		}, []string{"result"}),
		queueDepth: prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "queue_depth",
			Help:      "Current MPSC write queue depth.",
		}, func() float64 { return float64(db.queue.Len()) }),
		diskBytes: prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "disk_usage_bytes",
			Help:      "Estimated on-disk size of the store (SSTables + WAL).",
		}, func() float64 {
			if db.closed.Load() {
				return 0
			}
			pm := db.pebble.Metrics()
			return float64(pm.DiskSpaceUsage())
		}),
		memBytes: prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "go_heap_bytes",
			Help:      "Go runtime heap allocation in use.",
		}, func() float64 {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			return float64(ms.HeapAlloc)
		}),
		cacheBytes: prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "cache_bytes",
			Help:      "Current TinyLFU hot cache memory usage.",
		}, func() float64 {
			if db.hotCache == nil {
				return 0
			}
			_, _, _, _, cur, _ := db.hotCache.Stats()
			return float64(cur)
		}),
		cacheItems: prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "cache_entries",
			Help:      "Current TinyLFU entry count.",
		}, func() float64 {
			if db.hotCache == nil {
				return 0
			}
			_, _, _, entries, _, _ := db.hotCache.Stats()
			return float64(entries)
		}),
		throttled: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "write_throttled_total",
			Help:      "Total writes rejected by backpressure (queue full / memory exceeded).",
		}),
		batchSize: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "writebatch_ops",
			Help:      "Number of operations per WriteBatch call.",
			Buckets:   prometheus.ExponentialBuckets(1, 2, 12), // 1 ~ 2048
		}),
	}

	m.registry.MustRegister(m.opsTotal, m.opDuration, m.cacheTotal, m.queueDepth,
		m.diskBytes, m.memBytes, m.cacheBytes, m.cacheItems, m.throttled, m.batchSize)
	return m
}

// observe 记录一次操作的耗时与结果，热路径唯一入口。
// metrics 为 nil（未启用）时零开销。
func (m *metrics) observe(op, result string, start time.Time) {
	if m == nil {
		return
	}
	m.opsTotal.WithLabelValues(op, result).Inc()
	m.opDuration.WithLabelValues(op).Observe(time.Since(start).Seconds())
}

// observeThrottled 记录一次背压拒绝。
func (m *metrics) observeThrottled() {
	if m == nil {
		return
	}
	m.throttled.Inc()
}

// observeCache 记录一次缓存查找结果。
func (m *metrics) observeCache(hit bool) {
	if m == nil {
		return
	}
	if hit {
		m.cacheTotal.WithLabelValues("hit").Inc()
	} else {
		m.cacheTotal.WithLabelValues("miss").Inc()
	}
}

// MetricsRegistry 返回该 DB 独立的 Prometheus Registry。
// 未启用 Metrics（Config.Metrics=false）时返回 nil。
// 适用于将指标聚合到进程级全局 Registry 的场景。
func (db *DB) MetricsRegistry() prometheus.Gatherer {
	if db.metrics == nil {
		return nil
	}
	return db.metrics.registry
}

// MetricsHandler 返回标准 Prometheus 抓取端点的 HTTP Handler。
// 未启用 Metrics 时返回 nil。典型用法：
//
//	http.Handle("/metrics", db.MetricsHandler())
func (db *DB) MetricsHandler() http.Handler {
	if db.metrics == nil {
		return nil
	}
	return promhttp.HandlerFor(db.metrics.registry, promhttp.HandlerOpts{})
}

// resultOf 将错误归一化为指标标签。
func resultOf(err error) string {
	if err == nil {
		return "ok"
	}
	return "error"
}

// opTimer 是操作级计时器：Metrics 未启用时 start/finish 均为空操作，
// 热路径仅付出一次指针判空，不产生 time.Now / errors.Is 调用。
type opTimer struct {
	m     *metrics
	op    string
	start time.Time
}

// startOp 开始计时；db.metrics 为 nil 时返回零值计时器（零开销）。
func (db *DB) startOp(op string) opTimer {
	if db.metrics == nil {
		return opTimer{}
	}
	return opTimer{m: db.metrics, op: op, start: time.Now()}
}

// finish 记录操作结果与耗时；背压拒绝同时计入 throttled 计数。
func (t opTimer) finish(err error) {
	if t.m == nil {
		return
	}
	t.m.observe(t.op, resultOf(err), t.start)
	if errors.Is(err, ErrWriteThrottled) {
		t.m.observeThrottled()
	}
}

// MetricsText 以 Prometheus 文本格式输出全部指标，
// 便于无 HTTP 环境下（CLI、测试）直接采集。
func (db *DB) MetricsText() (string, error) {
	if db.metrics == nil {
		return "", fmt.Errorf("sqlitex: metrics not enabled (Config.Metrics=false)")
	}
	mfs, err := db.metrics.registry.Gather()
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	enc := expfmt.NewEncoder(&buf, expfmt.FmtText)
	for _, mf := range mfs {
		if err := enc.Encode(mf); err != nil {
			return "", err
		}
	}
	return buf.String(), nil
}
