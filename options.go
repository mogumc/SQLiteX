package sqlitex

import "time"

// Config 定义数据库打开时的所有可调参数。
// 零值代表使用 ProfileEdge 默认配置。
type Config struct {
	// Dir 数据目录路径（Pebble 原生目录结构）。必填。
	Dir string

	// BlockCacheSize Block Cache 大小，单位字节。
	// 默认 8MB（ProfileEdge）。
	BlockCacheSize int64

	// MemTableSize MemTable 大小，单位字节。
	// 默认 4MB（ProfileEdge）。
	MemTableSize int64

	// MaxQueueLen MPSC 写队列最大缓冲长度。
	// 默认 1024。队列满时 Put/Delete 返回 ErrWriteThrottled。
	MaxQueueLen int

	// MaxMemMB 全局内存软上限，单位 MB。
	// 基于 runtime.MemStats.Alloc 采样，超限时拒绝新写入。
	// 默认 0 表示不启用内存监控。
	MaxMemMB int64

	// DisableWAL 完全禁用 WAL，崩溃时无法恢复数据。
	// 仅适用于可容忍数据丢失的场景（如临时缓存）。
	// 默认 false（启用 WAL）。
	DisableWAL bool

	// AsyncWAL 启用异步 WAL：写入使用 NoSync 而非 Sync，
	// 配合 WALBytesPerSync 由 Pebble 后台周期性落盘。
	// 崩溃时可能丢失最近未 sync 的数据。
	// 默认 false（每写 fsync，保证持久性）。
	AsyncWAL bool

	// WALBytesPerSync WAL 后台 sync 的字节间隔。
	// 仅 AsyncWAL=true 时生效。
	// 0 表示不启用后台 sync（依赖操作系统刷盘）。
	// 推荐值：1MB (1 << 20)。
	WALBytesPerSync int

	// WALMinSyncInterval WAL 两次 sync 之间的最小间隔。
	// 引入人工延迟让更多写入合并到同一次 sync，减少 IOPS。
	// 仅 AsyncWAL=true 时生效。
	// 默认 0（不延迟）。
	WALMinSyncInterval time.Duration

	// BatchCommitSize 组提交批量大小。
	// >0 时 consumeLoop 攒批至多 N 个 op，合并为单次 Pebble Batch 提交。
	// 0 表示逐条写入（当前行为）。
	// 推荐值：32-128，取决于写入量和延迟容忍度。
	BatchCommitSize int

	// CacheMaxMB TinyLFU 热点读缓存最大内存占用（MB）。
	// ≤0 时使用默认值 10MB。显式设为 0 用默认。
	// 设为 -1 禁用缓存。
	// 推荐值：10-50MB，视热点数据量而定。
	CacheMaxMB int
}

// applyDefaults 将零值填充为 ProfileEdge 预设。
func (c *Config) applyDefaults() {
	if c.BlockCacheSize <= 0 {
		c.BlockCacheSize = 8 << 20 // 8MB
	}
	if c.MemTableSize <= 0 {
		c.MemTableSize = 4 << 20 // 4MB
	}
	if c.MaxQueueLen <= 0 {
		c.MaxQueueLen = 1024
	}
}
