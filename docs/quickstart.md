# 快速上手

5 分钟跑通 SQLiteX 全流程：定义 Schema → 生成代码 → CRUD → 查询 → 索引 → TTL。

## 1. 定义 Schema

创建 `user.proto`：

```proto
syntax = "proto3";

package example;

option go_package = "github.com/mogumc/sqlitex/example/generated";

import "sqlitex/options.proto";

// User 演示 SQLiteX 全部特性：
//   - 主键: id
//   - 二级索引: email (唯一), created_at (普通)
//   - 字段压缩: bio (大文本按需 zstd 压缩)
//   - 游标分页: AfterKey + Limit 替代 OFFSET
message User {
  option (sqlitex.table).primary_key = "id";

  int64 id = 1 [(sqlitex.field).primary_key = true];
  string name = 2;
  string email = 3 [(sqlitex.field).index = INDEX_UNIQUE];
  int64 created_at = 4 [(sqlitex.field).index = INDEX_NORMAL];
  bool active = 5;
  string bio = 6 [(sqlitex.field).compress = true];
}
```

## 2. 生成代码

```bash
# 构建插件
go build -o protoc-gen-sqlitex.exe ./cmd/protoc-gen-sqlitex/

# 生成 Go 代码 + SQLiteX 代码
PATH=".:$PATH" protoc \
  --go_out=paths=source_relative:example/generated \
  --sqlitex_out=package=generated,paths=source_relative:example/generated \
  --proto_path=proto --proto_path=example \
  example/user.proto
```

产物：`example/generated/User.pb.go` + `example/generated/User_sqlitex.go`。

> 依赖：`protoc`、`protoc-gen-go`。插件需在 PATH 中。

## 3. 打开数据库

```go
package main

import (
    "log"

    "github.com/mogumc/sqlitex"
    "github.com/mogumc/sqlitex/example/generated"
)

func main() {
    db, err := sqlitex.Open(sqlitex.Config{
        Dir:           "./data",
        MemTableSize:  64 << 20, // 64MB，读性能关键
        BlockCacheSize: 32 << 20, // 32MB
    })
    if err != nil { log.Fatal(err) }
    defer db.Close()

    store := generated.NewUserStore(db)
    // ... CRUD
}
```

## 4. CRUD

```go
// 创建
err := store.Create(&generated.User{
    Id:        1,
    Name:      "Alice",
    Email:     "alice@example.com",
    CreatedAt: 1700000000,
    Active:    true,
    Bio:       strings.Repeat("hello ", 100), // >256B 自动 zstd 压缩
})

// 读取
u, err := store.Get(1)          // (*User, error)
if u == nil { /* 不存在 */ }

// 更新（会刷新索引）
u.Name = "Alice Updated"
err = store.Update(u)

// 删除（原子删数据行 + 索引行）
err = store.Delete(1)
```

## 5. 查询

```go
q := generated.NewUserQuery(db)

// 按唯一索引精确查
users, _ := q.WhereEmail("=", "alice@example.com").Exec()

// 索引范围查询
recent, _ := q.WhereCreatedAt(">=", 1700000000).WhereActive("=", true).Limit(10).Exec()

// 游标分页（替代 OFFSET）
var lastKey []byte
for {
    page, _ := q.WhereCreatedAt(">=", 1700000000).AfterKey(lastKey).Limit(20).Exec()
    if len(page) == 0 { break }
    lastKey = page[len(page)-1].Serialize() // 或从迭代器取
    // 处理 page
}

// 计数
count, _ := q.WhereActive("=", true).Count()
```

**索引字段走索引扫描，非索引字段内存过滤**——高并发查询优先给字段加索引。

## 6. TTL

```proto
message Session {
  option (sqlitex.table).primary_key = "id";

  int64 id = 1 [(sqlitex.field).primary_key = true];
  string token = 2 [(sqlitex.field).ttl = "1s"]; // 1 秒过期
  string user_id = 3;
  bool active = 4;
}
```

```go
store := ttluser.NewSessionStore(db)

// 写入，1 秒后过期
store.Create(&ttluser.Session{Id: 1, Token: "abc", UserId: "u1"})

// 过期后 Get 返回 nil（自动惰性删除数据行 + 索引行）
time.Sleep(1500 * time.Millisecond)
s, _ := store.Get(1)
if s == nil { /* 已过期 */ }

// 后台定时清理未被读取的过期数据
go func() {
    ticker := time.NewTicker(time.Minute)
    for range ticker.C {
        n, _ := store.PurgeExpired()
        if n > 0 { log.Printf("purged %d", n) }
    }
}()
```

## 7. 常见问题

| 问题 | 解决 |
|------|------|
| QPS 上不去（~1.5K） | 默认每写 fsync，改用 `AsyncWAL: true` 可到 900K+ |
| 读取慢 / 索引慢 | `MemTableSize` 提到 64MB，见 [performance.md](performance.md) |
| 查询走了全表扫描 | 字段加 `index` 声明，用 `WhereXxx` 查询 |
| 测试要 mock | `NewMockUserStore()`，内存实现，无 IO |

## 8. 跑测试

```bash
# 全量测试
go test ./...

# 性能基准
go test -bench . -run=^$ ./example/generated/
```

更多细节：

- Schema 选项：[schema.md](schema.md)
- API 签名：[api.md](api.md)
- TTL 机制：[ttl.md](ttl.md)
- 性能调优：[performance.md](performance.md)