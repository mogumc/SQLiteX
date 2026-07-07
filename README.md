# SQLiteX
SQLiteX 是一个基于Go的编译时数据库，针对本地/云原生应用优化，旨在解决SQLite的性能问题同时保持轻量化。

## 为什么有这个项目?

SQLiteX 是一个专为 Go 语言打造的极速、嵌入式、静态类型的键值数据库包（Package）。它诞生于对标准 SQLite 在处理高频写入与海量大二进制/长文本日志时性能瓶颈带来的问题。  

不同于传统的数据库，SQLiteX 摒弃了复杂的 SQL 引擎和运行时的类型反射损耗。它采用了一种全新的“编译时（Compile-Time）”设计理念：以 Protobuf 作为数据的绝对定义（Schema），在编译阶段通过代码生成，同时在内部实现强类型的底层 CRUD（增删改查）接口。  

## 架构设计

### 编译时引擎 (Compile-Time Code Generation)

_核心机制： 消除运行时反射 (Reflect)，实现 Struct 到 Bytes 的直接内存映射与强类型 API 生成。_

Schema 承载： 采用 Protobuf proto3 语法作为底层数据描述语言 (IDL)，通过自定义 Options 扩展存储语义。   
AST 解析与生成： 开发自定义的 Go 插件 protoc-gen-sqlitex。通过解析 Protobuf AST（抽象语法树），提取 Message 结构、字段类型与自定义 Options（如 [(sqlitex.compress)=true]、[(sqlitex.index)=UNIQUE]）。   
零反射序列化： 生成代码彻底抛弃 encoding/json 或运行时的 proto.Marshal。直接硬编码 binary.LittleEndian 或 varint 算法进行字段偏移量计算和字节拼接，实现内存无分配或极低分配的序列化。  
强类型 API 生成： 为每张表自动生成独立的 Go Interface、链式查询构建器 (Fluent API) 以及用于单元测试的 Mock 实现，实现业务层与存储层的高度解耦。  

### 底层存储与数据布局 (Storage Engine & Data Layout)

_核心机制： 复用工业级 LSM-Tree 引擎，聚焦上层数据结构与资源优化。_

底层引擎选型： 采用 Pebble (BSD-3-Clause) 作为底层存储基座。直接利用其运行后生成的原生目录结构（包含 WAL、SSTable 等），不做强行单文件打包，以获取最佳的稳定性、空间控制与工程优雅性。  
命名空间路由 (Prefix Encoding)： 采用字典序前缀编码区分不同表（Message）的数据。Key 结构设计为 [TableID (Uvarint)] + [PrimaryKey (Bytes)]，保证局部扫描的缓存命中率与逻辑隔离。  
Value 结构设计 (Meta + Payload)： 摒弃传统的整块存储。Value 被拆分为固定长度的 Meta Header（记录字段偏移量、压缩标识、TTL 时间戳）与变长的 Payload。读取时先解析 Meta，再按需解压 Payload，避免全量解压的 CPU 浪费。  

### 内存热缓存与防穿透机制 (Hot Cache & Anti-Penetration)

_核心机制： 在有限内存下精准捕获读热点，防止恶意扫描打穿底层存储，保障高并发读性能。_

TinyLFU 热点探测： 引入极小内存（如 1MB）的 Count-Min Sketch 估算访问频率。仅允许访问频率超过动态阈值的 Key 进入热缓存，彻底免疫全表扫描或恶意请求导致的缓存污染（Cache Thrashing）。  
Zero-Alloc 对象缓存： 利用编译时生成的 `Size()` 方法精确计算对象内存占用。直接缓存反序列化后的 Struct 指针，返回只读视图，实现真正的零分配读取，避免重复解析。  
Singleflight 防击穿与空值缓存： 并发 Cache Miss 时通过 Singleflight 合并底层查询，防止瞬间请求打穿磁盘；对明确不存在的 Key 缓存短 TTL 的 Tombstone（墓碑），防止恶意探测反复穿透。  
大对象豁免与动态降级： 超过阈值（如 >1MB）的大 Value 拒绝入缓存，防止撑爆内存；实时监控 Go 进程 `runtime.MemStats`，当整体内存超限时自动清空并暂停缓存接收，防止 OOM。  

### 并发控制与写入优化 (Concurrency & Write Optimization)

_核心机制： 剥离传统 SQL 的全局锁，通过队列模型与底层 MVCC 实现高并发无锁读写与严格的资源管控。_

MPSC 队列与组提交 (Group Commit)： 实现多生产者单消费者模型。利用 Go 原生的带缓冲 channel 接收所有并发写请求，后台独占单个 Goroutine 消费。在极短时间窗口内将小批量写请求合并为一个巨大 WriteBatch，执行单次落盘，彻底转化随机 IO 为顺序 IO。  
背压限流与内存管控： 针对 MPSC 模型可能导致的 channel 膨胀问题，引入写队列长度限制与背压策略（Backpressure）。当队列满时直接拒绝写入，结合 Pebble 的 MemTable 阈值，实现严格的内存上限管控，防止海量突发写入导致 OOM。  
MVCC 读事务： 读操作直接利用 Pebble 提供的基于 Sequence ID 的多版本并发控制机制，建立快照直接读取数据，全程与写队列零竞争，读性能不受写入压力影响。  

### 细粒度压缩与生命周期 (Fine-Grained Compression & Lifecycle)

_核心机制： 拒绝默认过度设计，按需压缩与惰性清理，极致优化 CPU 与 IO 资源。_

局部压缩机制： 放弃块级压缩。仅针对被 .proto Option 显式标记且大小超过特定阈值（如 256 Bytes）的变长字段调用 Zstd 或 LZ4 压缩。固定长度的元数据保持明文存放，业务过滤或分页查询时完全跳过解压指令。  
TTL 惰性删除 (Lazy Deletion)： 结合底层 Compaction 特性，支持在 Protobuf 中声明 TTL。读取时进行轻量级过期校验（惰性删除），底层在 Compaction 阶段自动物理丢弃过期数据，实现零 CPU 负担的垃圾回收。  
可选透明加密 (TDE)： 秉持避免过度设计的原则，加密作为可选项且默认关闭。若确需开启引擎级加密，可在落盘前进行统一的流式 AES-GCM 处理，对上层透明。  

### 索引机制与游标分页 (Indexing & Cursor Pagination)

_核心机制： 编译时自动维护索引，强制 O(1) 游标寻址，消灭深分页性能灾难。_

自动化二级索引： 在 Protobuf 中引入索引 Option（如 [(sqlitex.index) = UNIQUE] 或普通索引）。编译时自动生成维护二级索引（IndexKey -> PrimaryKey）的写入逻辑与强类型查询 API，支持等值与前缀范围查询。  
游标分页算法 (Cursor Pagination)： API 强制采用游标机制，彻底抛弃传统 OFFSET。底层寻址键拼接为 [TableID] + [LastKey]，调用 Pebble 的 Seek 将迭代器瞬间移动到上一页物理边界并向后迭代，单次分页延迟始终恒定为 O(1)。  

### 开发者体验与可观测性 (Developer Experience & Observability)

_核心机制： 提供云原生友好的开发体验，内建生产级监控与运维工具。_

内建可观测性： 在生成的 CRUD 方法和写队列中原生提供 Prometheus Metrics 埋点，实时监控吞吐、延迟、热缓存命中率与队列深度。  
零停机热备份： 支持调用无阻塞的 Checkpoint/Snapshot API，利用 Pebble 底层的不可变快照特性，实现生产环境下的零停机热备份与数据导出。  
配套独立 CLI 工具： 快速生成标准化 Proto 表结构文件，简化 Schema 编写；CLI 子命令启动轻量 Web 面板服务，加载 Proto 与数据库目录，实现可视化数据调试与管理。  

## 开发路线图 (Roadmap)

_核心策略： 遵循“底层复用、上层创新、小步快跑”的原则。优先打通编译时引擎（核心灵魂），随后逐步补齐高级存储特性与生产级加固，避免在底层存储引擎上陷入过度设计的泥潭。_

### Phase 0: 底层对接与 MVP 验证 (Foundation & MVP)
_目标： 跑通 Pebble 底层封装，验证“轻量”与“高吞吐”的基准指标，建立最小可用的 KV 读写链路。_

*   **Pebble 极简封装**：集成 Pebble 引擎，放弃单文件打包，直接使用其原生目录结构。配置 `ProfileEdge`（边缘模式），严格限制 BlockCache（如 8MB）和 MemTable（如 4MB），验证低内存 footprint。
*   **基础路由与编码**：实现 `[TableID (Uvarint)] + [PrimaryKey (Bytes)]` 的 Key 编码逻辑，以及基础的 Value 字节流透传。
*   **MPSC 写队列 V1**：实现多生产者单消费者模型，带缓冲 Channel 接收写请求，后台单 Goroutine 消费并调用 Pebble 的 `Batch` 提交。
*   **背压限流机制**：实现写队列长度硬限制与基于 `runtime.MemStats` 的全局内存水位监控，队列满或内存超限时触发背压（返回 `ErrWriteThrottled`）。
*   **里程碑交付**：`sqlitex` 基础包发布，支持通过原生 API 进行高并发、低内存占用的基础 Put/Get/Delete 操作。

### Phase 1: 编译时引擎与零反射 DX (Compile-Time Engine)
_目标： 打造项目的核心灵魂——`protoc-gen-sqlitex`，实现从 Proto 定义到强类型、零反射 Go 代码的完整闭环。_

*   **AST 解析与 Option 提取**：开发 protoc 插件，解析 Protobuf AST，提取 Message 结构、字段类型，以及自定义 Options（如 `compress`, `index`, `ttl`）。
*   **零反射序列化生成**：生成硬编码 `binary.LittleEndian` 和 `varint` 的序列化/反序列化代码，实现 Struct 到 Bytes 的直接内存映射，并自动生成精确的 `Size()` 方法。
*   **强类型 CRUD 与 Fluent API**：为每个 Message 生成独立的 Store Interface、链式查询构建器（支持 Where/Limit/OrderBy）以及用于单元测试的 Mock 实现。
*   **Value 结构重构**：在生成代码中实现 `Meta Header`（偏移量/压缩标识）与 `Payload` 的分离逻辑，为后续局部压缩打下基础。
*   **里程碑交付**：`protoc-gen-sqlitex` 插件发布。业务侧只需编写 `.proto` 文件，即可生成具备完整 CRUD、链式查询和 Mock 能力的强类型 Go 代码。

### Phase 2: 核心存储特性与读性能护城河 (Advanced Features)
_目标： 实现 SQLiteX 区别于普通 KV 库的高级特性，重点解决“读热点打穿”和“大字段 CPU 浪费”两大生产痛点。_

*   **TinyLFU 热缓存层**：
    *   引入 Count-Min Sketch 进行热点探测，免疫全表扫描导致的缓存污染。
    *   利用生成的 `Size()` 方法实现精确到字节的缓存内存管控。
    *   集成 `singleflight` 防止 Cache Miss 时的并发击穿，实现空值（Tombstone）短 TTL 缓存。
*   **细粒度局部压缩**：实现基于字段大小阈值（如 >256 Bytes）和 `.proto` Option 标记的 Zstd/LZ4 动态压缩/解压调度，元数据保持明文。
*   **自动化二级索引**：在编译时生成二级索引（IndexKey -> PrimaryKey）的维护逻辑。在 MPSC 写队列中，将主数据与索引数据聚合为单次 Pebble 事务提交，保证原子性。
*   **O(1) 游标分页**：在 Fluent API 中强制实现基于 `[TableID] + [LastKey]` 的 Seek 游标分页，彻底废弃 OFFSET。
*   **里程碑交付**：引擎具备抗读热点打穿能力、低 CPU 损耗的大字段查询能力，以及高效的深分页性能。

### Phase 3: 生产级加固、生命周期与生态 (Production Hardening)
_目标： 补齐企业级/生产环境所需的可观测性、数据生命周期管理与运维工具，达到 Production-Ready 状态。_

*   **TTL 与生命周期管理**：结合 Pebble 原生的 `ExpiresAt` 与上层的 Meta Header 时间戳，实现读取时的惰性删除（Lazy Deletion）与 Compaction 阶段的物理清理联动。
*   **内建可观测性**：在生成的 CRUD 方法、热缓存层和 MPSC 队列中注入 Prometheus Metrics（涵盖 QPS、延迟 P99、缓存命中率、内存/磁盘使用率、队列深度）。
*   **零停机热备份**：封装 Pebble 的 `Checkpoint` API，提供无阻塞的快照导出功能，支持将数据目录安全拷贝至备份路径。
*   **独立 Web Admin 工具**：开发轻量级 CLI 工具，读取 `.proto` 文件和 Pebble 数据目录，启动内嵌的 HTTP Server 提供可视化数据浏览、Schema 查看与基础调试面板。
*   **稳定性与混沌测试**：引入故障注入测试（如随机 Kill 进程、磁盘满模拟、IO 延迟），验证 WAL 崩溃恢复与数据一致性。
*   **里程碑交付**：SQLiteX v1.0.0 正式版发布，具备完整的生产级运维能力与可观测性，文档与示例库完善。

### 📌 长期演进方向 (Future Explorations)

*   **跨表只读视图 (Read-Only Views)**：利用 Pebble 的 MVCC 快照，支持在同一个 Sequence ID 下对多张表进行一致性只读查询（不保证跨表写原子性）。
*   **边缘数据同步 (Edge Sync)**：探索基于 WAL 变更流（Change Stream）的轻量级主从同步协议，适配云边协同场景。
*   **多语言 SDK 扩展**：通过 FFI 或 gRPC 网关，将 SQLiteX 的能力暴露给 Rust/C++ 或前端 Node.js 环境。

## _目前项目仍在开发中！_

## License
MIT License
