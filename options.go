package sqlitex

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
