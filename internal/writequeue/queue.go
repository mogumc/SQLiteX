// Package writequeue 提供 MPSC 异步写队列与背压控制。
//
// 设计目标：
// - 多生产者单消费者模型，后台单 Goroutine 消费
// - 队列满或内存超限时快速失败，返回哨兵错误
// - 通过 runtime.MemStats 监控全局内存水位，防止 OOM
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

// Putter 抽象底层写入能力。
type Putter interface {
	Set(key, value []byte) error
	Delete(key []byte) error
}

// Queue 是 MPSC 写队列的核心结构。
// 多生产者通过 Submit 提交操作，单后台 Goroutine 消费。
type Queue struct {
	queue   chan *WriteOp
	putter  Putter
	maxMem  uint64 // 字节
	stopped atomic.Bool
	wg      sync.WaitGroup
}

// Config 定义队列参数。
type Config struct {
	MaxLen   int
	MaxMemMB int64 // 0 表示不启用内存监控
	Putter   Putter
}

// New 创建并启动写队列。
func New(cfg Config) *Queue {
	if cfg.MaxLen <= 0 {
		cfg.MaxLen = 1024
	}
	q := &Queue{
		queue:  make(chan *WriteOp, cfg.MaxLen),
		putter: cfg.Putter,
		maxMem: uint64(cfg.MaxMemMB) << 20,
	}
	q.wg.Add(1)
	go q.consumeLoop()
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

// consumeLoop 后台单 Goroutine 消费队列。
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
