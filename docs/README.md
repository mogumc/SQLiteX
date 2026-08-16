# SQLiteX 文档

嵌入式、编译时优化的键值存储引擎，底层基于 Pebble + MPSC 写队列。

## 文档索引

| 文档 | 内容 | 适用人群 |
|------|------|---------|
| [快速上手](quickstart.md) | 5 分钟跑通：Schema → 生成 → CRUD → 查询 → TTL | 新用户 |
| [Schema 指南](schema.md) | proto options 编写（主键/索引/TTL/压缩） | 表设计者 |
| [API 参考](api.md) | 核心包 + 生成代码全部接口签名与语义 | 开发者 |
| [TTL 生命周期](ttl.md) | 惰性删除、墓碑回收、PurgeExpired 原理 | 开发者 |
| [性能调优](performance.md) | 压测数据、索引加速、内存参数建议 | 运维/调优 |
| [可观测性](observability.md) | Prometheus 指标清单、/metrics 挂载、告警规则 | 运维/SRE |
| [运维手册](ops.md) | 零停机备份、Web Admin 面板、崩溃恢复 | 运维 |

## 快速导航

- **核心包**：`sqlitex.Open` / `Put` / `Get` / `Delete` / `WriteBatch` / `Iterate` → [api.md](api.md)
- **生成代码**：`UserStore` / `UserQuery` / `PurgeExpired` → [api.md](api.md)
- **Schema 声明**：`(sqlitex.table)` / `(sqlitex.field)` → [schema.md](schema.md)
- **TTL**：惰性删除 + Compaction 回收 → [ttl.md](ttl.md)
- **性能**：索引 240-800x、Async WAL 900K QPS → [performance.md](performance.md)
- **监控**：`Config.Metrics` + `/metrics` → [observability.md](observability.md)
- **备份/面板**：`BackupTo` + `sqlitex-admin` → [ops.md](ops.md)