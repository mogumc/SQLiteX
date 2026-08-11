# Schema 编写指南

SQLiteX 使用 proto3 语法声明表结构，通过 `protoc-gen-sqlitex` 插件在编译期生成强类型 Store 与 Query 代码。所有表都基于 `.proto` 文件定义，`.proto` 即唯一的事实来源（Single Source of Truth）。

## 1. 最小示例

```proto
syntax = "proto3";

package example;

import "sqlitex/options.proto";

// 声明 User 为一张表，主键为 id
message User {
  option (sqlitex.table).primary_key = "id";

  int64  id         = 1 [(sqlitex.field).primary_key = true];
  string name       = 2 [(sqlitex.field).index = sqlitex.INDEX_NORMAL];
  string email      = 3 [(sqlitex.field).index = sqlitex.INDEX_UNIQUE];
  int64  created_at = 4;
  bool   active     = 5;
  string bio        = 6;
}
```

## 2. Message 级选项（TableOption）

| 选项 | 类型 | 必填 | 默认 | 说明 |
|------|------|------|------|------|
| `primary_key` | string | ✅ | - | 主键字段名，必须是 `int64`/`uint64`/`string` 类型 |
| `compress` | bool | - | false | 表级压缩开关，对超过阈值的变长字段自动压缩 |
| `compress_threshold` | int32 | - | 256 | 压缩阈值（字节），仅 `compress=true` 时生效 |

```proto
message Post {
  option (sqlitex.table).primary_key = "id";
  option (sqlitex.table).compress = true;          // 启用表级压缩
  option (sqlitex.table).compress_threshold = 512; // 512 字节以上才压缩

  uint64 id    = 1 [(sqlitex.field).primary_key = true];
  string title = 2;
  string body  = 3; // 超过 512B 自动压缩
}
```

## 3. 字段级选项（FieldOption）

| 选项 | 类型 | 默认 | 说明 |
|------|------|------|------|
| `index` | IndexOption | `INDEX_NONE` | 二级索引：`INDEX_NORMAL`（可重复）/ `INDEX_UNIQUE`（唯一） |
| `compress` | bool | false | 单字段压缩，**覆盖**表级设置；仅 `string`/`bytes` 有效 |
| `primary_key` | bool | false | 标记主键字段（配合 `TableOption.primary_key`） |
| `ttl` | string | "" | TTL 过期时间，如 `"30d"`、`"24h"`、`"500ms"` |

### 索引

```proto
message Order {
  option (sqlitex.table).primary_key = "id";

  int64  id        = 1 [(sqlitex.field).primary_key = true];
  string order_no  = 2 [(sqlitex.field).index = sqlitex.INDEX_UNIQUE]; // 订单号唯一
  int64  user_id   = 3 [(sqlitex.field).index = sqlitex.INDEX_NORMAL]; // 普通索引，可重复
  int64  amount    = 4;
}
```

**索引字段支持的类型**：`string`、`[]bytes`、`int64`、`int32`、`uint64`、`bool`。其他类型（如 `float`）会退化为字符串编码，不推荐。

### TTL

```proto
message Session {
  option (sqlitex.table).primary_key = "id";

  int64  id    = 1 [(sqlitex.field).primary_key = true];
  string token = 2 [(sqlitex.field).ttl = "1s"]; // 1 秒后过期
  string user_id = 3;
  bool   active  = 4;
}
```

TTL 时间字符串支持 Go `time.ParseDuration` 格式：`"30s"`、`"5m"`、`"24h"`、`"7d"`（天为特例，自动乘 24h）。

> ⚠️ TTL 是**惰性删除**：过期数据在下次被读取时物理删除。若写入后不再读取，数据会持续占用磁盘直到被 `PurgeExpired()` 主动清理或 Compaction 回收。详见 [ttl.md](ttl.md)。

## 4. 支持的主键类型

| 类型 | 编码方式 | 排序 |
|------|---------|------|
| `int64` | 变长 Uvarint | 数值序 |
| `uint64` | 变长 Uvarint | 数值序 |
| `string` | 原始字节 | 字典序 |

## 5. 字段类型映射

| proto 类型 | Go 类型 | 可索引 | 可压缩 | 可 TTL |
|-----------|---------|--------|--------|--------|
| `int64` | `int64` | ✅ | - | - |
| `uint64` | `uint64` | ✅ | - | - |
| `int32` | `int32` | ✅ | - | - |
| `string` | `string` | ✅ | ✅ | - |
| `bytes` | `[]byte` | ✅ | ✅ | - |
| `bool` | `bool` | ✅ | - | - |
| `float`/`double` | `float64` | ⚠️ 退化 | - | - |

## 6. 生成产物

将 `.proto` 放入 `example/`（或任意目录），执行：

```bash
protoc \
  --go_out=paths=source_relative:example/generated \
  --sqlitex_out=package=generated,paths=source_relative:example/generated \
  --proto_path=proto --proto_path=example \
  example/user.proto
```

每个 `message User` 生成一个 `User_sqlitex.go`，包含：

| 产物 | 说明 |
|------|------|
| `Serialize()` / `DeserializeUser()` | 手动序列化（比 protobuf 反射快） |
| `UserStore` | 强类型 CRUD 接口 + 实现 |
| `NewMockUserStore()` | 内存 Mock，用于单元测试 |
| `UserQuery` | 流式 Fluent 查询 API |
| `SerializeWithExpiry()` / `DeserializeUserMeta()` | TTL 表专用，Meta Header 时间戳 |

## 7. 命名规范

- Message 名 → 表名（`User`）
- Store 接口名 = `MessageName + "Store"`（`UserStore`）
- Query 名 = `MessageName + "Query"`（`UserQuery`）
- Mock 工厂 = `NewMock + MessageName + "Store"`（`NewMockUserStore`）
- 字段名 `snake_case` → Go 导出字段 `CamelCase`（`user_id` → `UserId`）

## 8. 常见错误

| 错误 | 原因 | 解决 |
|------|------|------|
| `primary_key not found` | TableOption 声明的字段不存在 | 检查拼写 |
| 主键类型不支持 | 主键是 `bool`/`int32` 等 | 改用 `int64`/`uint64`/`string` |
| 索引未生效 | 查询走全表扫描 | 确认字段声明了 `index` 且 Query 用了 `WhereXxx` |
| Mock 无 TTL 语义 | Mock 不模拟过期 | 测试 TTL 业务逻辑用真实 `sqlitex.DB` |