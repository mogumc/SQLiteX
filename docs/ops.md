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

> ⚠️ **安全警示：本工具仅供本机调试使用，请勿暴露到网络。**
> 工具**没有任何认证**，且可读取整个数据库的全部数据与 Schema 结构。默认绑定 `127.0.0.1` 已做防护；若用 `-addr` 绑定到非回环地址（如 `0.0.0.0`），启动时会打印显著告警，由此产生的数据泄露风险由操作者自担。确需远程使用，请前置带认证的反向代理（如 nginx basic auth + TLS）。

```bash
go build -o sqlitex-admin ./cmd/sqlitex-admin

# 基础启动（默认 127.0.0.1:8080，只读，不会写入数据）
./sqlitex-admin -dir ./data

# 指定端口 + 关联 Schema
./sqlitex-admin -dir ./data -addr 127.0.0.1:8080 -proto ./example/demo.proto

# 超大 proto 分片导入：调大请求体上限（默认 8MB）
./sqlitex-admin -dir ./data -maxbody 67108864
```

功能：

| 页面/端点 | 说明 |
| --- | --- |
| `/` | 单页面板：存储概览卡片 + Schema 面板 + Key 浏览器 |
| `/api/schema` | **导入 .proto 并解析出真实库表结构**（表/字段/主键/索引/TTL/压缩），GET 查询 / POST 上传 / DELETE 清空 |
| `/api/stats` | 磁盘占用 / 活跃数据 / WAL / MemTable / SSTable 数（JSON） |
| `/api/keys?table_id=&decode=1&cursor=&limit=` | 表级过滤 + 游标分页（O(1) Seek）；`decode=1` 附带行级字段解码（标准表格视图数据源） |
| `/api/key?k=<base64>` | 记录详情：schema 字段级解码（含 zstd 压缩字段还原、TTL 过期判定）+ 原始十六进制转储 |
| `/schema` | `.proto` 原文（需 `-proto` 参数） |

只读打开保证面板误操作不可能污染生产数据。

### Schema 导入与结构化解码

面板右上角「导入 Schema (.proto)」上传业务 proto 文件（或启动时 `-proto` 自动导入）后：

- **表结构视图**：按 proto 解析出每张表的 TableID、主键、字段类型、二级索引（unique/normal）、压缩标记与 TTL——与 `protoc-gen-sqlitex` 代码生成走同一条 IR 提取路径，编号与语义严格一致；
- **Key 语义化**：原始字节 Key 解码为 `表名 + 主键值`（数据行）或 `表名 + 索引字段 = 值`（索引键），表选择器按表过滤扫描；
- **标准表格视图**：选中具体表后，数据以数据库表格形式展示——列 = 主键 + proto 字段（含类型标注，TTL 表附过期时间/存活状态列），行 = 字段级解码值（zstd 压缩字段自动还原），点击行查看完整详情与原始十六进制兜底；「全库（原始 key）」模式保留原始字节浏览；
- **Value 字段级解码**：扁平 Value 按字段类型逐个还原（定长标量 / 变长字符串 / zstd 压缩字段自动解压），TTL 表显示过期时间与存活状态；解码失败的字段保留原始十六进制兜底。

### 排序语义与已知约束

**表视图按主键数值序排列**：物理 Key 中 int 主键是小端编码，字节序≠数值序（如 pk=256 的字节序先于 pk=1），后端在扫描后按主键类型（有符号/无符号数值序、字符串字典序）重排，前端翻页即真实有序。

- **为什么是后端排序而不是前端排序**（决策记录）：前端只能在单页内排序，翻页切片仍是存储乱序——对超过一页的表是误导性"伪有序"。后端排序 + 游标分页是后续字段过滤、行编辑等能力（见下方路线）的必要地基，回退前端排序只会把同一工作推迟重做。
- **快照一致性（已知约束）**：排序视图每次请求独立扫描并重排，**翻页期间若正在写入，可能出现跨页重复或跳行**。只读调试（读多写少、人工浏览）场景可接受；未来表删改上线时将引入 Pebble 快照迭代器，保证整次翻页的稳定视图。
- **扫描护栏**：单表最多扫描 1 万行，超过时按主键序截断并在页脚标记「超上限截断」（恰好 1 万行不算截断）。

### Web 工具远期路线（phpMyAdmin 化）

当前面板定位为**只读调试**，远期逐步演进为类 phpMyAdmin 的管理工具：

| 阶段 | 能力 | 说明 |
| --- | --- | --- |
| P1 | 查询过滤 | 按字段条件过滤（等值/范围/LIKE），复用生成的二级索引 |
| P2 | 表删改 | 行级新增/编辑/删除（需显式开启写模式，解除只读保护 + 审计日志） |
| P3 | protobuf 生成 | 面板内编写 .proto、调用 protoc-gen-sqlitex 预览生成产物 |
| P4 | protobuf 修改 | Schema 编辑、版本对比与兼容性检查（TableID/字段编号变更检测） |
| P5 | 导入导出 | CSV/JSON 数据导入导出 |

写能力（P2+）落地时同步解决：翻页快照一致性（Pebble 快照迭代器）、操作审计、危险操作二次确认。

### 前端独立编译与嵌入

前端是独立工程（`cmd/sqlitex-admin/web`，Vite + 原生 JS，无运行时框架依赖）：

```bash
cd cmd/sqlitex-admin/web
npm install && npm run build   # 产物输出 web/dist，go:embed 嵌入二进制
```

- `web/dist` 随仓库提交：**Go 编译不依赖 Node**，仅前端改动后需重新构建。
- 二进制单文件分发：UI 全部内嵌，运行期零外部文件依赖。
- 前后端独立迭代开发：`npm run dev` 起 Vite 热更新服务（5173），
  `/api`、`/schema` 自动代理到 Go 服务（默认 8080，`SQLITEX_ADMIN_API` 可覆盖），
  详见 `web/README.md`。

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
