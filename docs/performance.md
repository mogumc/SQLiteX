# 性能基准与调优

本文档记录 SQLiteX 在 AMD Ryzen 7 8845H 上的实测数据，以及关键的调优结论。

## 1. 写入性能（Async WAL）

**同步 WAL（每写 fsync）**：~1.5K QPS —— 瓶颈是磁盘 IOPS。

**异步 WAL（NoSync）**：

| 并发 | QPS |
|------|-----|
| 8 并发 | 839K |
| 16 并发 | 972K（峰值） |
| 32 并发 | 807K |

**关键调优结论**：

1. **NoSync 下组提交是负优化**：`BatchCommitSize=64` 会掉到 701K QPS，故默认 `BatchCommitSize=0`（逐条）。
2. **高并发有 Pebble 内部互斥限制**：Direct Pebble NoSync 在 16 并发退化到 284K QPS，SQLiteX 的 MPSC 队列能维持稳定高吞吐。
3. **DirectWrite 方案已废弃**：Pebble 在 16+ 并发 WAL 轮转时 `closed LogWriter` 崩溃，不再采用。

## 2. 读写混合（80/20）

| 配置 | QPS |
|------|-----|
| 默认（4MB MemTable） | 54K |
| MemTable 64MB + BlockCache 32MB | **317K**（+487%） |
| SQLite3 对比 | 195K |

**SQLiteX 比 SQLite3 快 63%**（317K vs 195K），前提是配置合适的内存参数。

## 3. 索引进度：非主键查询加速

**实测**（300 次迭代）：

| 查询方式 | 10K 行 | 100K 行 |
|---------|--------|---------|
| 索引查询（email 唯一索引） | 2.2 μs | 127 μs |
| 全表扫描（name 无索引） | 1.74 ms | 30.4 ms |
| **加速倍数** | **801x** | **240x** |

**结论**：

- 索引查询快 2~3 个数量级，数据量越大差距越明显。
- 全表扫描 10 倍数据量退化 17.5 倍（O(N) 线性）；索引查询只从 2.2μs → 127μs。
- 索引在 100K 行仍有优化空间（127μs 而非理想 O(log N) 常量级），原因是默认 4MB MemTable 在 100K 写入后产生大量 L0 SSTable，Seek/Get 需跨多层级——**调大 MemTable 可缓解**。
- 这是 Pebble 配置问题，不是索引设计问题。

复现基准：

```bash
go test -bench . -run=^$ ./internal/testmodels/
```

## 4. 消费者优化（已实现）

| 优化 | 效果 |
|------|------|
| WriteOp sync.Pool | 消除每 op 的 channel + struct 分配 |
| 异步 MemStats 采样 | 后台 250ms 采样替换热路径 `runtime.ReadMemStats` |
| MemTable 64MB + BlockCache 32MB | 混合场景 +487% |

**纯写入 soak**：10s 从 490K → 591K（+21%，pool + 异步 memstats）。

## 5. 内存调优建议

| 负载类型 | MemTableSize | BlockCacheSize | 说明 |
|---------|-------------|----------------|------|
| 纯写入 | 32-64MB | 8-16MB | MemTable 大减少 L0 flush 频率 |
| 读写混合 | 64MB | 32MB | 实测最佳（317K QPS） |
| 索引查询 | 64MB+ | 32MB | 减少 L0 SSTable 层级 |
| 只读 | 8-16MB | 64MB+ | 缓存优先 |

**通用原则**：

- MemTable 越大，写放大越低、L0 SSTable 越少、读跨层级越少。
- 默认 4MB 偏小，**生产建议至少 64MB**。
- BlockCache 按热点数据量调整，通常 32MB 起步。

**MaxMemMB（进程堆水位背压）**：默认 `0 = 不限制`。注意 `MaxQueueLen` 只限制在途写入的**条数**（默认 1024）而非单条大小——小 KV 场景在途内存可忽略，默认关闭没有实际风险；但存储 **MB 级长文本/大二进制**（本项目核心场景）时，突发写入可在写队列中堆积最多 1024 × N MB 的在途数据，堆内存无上限增长直至 OOM。此类负载建议开启 `MaxMemMB`（约为进程可用内存的 70-80%），超限时新写入返回 `ErrWriteThrottled` 背压而非崩溃。它是 250ms 采样窗口的软限；同进程多实例共享同一进程堆，阈值需按进程总量规划。

## 6. 写路径剖析

```
Put/Delete → submit() → MPSC 队列
    → consumeLoop（单 goroutine）→ pebblePutter
    → Pebble Set/Delete → WAL Sync（默认每写 fsync）
```

**瓶颈**：`pebble.Sync`（fsync 每写）把 QPS 限制在磁盘 IOPS 量级（~1.5K）。改用 `AsyncWAL` 可突破到 900K+，代价是崩溃时可能丢最近未 sync 数据。

## 7. 二进制体积

| 编译选项 | 体积 |
|---------|------|
| 默认 | 33MB |
| `-ldflags "-s -w"` | 15MB |
| `CGO_ENABLED=0` + `-trimpath` | ~12MB |

Pebble + 依赖链主导体积。