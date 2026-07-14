// Package cache 提供 TinyLFU 热点探测 + LRU 内存缓存层。
// Count-Min Sketch 估算访问频率，仅允许高频 Key 进入缓存，
// 免疫全表扫描或恶意请求导致的缓存污染。
package cache

import (
	"container/list"
	"hash/fnv"
	"sync"
	"sync/atomic"
)

// Config 定义缓存参数。
type Config struct {
	// MaxBytes 缓存最大内存占用（字节），0 使用默认值 10MB。
	MaxBytes int64
	// MaxItemBytes 单条记录最大缓存大小（字节），超限直接穿透。
	MaxItemBytes int64
	// AdmissionThreshold 进入缓存所需的最小访问频次。
	AdmissionThreshold uint32
}

// TinyLFU 实现 Count-Min Sketch + LRU 的轻量级热点缓存。
// 零依赖、纯内存、线程安全。
type TinyLFU struct {
	sketch   *countMinSketch
	cache    map[string]*list.Element
	lru      *list.List
	mu       sync.RWMutex

	maxBytes     int64
	maxItemBytes int64
	curBytes     int64
	admThreshold uint32

	hits      atomic.Int64
	misses    atomic.Int64
	evictions atomic.Int64
}

type entry struct {
	key   string
	value []byte
	size  int64
}

// entryOverhead 为 map bucket + list.Element + entry struct + 字符串双存的均摊开销。
// MaxBytes 管控的是 "entry.size"，即 len(key)+len(value)+entryOverhead，反映真实内存占用。
const entryOverhead = 128

// New 创建 TinyLFU 缓存。
func New(cfg Config) *TinyLFU {
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = 10 << 20 // 10MB
	}
	if cfg.MaxItemBytes <= 0 {
		cfg.MaxItemBytes = 1 << 20 // 1MB
	}
	if cfg.AdmissionThreshold <= 1 {
		cfg.AdmissionThreshold = 2
	}

	return &TinyLFU{
		sketch:       newCountMinSketch(),
		cache:        make(map[string]*list.Element),
		lru:          list.New(),
		maxBytes:     cfg.MaxBytes,
		maxItemBytes: cfg.MaxItemBytes,
		admThreshold: cfg.AdmissionThreshold,
	}
}

// Get 从缓存获取值。命中返回 (value, true)，未命中返回 (nil, false)。
// 纯读操作，仅获取 RLock，无写锁争用。
func (c *TinyLFU) Get(key string) ([]byte, bool) {
	c.mu.RLock()
	elem, ok := c.cache[key]
	if ok {
		c.mu.RUnlock()
		c.hits.Add(1)
		ent := elem.Value.(*entry)
		v := make([]byte, len(ent.value))
		copy(v, ent.value)
		return v, true
	}
	c.mu.RUnlock()
	c.misses.Add(1)
	return nil, false
}

// Record 记录一次 key 访问，返回是否应该进入缓存。
func (c *TinyLFU) Record(key string) bool {
	freq := c.sketch.Increment(key)
	return freq >= c.admThreshold
}

// Set 将 key-value 放入缓存。超过 MaxItemBytes 时静默拒绝。
// 若缓存已满，按 LRU 驱逐直到空间足够。
// size 计入 len(key)+len(value)+entryOverhead，反映真实内存占用。
func (c *TinyLFU) Set(key string, value []byte) {
	itemSize := int64(len(key)+len(value)) + entryOverhead
	if itemSize > c.maxItemBytes {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// 已存在则更新
	if elem, ok := c.cache[key]; ok {
		old := elem.Value.(*entry)
		c.curBytes -= old.size
		c.lru.Remove(elem)
	}

	// LRU 驱逐
	for c.curBytes+itemSize > c.maxBytes && c.lru.Len() > 0 {
		c.evictLocked()
	}

	ent := &entry{key: key, value: value, size: itemSize}
	elem := c.lru.PushFront(ent)
	c.cache[key] = elem
	c.curBytes += itemSize
}

// Delete 从缓存中删除 key。
func (c *TinyLFU) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.cache[key]; ok {
		ent := elem.Value.(*entry)
		c.curBytes -= ent.size
		c.lru.Remove(elem)
		delete(c.cache, key)
	}
}

// Stats 返回缓存运行指标：命中、未命中、驱逐次数、当前条目数、当前字节数。
func (c *TinyLFU) Stats() (hits, misses, evictions, entries int64, curBytes int64) {
	c.mu.RLock()
	entries = int64(c.lru.Len())
	curBytes = c.curBytes
	c.mu.RUnlock()
	return c.hits.Load(), c.misses.Load(), c.evictions.Load(), entries, curBytes
}

// evictLocked 驱逐 LRU 尾部条目，调用方必须持有写锁。
func (c *TinyLFU) evictLocked() {
	tail := c.lru.Back()
	if tail == nil {
		return
	}
	ent := tail.Value.(*entry)
	c.curBytes -= ent.size
	c.lru.Remove(tail)
	delete(c.cache, ent.key)
	c.evictions.Add(1)
}

// countMinSketch 用 4 个哈希函数 + 计数器数组估算频率。
// 内存占用：4 × width × 4 bytes (uint32)
type countMinSketch struct {
	rows    [4][]uint32
	width   int
	counter uint64 // 累计增量计数，用于触发全局退化
}

func newCountMinSketch() *countMinSketch {
	width := 2048
	cms := &countMinSketch{width: width}
	for i := range cms.rows {
		cms.rows[i] = make([]uint32, width)
	}
	return cms
}

// Increment 增加计数并返回 4 个哈希位的最小值（频率估计）。
func (c *countMinSketch) Increment(key string) uint32 {
	h1, h2 := hashKey(key)
	return c.increment(h1, h2)
}

func (c *countMinSketch) increment(h1, h2 uint64) uint32 {
	min := uint32(^uint32(0))
	for i := range c.rows {
		idx := c.index(i, h1, h2)
		if c.rows[i][idx] < ^uint32(0) {
			c.rows[i][idx]++
		}
		if c.rows[i][idx] < min {
			min = c.rows[i][idx]
		}
	}
	c.counter++
	// 每 100万次增量全局右移 1 位，避免计数器饱和
	if c.counter%1_000_000 == 0 {
		c.decay()
	}
	return min
}

// decay 全局右移所有计数器 1 位。
func (c *countMinSketch) decay() {
	for i := range c.rows {
		for j := range c.rows[i] {
			c.rows[i][j] >>= 1
		}
	}
}

func (c *countMinSketch) index(row int, h1, h2 uint64) int {
	h := h1 + uint64(row)*h2
	return int(h % uint64(c.width))
}

// hashKey 用 FNV-1a 产生两个 64 位哈希。
func hashKey(key string) (uint64, uint64) {
	h := fnv.New64a()
	h.Write([]byte(key))
	h1 := h.Sum64()
	h.Reset()
	h.Write([]byte(key))
	h.Write([]byte{0xFF})
	return h1, h.Sum64()
}
