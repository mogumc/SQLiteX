package sqlitex

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/mogumc/sqlitex/internal/writequeue"
)

// DB 是 SQLiteX 的核心句柄，持有底层 Pebble 引擎实例。
// 调用方通过 Open 创建，通过 Close 释放，生命周期严格管理。
type DB struct {
	pebble *pebble.DB
	cache  *pebble.Cache // 由 profileEdge 创建，Close 时释放
	queue  *writequeue.Queue
	cfg    Config
	closed atomic.Bool
}

// Open 打开或创建指定目录下的 SQLiteX 数据库。
// 传入零值 Config 时使用 ProfileEdge 预设。
func Open(cfg Config) (*DB, error) {
	if cfg.Dir == "" {
		return nil, errors.New("sqlitex: Dir is required")
	}
	cfg.applyDefaults()

	cache := pebble.NewCache(cfg.BlockCacheSize)
	opts := &pebble.Options{
		Cache:                       cache,
		MemTableSize:                uint64(cfg.MemTableSize),
		MemTableStopWritesThreshold: 2,
	}

	// 配置 WAL 相关选项
	if cfg.DisableWAL {
		opts.DisableWAL = true
	}
	if cfg.AsyncWAL {
		if cfg.WALBytesPerSync > 0 {
			opts.WALBytesPerSync = cfg.WALBytesPerSync
		}
		if cfg.WALMinSyncInterval > 0 {
			interval := cfg.WALMinSyncInterval // 捕获到闭包外避免循环引用
			opts.WALMinSyncInterval = func() time.Duration {
				return interval
			}
		}
	}

	pdb, err := pebble.Open(cfg.Dir, opts)
	if err != nil {
		cache.Unref()
		return nil, fmt.Errorf("sqlitex: open pebble: %w", err)
	}

	// 根据 AsyncWAL 选择 WriteOptions
	writeOpts := pebble.Sync
	if cfg.AsyncWAL {
		writeOpts = pebble.NoSync
	}

	q := writequeue.New(writequeue.Config{
		MaxLen:    cfg.MaxQueueLen,
		MaxMemMB:  cfg.MaxMemMB,
		BatchSize: cfg.BatchCommitSize,
		Putter: &pebblePutter{
			db:        pdb,
			writeOpts: writeOpts,
		},
	})

	return &DB{
		pebble: pdb,
		cache:  cache,
		queue:  q,
		cfg:    cfg,
	}, nil
}

// Close 关闭数据库，释放底层资源。
// 先停止写队列（排空并等待待处理写入完成），再关闭 Pebble 并释放 Cache。
// 重复调用返回 ErrDBClosed。
func (db *DB) Close() error {
	if !db.closed.CompareAndSwap(false, true) {
		return ErrDBClosed
	}
	db.queue.Stop()
	err := db.pebble.Close()
	db.cache.Unref()
	return err
}

// Put 写入一个键值对。
// 底层通过 MPSC 队列执行，调用方阻塞等待写入完成。
// 队列满或内存超限时返回 ErrWriteThrottled。
func (db *DB) Put(key, value []byte) error {
	return db.submit(key, value, writequeue.OpPut)
}

// Get 读取指定 Key 的值。
// Key 不存在时返回 (nil, nil)，不视为错误。
// Get 是同步操作，直接读取 Pebble 快照，不走队列。
func (db *DB) Get(key []byte) ([]byte, error) {
	if db.closed.Load() {
		return nil, ErrDBClosed
	}
	if len(key) == 0 {
		return nil, ErrInvalidKey
	}
	val, closer, err := db.pebble.Get(key)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("sqlitex: get: %w", err)
	}
	result := make([]byte, len(val))
	copy(result, val)
	closer.Close()
	return result, nil
}

// Delete 删除指定 Key。
// Key 不存在时不返回错误（幂等语义）。
// 底层通过 MPSC 队列执行。
func (db *DB) Delete(key []byte) error {
	return db.submit(key, nil, writequeue.OpDelete)
}

// PutSync 同步写入，绕过 MPSC 队列直接写入 Pebble。
// 始终使用 pebble.Sync，不受 AsyncWAL 配置影响。
// 仅用于特殊场景（如需要严格顺序保证的初始化写入）。
func (db *DB) PutSync(key, value []byte) error {
	if db.closed.Load() {
		return ErrDBClosed
	}
	if len(key) == 0 {
		return ErrInvalidKey
	}
	return db.pebble.Set(key, value, pebble.Sync)
}

// submit 统一的入队逻辑：校验 → 构造 WriteOp → 提交 → 等待结果。
func (db *DB) submit(key, value []byte, opType writequeue.OpType) error {
	if db.closed.Load() {
		return ErrDBClosed
	}
	if len(key) == 0 {
		return ErrInvalidKey
	}

	done := make(chan error, 1)
	if err := db.queue.Submit(&writequeue.WriteOp{
		Key:   key,
		Value: value,
		Op:    opType,
		Done:  done,
	}); err != nil {
		return ErrWriteThrottled
	}
	return <-done
}

// pebblePutter 实现 writequeue.Putter 和 writequeue.BatchPutter 接口，
// 桥接队列与 Pebble。writeOpts 控制每次写入是否 fsync。
type pebblePutter struct {
	db        *pebble.DB
	writeOpts *pebble.WriteOptions
}

// Set 逐条写入键值对。
func (p *pebblePutter) Set(key, value []byte) error {
	return p.db.Set(key, value, p.writeOpts)
}

// Delete 逐条删除键。
func (p *pebblePutter) Delete(key []byte) error {
	return p.db.Delete(key, p.writeOpts)
}

// ApplyBatch 批量提交一组操作，合并为单次 Pebble Batch 写入。
// Pebble Batch 是原子的：全成功或全失败。
func (p *pebblePutter) ApplyBatch(entries []writequeue.BatchEntry) error {
	batch := p.db.NewBatch()
	for i := range entries {
		var err error
		switch entries[i].Op {
		case writequeue.OpPut:
			err = batch.Set(entries[i].Key, entries[i].Value, p.writeOpts)
		case writequeue.OpDelete:
			err = batch.Delete(entries[i].Key, p.writeOpts)
		}
		if err != nil {
			batch.Close()
			return err
		}
	}
	return batch.Commit(p.writeOpts)
}

// PrefixIterator 封装 Pebble 迭代器，用于前缀扫描。
// Next 按 Key 顺序推进，调用方必须在使用后调用 Close 释放资源。
type PrefixIterator struct {
	iter    *pebble.Iterator
	prefix  []byte
	started bool
}

// Valid 返回迭代器当前位置是否有效且在指定前缀范围内。
func (it *PrefixIterator) Valid() bool {
	if !it.iter.Valid() {
		return false
	}
	k := it.iter.Key()
	if len(k) < len(it.prefix) {
		return false
	}
	for i := 0; i < len(it.prefix); i++ {
		if k[i] != it.prefix[i] {
			return false
		}
	}
	return true
}

// Next 推进到下一个 Key。
// 首次调用时定位到第一条记录，后续调用推进到下一条。
func (it *PrefixIterator) Next() bool {
	if !it.started {
		it.started = true
		it.iter.First()
		return it.Valid()
	}
	it.iter.Next()
	return it.Valid()
}

// Key 返回当前记录的 Key（字节切片）。
func (it *PrefixIterator) Key() []byte {
	return it.iter.Key()
}

// Value 返回当前记录的 Value（字节切片）。
func (it *PrefixIterator) Value() []byte {
	return it.iter.Value()
}

// Close 释放迭代器资源。
func (it *PrefixIterator) Close() error {
	return it.iter.Close()
}

// Iterate 创建一个前缀迭代器，用于按 Key 顺序扫描指定前缀的所有记录。
func (db *DB) Iterate(prefix []byte) *PrefixIterator {
	if db.closed.Load() || len(prefix) == 0 {
		return nil
	}
	iter, err := db.pebble.NewIter(&pebble.IterOptions{LowerBound: prefix})
	if err != nil {
		return nil
	}
	return &PrefixIterator{
		iter:   iter,
		prefix: prefix,
	}
}
