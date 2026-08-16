# API 参考

SQLiteX 有两层 API：**核心包**（`github.com/mogumc/sqlitex`，通用键值引擎）与**生成代码**（每个表编译期产出的强类型 Store/Query）。

---

## 一、核心包（`sqlitex`）

### 1. 打开 / 关闭

```go
func Open(cfg Config) (*DB, error)
func (db *DB) Close() error
```

```go
db, err := sqlitex.Open(sqlitex.Config{Dir: "/data/mydb"})
if err != nil { log.Fatal(err) }
defer db.Close()
```

`Dir` 必填；其余字段传零值即用 ProfileEdge 预设（见 Config 表）。

### 2. 基础读写

```go
func (db *DB) Put(key, value []byte) error      // 异步写，无 fsync
func (db *DB) PutSync(key, value []byte) error  // 同步写，立即 fsync
func (db *DB) Get(key []byte) ([]byte, error)   // 读取，未命中返回 (nil, nil)
func (db *DB) Delete(key []byte) error          // 删除
```

- `Get` 未命中返回 `nil` **而非错误**——调用方用 `value == nil` 判断不存在。
- `Put` 走 MPSC 写队列异步落盘，高吞吐；`PutSync` 直写并 fsync，强持久性。

### 3. 批量原子写

```go
type KVPair struct {
    Key    []byte
    Value  []byte
    Delete bool // true 表示删除该 Key
}
func (db *DB) WriteBatch(ops []KVPair) error
```

WriteBatch 单次提交原子执行，要么全成功要么全失败。**生成代码的 Create/Update/Delete/PurgeExpired 均基于它**，保证数据行与索引行一致。

### 4. 范围迭代

```go
func (db *DB) Iterate(prefix []byte) *PrefixIterator
```

```go
it := db.Iterate([]byte(prefix))
for it.Next() {
    key := it.Key()
    val := it.Value()
    // 处理
}
it.Close()
```

`PrefixIterator` 方法：

| 方法                      | 说明                           |
| ------------------------- | ------------------------------ |
| `Valid() bool`          | 当前是否有效                   |
| `Next() bool`           | 前进到下一项，无更多返回 false |
| `Key() []byte`          | 当前 key                       |
| `Value() []byte`        | 当前 value                     |
| `Seek(target []byte)`   | 定位到 ≥ target 的位置        |
| `SeekLT(target []byte)` | 定位到 < target 的位置         |
| `Close() error`         | 释放迭代器                     |

### 5. 缓存统计

```go
func (db *DB) CacheStats() (hits, misses, evictions, entries, curBytes, expirations int64)
```

返回 TinyLFU 热点读缓存的命中/未命中/淘汰/条目数/占用字节/过期数，用于可观测性。

### 6. 可观测性（Metrics）

```go
func (db *DB) MetricsHandler() http.Handler        // 标准 /metrics 端点，未启用返回 nil
func (db *DB) MetricsRegistry() prometheus.Gatherer // 独立 Registry，用于聚合采集
func (db *DB) MetricsText() (string, error)          // 文本格式直接输出（CLI/测试用）
```

需要 `Config.Metrics=true` 启用。详见 [observability.md](observability.md)。

### 7. 零停机热备份

```go
func (db *DB) Checkpoint(destDir string) error // 非阻塞物理快照，destDir 须为空
func (db *DB) BackupTo(destDir string) error   // Checkpoint + 目录 fsync 的运维封装
```

快照目录可直接用 `sqlitex.Open` 打开恢复。详见 [ops.md](ops.md)。

---

## 二、Config 全参

| 字段                   | 类型          | 默认  | 说明                                                  |
| ---------------------- | ------------- | ----- | ----------------------------------------------------- |
| `Dir`                | string        | -     | 数据目录路径，**必填**                          |
| `BlockCacheSize`     | int64         | 8MB   | Pebble 块缓存                                         |
| `MemTableSize`       | int64         | 4MB   | MemTable 大小（**读性能关键**，压测建议 64MB+） |
| `MaxQueueLen`        | int           | 1024  | 写队列最大缓冲，满则`ErrWriteThrottled`             |
| `MaxMemMB`           | int64         | 0     | 全局内存软上限（MB），超限拒绝写入，**0=不限制**；存长文本/大 value 建议开启（≈进程可用内存 70-80%） |
| `DisableWAL`         | bool          | false | 完全禁用 WAL（可丢数据场景）                          |
| `AsyncWAL`           | bool          | false | 异步 WAL（NoSync），崩溃可能丢最近数据                |
| `WALBytesPerSync`    | int           | 0     | 异步 WAL 后台 sync 字节间隔，推荐 1MB                 |
| `WALMinSyncInterval` | time.Duration | 0     | 异步 WAL 两次 sync 最小间隔，合并写减少 IOPS          |
| `BatchCommitSize`    | int           | 0     | 组提交批量大小，0=逐条（NoSync 下批量反而更差）       |
| `CacheMaxMB`         | int           | 10    | TinyLFU 热点读缓存上限，-1 禁用                       |
| `Metrics`            | bool          | false | 启用内建 Prometheus 指标（默认零开销）                |
| `MetricsNamespace`   | string        | sqlitex | 指标名前缀，多实例区分                               |

---

## 三、错误

通过 `errors.Is` 匹配 **哨兵错误**，禁止字符串比较：

```go
var (
    ErrDBClosed        // 数据库已关闭
    ErrInvalidKey      // key 为空
    ErrWriteThrottled  // 队列满或内存超限（背压）
    ErrQueueFull       // 内部，对外转为 ErrWriteThrottled
    ErrMemoryExceeded  // 内存超限
)
```

示例：

```go
err := db.Put(k, v)
if errors.Is(err, sqlitex.ErrWriteThrottled) {
    // 背压：稍后重试或降级
}
```

---

## 四、生成代码

以 `User` 表为例（见 [schema.md](schema.md)）。完整实现见 `User_sqlitex.go`。

### 1. Store 接口

```go
type UserStore interface {
    Create(m *User) error
    Update(m *User) error
    Delete(Id int64) error
    Get(Id int64) (*User, error)
    // TTL 表多一个方法：
    PurgeExpired() (int, error)  // 返回清理条数
}

func NewUserStore(db *sqlitex.DB) UserStore
func NewMockUserStore() *mockUserStore  // 内存 Mock，单元测试用
```

各方法语义：

| 方法             | 语义                                                                   |
| ---------------- | ---------------------------------------------------------------------- |
| `Create`       | 原子写数据行 + 全部索引行（WriteBatch）。`Get` 若已存在会覆盖        |
| `Update`       | 先`Get` 旧值删除旧索引，再写新数据 + 新索引                          |
| `Delete`       | 原子删数据行 + 索引行                                                  |
| `Get`          | 主键查询，未命中返回`(nil, nil)`；TTL 表过期自动惰性删除返回 `nil` |
| `PurgeExpired` | 遍历全表删过期记录 + 索引，返回条数（TTL 表）                          |

### 2. 序列化

```go
func (m *User) Serialize() []byte
func DeserializeUser(data []byte) (*User, error)
func (m *User) Size() int

// TTL 表额外生成：
func (m *User) SerializeWithExpiry(expiresAt int64) []byte
func DeserializeUserMeta(data []byte) (*User, int64, error)  // 返回 (msg, expiresAt, err)
```

### 3. Query 流式 API

```go
func NewUserQuery(db *sqlitex.DB) *UserQuery

q := NewUserQuery(db).
    WhereEmail(">=", "a@example.com").  // 索引字段 → 走索引扫描
    WhereActive("=", true).             // 非索引字段 → 内存过滤
    Limit(20).
    AfterKey(lastKey)                    // 游标分页

results, err := q.Exec()   // []*User
first, err  := q.First()   // *User（nil 表示无）
count, err  := q.Count()   // int
```

每个可查询字段生成一个 `WhereXxx(op string, value T) *UserQuery`：

| 字段类型      | Where 方法                    | 比较操作符                          |
| ------------- | ----------------------------- | ----------------------------------- |
| 索引 string   | `WhereEmail(op, value)`     | `=`、`>=`、`>`、`<=`、`<` |
| 索引 int64    | `WhereCreatedAt(op, value)` | 同上                                |
| 索引 bool     | `WhereActive(op, value)`    | `=`                               |
| 非索引 string | `WhereBio(op, value)`       | 同上（内存过滤）                    |

**索引字段与非索引字段的查询语义不同**：

- **索引字段**（声明了 `INDEX_*`）→ 走二级索引扫描，`AfterKey` 可用作游标分页。
- **非索引字段** → 全表扫描 + 内存过滤，`AfterKey` 忽略。

### 4. 编码层（`internal/encoding`）

```go
func EncodeKey(tableID uint64, pk []byte) []byte
func EncodeIndexKey(tableID uint64, fieldNum int32, fieldValue, pk []byte) []byte
func EncodeIndexPrefix(tableID uint64, fieldNum int32, fieldValue []byte) []byte
func DecodeKey(raw []byte) (tableID uint64, pk []byte, err error)
func DecodeIndexKey(raw []byte) (tableID uint64, fieldNum int32, fieldValue, pk []byte, err error)
```

Key 布局：

```
数据行: [TableID Uvarint][PrimaryKey]
索引行: [0xFF][TableID Uvarint][FieldNum Varint][FieldValue][PrimaryKey]
```

`0xFF` 前缀使索引行与数据行隔离，避免键冲突。

---

## 五、线程安全

- 核心包 `DB` **并发安全**：多 goroutine 可同时 Put/Get/Delete/Iterate。
- 生成代码 Store/Query **非线程安全**（无内部锁）：同一 Store 实例并发使用需外部加锁；多 goroutine 各自 `NewXxxStore`/`NewXxxQuery` 则安全。
- Mock Store 内部有锁，线程安全。
