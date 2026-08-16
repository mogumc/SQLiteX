# 运维手册（Operations）

生产环境的备份恢复、数据检视与故障排查。

## 1. 零停机热备份

```go
db, _ := sqlitex.Open(sqlitex.Config{Dir: "./data"})
defer db.Close()

// 物理一致性快照：非阻塞，读写不受影响
if err := db.BackupTo("./backup-20260816"); err != nil {
    log.Fatal(err)
}
```

语义要点：

- **非阻塞**：Checkpoint 期间读写照常，无延迟毛刺。
- **时点一致**：快照对应调用时刻的逻辑时点，包含此前全部已提交写入；之后的写入不出现在快照中。
- **可直接恢复**：备份目录是完整 Pebble 目录，`sqlitex.Open(Config{Dir: "./backup-20260816"})` 即可打开。
- **目录约束**：目标目录必须不存在或为空，防止覆盖历史备份。建议路径带时间戳。

定时全量备份示例：

```go
ticker := time.NewTicker(time.Hour)
go func() {
    for range ticker.C {
        dest := fmt.Sprintf("./backup-%s", time.Now().Format("20060102-150405"))
        if err := db.BackupTo(dest); err != nil {
            log.Printf("backup failed: %v", err)
        }
    }
}()
```

## 2. Web Admin 管控面板

`sqlitex-admin` 以**只读**模式加载数据目录，提供可视化调试：

```bash
go build -o sqlitex-admin ./cmd/sqlitex-admin

# 基础启动（默认 127.0.0.1:8080，只读，不会写入数据）
./sqlitex-admin -dir ./data

# 指定端口 + 关联 Schema
./sqlitex-admin -dir ./data -addr :8080 -proto ./example/demo.proto
```

功能：

| 页面/端点 | 说明 |
| --- | --- |
| `/` | 单页面板：存储概览卡片 + Key 浏览器 |
| `/api/stats` | 磁盘占用 / 活跃数据 / WAL / MemTable / SSTable 数（JSON） |
| `/api/keys?prefix=&cursor=&limit=` | 前缀扫描 + 游标分页（O(1) Seek，与引擎侧一致） |
| `/api/key?k=<base64>` | 单条记录十六进制转储 + 可读预览 |
| `/schema` | `.proto` 原文（需 `-proto` 参数） |

只读打开保证面板误操作不可能污染生产数据。Key 以「尽力可读」形式展示（不可打印字节转 `\xNN`）。

## 3. 崩溃恢复与数据一致性

SQLiteX 的持久性由 Pebble WAL 保证，已在测试中覆盖三类故障场景（见 `stability_test.go`）：

| 场景 | 行为 |
| --- | --- |
| 干净重启（Close → Open） | 全部数据完整 |
| 混合负载重启（写/删交错 + 并发） | 终态与内存模型一致，无丢失/复活 |
| 进程被 Kill（无 Close、无优雅退出） | `PutSync` 数据经 WAL 回放完整恢复 |

配置对崩溃恢复的影响：

- **默认（Sync WAL）**：Put/PutSync 均落盘确认，崩溃零丢失。
- **`AsyncWAL=true`（NoSync）**：崩溃可能丢最近未 sync 的写入，换取吞吐。
- **`DisableWAL=true`**：崩溃无法恢复，仅用于可重建的临时数据。

## 4. 故障排查速查

| 症状 | 排查方向 |
| --- | --- |
| `ErrWriteThrottled` 频发 | `sqlitex_queue_depth` 是否打满；增大 `MaxQueueLen` 或上游限流 |
| 读延迟抬升 | `sqlitex_cache_requests_total` 命中率；热点 key 是否超过 `CacheMaxMB` 容量 |
| 内存上涨 | `sqlitex_go_heap_bytes` 与 `MaxMemMB` 水位；大 Value 是否绕过缓存豁免 |
| 磁盘增长 | TTL 表未调用 `PurgeExpired`；Compaction 跟不上写入速度 |
