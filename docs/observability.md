# 可观测性（Observability）

SQLiteX 内建 Prometheus 指标，覆盖 QPS、延迟分布、缓存命中率、队列深度、内存与磁盘水位。默认关闭（零开销），通过 `Config.Metrics = true` 一键开启。

## 1. 启用与挂载

```go
db, err := sqlitex.Open(sqlitex.Config{
    Dir:    "./data",
    Metrics: true,
    // MetricsNamespace: "sqlitex_order", // 可选：多实例按业务命名
})
defer db.Close()

// 标准做法：挂载到业务 HTTP 服务的 /metrics
http.Handle("/metrics", db.MetricsHandler())
```

无 HTTP 环境时（CLI、批处理、测试）可直接取文本：

```go
text, err := db.MetricsText()
```

多实例聚合格式采集时，可把独立 Registry 注册进全局 collector：

```go
// prometheus.DefaultRegisterer.Register(...) 需要 Collector 适配，
// 推荐直接用 Gatherer 输出（db.MetricsRegistry()）
```

## 2. 指标清单

前缀默认为 `sqlitex`（由 `MetricsNamespace` 决定）。

| 指标 | 类型 | 标签 | 说明 |
| --- | --- | --- | --- |
| `sqlitex_ops_total` | Counter | `op`, `result` | 操作计数。op ∈ get/put/delete/put_sync/batch，result ∈ ok/error |
| `sqlitex_op_duration_seconds` | Histogram | `op` | 操作延迟分布（桶 5us~40ms） |
| `sqlitex_cache_requests_total` | Counter | `result` | TinyLFU 命中/未命中 |
| `sqlitex_cache_bytes` | Gauge | - | TinyLFU 当前内存占用 |
| `sqlitex_cache_entries` | Gauge | - | TinyLFU 条目数 |
| `sqlitex_queue_depth` | Gauge | - | MPSC 写队列瞬时深度 |
| `sqlitex_write_throttled_total` | Counter | - | 背压拒绝（队列满/内存超限）次数 |
| `sqlitex_writebatch_ops` | Histogram | - | 单次 WriteBatch 操作数分布 |
| `sqlitex_disk_usage_bytes` | Gauge | - | 数据目录磁盘占用（SST+WAL） |
| `sqlitex_go_heap_bytes` | Gauge | - | Go 进程堆内存 |

延迟类 Gauge（磁盘/内存/缓存）在抓取时实时采样，无后台轮询 Goroutine 常驻开销。

## 3. 推荐告警规则（起步）

```yaml
# P99 延迟持续偏高
- alert: SqlitexHighLatency
  expr: histogram_quantile(0.99, rate(sqlitex_op_duration_seconds_bucket[5m])) > 0.05
# 背压持续触发（写入过载）
- alert: SqlitexThrottling
  expr: rate(sqlitex_write_throttled_total[1m]) > 0
# 缓存失效（命中率 < 50% 持续 10 分钟）
- alert: SqlitexCacheCold
  expr: rate(sqlitex_cache_requests_total{result="hit"}[10m])
        / rate(sqlitex_cache_requests_total[10m]) < 0.5
```

## 4. 与 TinyLFU 原始计数的关系

`DB.CacheStats()` 返回无 Prometheus 依赖的原始六元组计数（hits/misses/evictions/entries/curBytes/expirations），适合单元测试断言；生产监控请使用上述 Counter/Gauge。
