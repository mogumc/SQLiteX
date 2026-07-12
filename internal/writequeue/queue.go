// Package writequeue 提供 MPSC 异步写队列与背压控制。
//
// 设计目标：
// - 多生产者单消费者模型，后台单 Goroutine 消费
// - 队列满或内存超限时快速失败，返回哨兵错误
// - 通过 runtime.MemStats 监控全局内存水位，防止 OOM
// - 可选组提交（BatchSize>0），合并多次写入为单次 Batch 提交
package writequeue

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
)

// 哨兵错误：调用方通过 errors.Is 匹配，禁止字符串比较。
var (
	ErrStopped     = errors.New("writequeue: queue stopped")
	ErrFull        = errors.New("writequeue: queue full")
	ErrMemExceeded = errors.New("writequeue: memory exceeded")
)

// OpType 定义写操作类型。
type OpType int

const (
	OpPut OpType = iota
	OpDelete
)

// WriteOp 表示一个待执行的写操作。
type WriteOp struct {
	Key   []byte
	Value []byte
	Op    OpType
	Done  chan error // 结果回传通道，必须为缓冲 channel（容量 ≥1）
}

// BatchEntry 表示组提交中的一个批量条目。
type BatchEntry struct {
	Key   []byte
	Value []byte
	Op    OpType
}

// Putter 抽象底层写入能力。
type Putter interface {
	Set(key, value []byte) error
	Delete(key []byte) error
}

// BatchPutter 是可选的批量写入接口。
// Putter 实现此接口且 BatchSize>0 时，启用组提交：
// consumeLoop 攒批多个 op 合并为一次 ApplyBatch 调用。
type BatchPutter interface {
	Putter
	ApplyBatch(entries []BatchEntry) error
}

// Queue 是 MPSC 写队列的核心结构。
// 多生产者通过 Submit 提交操作，单后台 Goroutine 消费。
type Queue struct {
	queue     chan *WriteOp
	putter    Putter
	batchSize int // 组提交批量大小，0=逐条消费
	maxMem    uint64
	stopped   atomic.Bool
	wg        sync.WaitGroup
}

// Config 定义队列参数。
type Config struct {
	MaxLen    int
	MaxMemMB  int64 // 0 表示不启用内存监控
	BatchSize int   // 组提交批量大小，0=逐条消费（当前行为）
	Putter    Putter
}

// New 创建并启动写队列。
// BatchSize>0 且 Putter 实现了 BatchPutter 时启用组提交模式。
func New(cfg Config) *Queue {
	if cfg.MaxLen <= 0 {
		cfg.MaxLen = 1024
	}
	q := &Queue{
		queue:     make(chan *WriteOp, cfg.MaxLen),
		putter:    cfg.Putter,
		batchSize: cfg.BatchSize,
		maxMem:    uint64(cfg.MaxMemMB) << 20,
	}
	q.wg.Add(1)
	if cfg.BatchSize > 0 {
		go q.consumeBatchLoop()
	} else {
		go q.consumeLoop()
	}
	return q
}

// Submit 提交一个写操作，同步等待消费完成。
// 队列满时返回 ErrFull，内存超限时返回 ErrMemExceeded，
// 队列已停止时返回 ErrStopped。
func (q *Queue) Submit(op *WriteOp) error {
	if q.stopped.Load() {
		return ErrStopped
	}

	if q.maxMem > 0 {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		if m.Alloc > q.maxMem {
			return ErrMemExceeded
		}
	}

	select {
	case q.queue <- op:
		return nil
	default:
		return ErrFull
	}
}

// Stop 停止队列消费循环。
// 先排空缓冲区中已入队的操作（回传错误避免调用方泄漏），
// 再关闭 channel 并等待消费 Goroutine 退出。
func (q *Queue) Stop() {
	if !q.stopped.CompareAndSwap(false, true) {
		return
	}

	// 排空缓冲区中已入队但未消费的操作
	for {
		select {
		case op := <-q.queue:
			op.Done <- ErrStopped
		default:
			goto drained
		}
	}
drained:
	close(q.queue)
	q.wg.Wait()
}

// consumeLoop 逐条消费队列（BatchSize==0 时使用）。
func (q *Queue) consumeLoop() {
	defer q.wg.Done()
	for op := range q.queue {
		var err error
		switch op.Op {
		case OpPut:
			err = q.putter.Set(op.Key, op.Value)
		case OpDelete:
			err = q.putter.Delete(op.Key)
		}
		op.Done <- err
	}
}

// consumeBatchLoop 批量消费队列（BatchSize>0 时使用）。
// 阻塞等待首个 op，然后非阻塞收集最多 batchSize-1 个 op，
// 合并为一次批量提交以降低 fsync 次数。
func (q *Queue) consumeBatchLoop() {
	defer q.wg.Done()
	for {
		// 阻塞等待第一个 op
		op, ok := <-q.queue
		if !ok {
			return
		}

		batch := []*WriteOp{op}

		// 非阻塞收集更多 op，达到 batchSize 或 channel 空时提交
		for len(batch) < q.batchSize {
			select {
			case op2, ok2 := <-q.queue:
				if !ok2 {
					// channel 关闭，提交当前 batch
					q.applyBatch(batch)
					return
				}
				batch = append(batch, op2)
			default:
				goto submit
			}
		}

	submit:
		q.applyBatch(batch)
	}
}

// applyBatch 提交一批操作。
// Putter 实现 BatchPutter 时走批量路径，否则降级逐条提交。
func (q *Queue) applyBatch(ops []*WriteOp) {
	if len(ops) == 0 {
		return
	}

	// 单 op 或未实现 BatchPutter → 降级逐条
	bp, ok := q.putter.(BatchPutter)
	if !ok || len(ops) == 1 {
		for _, op := range ops {
			var err error
			switch op.Op {
			case OpPut:
				err = q.putter.Set(op.Key, op.Value)
			case OpDelete:
				err = q.putter.Delete(op.Key)
			}
			op.Done <- err
		}
		return
	}

	// 构造批量条目
	entries := make([]BatchEntry, len(ops))
	for i, op := range ops {
		entries[i] = BatchEntry{
			Key:   op.Key,
			Value: op.Value,
			Op:    op.Op,
		}
	}

	// 批量提交：Pebble Batch 是原子的，全成功或全失败
	err := bp.ApplyBatch(entries)
	for _, op := range ops {
		op.Done <- err
	}
}
