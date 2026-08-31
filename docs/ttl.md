# TTL 与生命周期管理

## 1. 数据布局

TTL 表的 Value 头部额外携带 8 字节过期时间戳：

```
Value: [8B expiresAt UnixNano][payload ...]
       └──────── Meta Header ────────┘
```

- `expiresAt` 为 0 表示永不过期（无 TTL 字段的普通表）。
- 写入时 `expiresAt = now + TTL`。
- 判断过期：`time.Now().UnixNano() > expiresAt`。

## 2. 生命周期状态机

一条 TTL 记录经历四个阶段：

```
① 写入 → ② 过期 → ③ 惰性删除 → ④ Compaction 物理回收
```

| 阶段 | 状态 | 数据在磁盘 | SQLiteX API 读 | 绕过上层的直读 |
|------|------|-----------|---------------|---------------|
| ① 写入至过期 | 有效 | ✅ | 返回数据 ✅ | 返回数据 |
| ② 过期至惰性删除 | 逻辑过期 | ✅ | 检查 expiresAt → `nil` ✅ | **读到旧值 ⚠️** |
| ③ 惰性删除至 Compaction | 墓碑 | tombstone + 旧数据 | `nil` ✅ | Pebble 过滤 ✅ |
| ④ Compaction 后 | 已回收 | ❌ | `nil` ✅ | `nil` ✅ |

## 3. 惰性删除（Lazy Deletion）

**触发时机**：`Get` 或 `Query` 扫描读取到过期记录时。

**动作**：调用 `deleteXWithIndexes`，WriteBatch 原子删除**数据行 + 全部索引行**：

```
过期检测 (Get / Query):
  → deleteXWithIndexes(db, tableHash, dataKey, m)
       ├─ 数据行: [0x00][TableHash 8B][PK] Delete
       ├─ 索引行: [0xFF][TableHash 8B][FieldNum][ValueLen][Value][PK] Delete × N
       └─ 单次 WriteBatch 原子提交
```

**为什么同步删索引**：若只删数据行，索引行会残留占用空间。虽然查询路径已对 `value == nil` 兜底（正确性无虞），但残留索引是空间浪费，故一并清理。

**惰性删除的特点**：零主动扫描开销，但**未被读取的过期数据不会立即消失**——这是 Redis、RocksDB TTL 的通用设计。

## 4. 墓碑与 Compaction 物理回收

惰性删除产生的是 **tombstone（墓碑）**，此时旧数据仍占磁盘。Pebble 的 Compaction 会：

- 当某层 tombstone 占比超过阈值时，自动触发 Compaction；
- 在 Compaction 中把 tombstone 覆盖的旧数据物理抹除，空间真正释放。

**无需代码干预**——回收完全由 Pebble 的 tombstone 压缩启发式驱动，与上层 TTL 逻辑解耦。

## 5. PurgeExpired：主动清理接口

惰性删除只清理"被读到"的过期数据。对于写入后不再读取的过期记录，需要主动清理。Store 接口提供：

```go
// 遍历全表，删除所有过期记录 + 索引，返回清理条数
n, err := store.PurgeExpired()
```

**推荐用法**：放在后台 goroutine / cron，定时调用：

```go
go func() {
    ticker := time.NewTicker(5 * time.Minute)
    for range ticker.C {
        n, err := store.PurgeExpired()
        if err != nil { log.Printf("purge: %v", err); continue }
        if n > 0 { log.Printf("purged %d expired sessions", n) }
    }
}()
```

**收益**：

- 消除逻辑过期数据的脏读窗口（阶段②）；
- 提前产生墓碑，降低 Compaction 压力；
- 返回条数可观测。

**注意**：PurgeExpired 不删除未过期记录（有测试覆盖）。

## 6. Update 刷新 TTL

`Update` 会重新计算过期时间戳（从更新时刻起算 TTL），因此"续期"用 Update 即可：

```go
store.Update(&Session{Id: 1, Token: "new-token", ...}) // TTL 重新从 now 起算
```

## 7. 脏读风险评估

| 场景 | 风险 | 说明 |
|------|------|------|
| 走 SQLiteX 生成代码 | **无** | Get/Query 都检查 expiresAt，逻辑过期不返回 |
| 绕过上层直读 Pebble | 阶段②可能读到旧值 | 惰性机制固有窗口，需主动 PurgeExpired 缩小 |
| tombstone 阶段 | 无 | Pebble 原生过滤 |

**结论**：在 SQLiteX 语义层是干净的；唯一窗口是绕过上层直读 Pebble 的外部工具，属设计边界，无需修改代码。

## 8. 技术债务（已记录）

| # | 债务 | 状态 |
|---|------|------|
| 1 | 惰性删除同步清理索引 | ✅ 已解决（deleteXWithIndexes） |
| 2 | 阶段②绕过上层的脏读窗口 | 设计边界，文档化声明即可 |

## 9. 测试覆盖

`internal/testmodels/session_integration_test.go` 8 项集成测试：

| 测试 | 验证 |
|------|------|
| `TestTTLLazyDeletionOnGet` | Get 过期 → nil + 物理删除 |
| `TestTTLQuerySkipsExpired` | Query 跳过过期并清理 |
| `TestTTLNonExpiredSurvives` | TTL 窗口内存活 |
| `TestTTLUpdateRefreshes` | Update 刷新过期时间戳 |
| `TestTTLLazyDeleteCleansIndex` | 惰性删除同步清理索引 |
| `TestPurgeExpired` | Purge 清理过期记录 + 索引 |
| `TestPurgeExpiredSkippsFresh` | Purge 不删未过期 |
| `TestSessionMetaHeaderFormat` | 序列化首 8 字节为 expiresAt（Meta Header） |