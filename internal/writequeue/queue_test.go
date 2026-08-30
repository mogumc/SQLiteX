package writequeue

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// mockPutter 模拟底层写入。
type mockPutter struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (m *mockPutter) Set(key, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		m.data = make(map[string][]byte)
	}
	m.data[string(key)] = append([]byte(nil), value...)
	return nil
}

func (m *mockPutter) Delete(key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, string(key))
	return nil
}

func (m *mockPutter) get(key string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	return v, ok
}

func TestQueueBasic(t *testing.T) {
	p := &mockPutter{}
	q := New(Config{MaxLen: 16, Putter: p})
	defer q.Stop()

	op := &WriteOp{
		Key:   []byte("k1"),
		Value: []byte("v1"),
		Op:    OpPut,
		Done:  make(chan error, 1),
	}
	if err := q.Submit(op); err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	err := <-op.Done
	if err != nil {
		t.Fatalf("Op failed: %v", err)
	}

	v, ok := p.get("k1")
	if !ok || string(v) != "v1" {
		t.Errorf("expected v1, got %v", v)
	}
}

func TestQueueDelete(t *testing.T) {
	p := &mockPutter{}
	q := New(Config{MaxLen: 16, Putter: p})
	defer q.Stop()

	// 先写入
	op1 := &WriteOp{Key: []byte("k1"), Value: []byte("v1"), Op: OpPut, Done: make(chan error, 1)}
	q.Submit(op1)
	<-op1.Done

	// 再删除
	op2 := &WriteOp{Key: []byte("k1"), Op: OpDelete, Done: make(chan error, 1)}
	q.Submit(op2)
	<-op2.Done

	_, ok := p.get("k1")
	if ok {
		t.Error("expected key to be deleted")
	}
}

func TestQueueFull(t *testing.T) {
	p := &mockPutter{}
	q := New(Config{MaxLen: 1, Putter: p}) // 容量为 1
	defer q.Stop()

	// 填满队列（后台 Goroutine 可能还没来得及消费）
	op1 := &WriteOp{Key: []byte("k1"), Value: []byte("v1"), Op: OpPut, Done: make(chan error, 1)}
	q.Submit(op1)

	// 第二个应该被拒绝
	op2 := &WriteOp{Key: []byte("k2"), Value: []byte("v2"), Op: OpPut, Done: make(chan error, 1)}
	if !errors.Is(q.Submit(op2), ErrFull) {
		t.Errorf("expected ErrFull, got %v", q.Submit(op2))
	}
}

func TestQueueStop(t *testing.T) {
	p := &mockPutter{}
	q := New(Config{MaxLen: 16, Putter: p})

	// 提交几个操作
	for i := 0; i < 5; i++ {
		op := &WriteOp{Key: []byte{byte(i)}, Value: []byte{byte(i)}, Op: OpPut, Done: make(chan error, 1)}
		q.Submit(op)
	}

	// 停止
	q.Stop()

	// 再次提交应该返回 ErrStopped
	op := &WriteOp{Key: []byte("k"), Value: []byte("v"), Op: OpPut, Done: make(chan error, 1)}
	if !errors.Is(q.Submit(op), ErrStopped) {
		t.Errorf("expected ErrStopped after Stop, got %v", q.Submit(op))
	}
}

func TestQueueConcurrency(t *testing.T) {
	p := &mockPutter{}
	q := New(Config{MaxLen: 100, Putter: p})
	defer q.Stop()

	var wg sync.WaitGroup
	// 10 个生产者并发提交
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				key := []byte{byte(id), byte(j)}
				op := &WriteOp{Key: key, Value: []byte("val"), Op: OpPut, Done: make(chan error, 1)}
				if err := q.Submit(op); err != nil {
					t.Errorf("Submit failed: %v", err)
					return
				}
				<-op.Done
			}
		}(i)
	}
	wg.Wait()
}

// blockingPutter 首次写入阻塞直到 release 收到信号，用于制造消费积压。
type blockingPutter struct {
	release chan struct{}
	once    sync.Once
}

func (p *blockingPutter) Set(key, value []byte) error {
	p.once.Do(func() { <-p.release })
	return nil
}

func (p *blockingPutter) Delete(key []byte) error { return nil }

// TestSubmitStopRace 并发 Submit 与 Stop 的回归测试。
// 修复前 Submit 的「检查 stopped → 发送」存在窗口，Stop 排空后
// close channel 会触发 send on closed channel panic。
// 需在 -race 下运行：多轮、多生产者与 Stop 并发，验证既不 panic、
// 也不因 op 无应答而挂死（挂死会表现为测试超时）。
func TestSubmitStopRace(t *testing.T) {
	for round := 0; round < 100; round++ {
		q := New(Config{MaxLen: 8, Putter: &mockPutter{}})
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					op := &WriteOp{Key: []byte("k"), Value: []byte("v"), Op: OpPut, Done: make(chan error, 1)}
					// 仅 Submit 成功时等待应答；ErrStopped/ErrFull 时
					// 队列不负责应答，不得阻塞（与 db.submit 的用法一致）。
					if err := q.Submit(op); err == nil {
						<-op.Done
					}
				}
			}()
		}
		time.Sleep(time.Millisecond)
		close(stop)
		q.Stop()
		wg.Wait()
	}
}

// TestStopRepliesDrainedOps Stop 时已入队未消费的操作必须收到
// ErrStopped 或正常完成应答，调用方不得永久阻塞。
func TestStopRepliesDrainedOps(t *testing.T) {
	// 首次写入阻塞，把后续 op 积压在缓冲区
	slow := &blockingPutter{release: make(chan struct{})}
	q := New(Config{MaxLen: 64, Putter: slow})

	type pending struct {
		op  *WriteOp
		err error
	}
	pendingOps := make([]pending, 0, 16)
	for i := 0; i < 16; i++ {
		op := &WriteOp{Key: []byte("k"), Value: []byte("v"), Op: OpPut, Done: make(chan error, 1)}
		pendingOps = append(pendingOps, pending{op: op, err: q.Submit(op)})
	}

	// 放行被阻塞的写入并给消费者一点处理时间，随后 Stop
	slow.release <- struct{}{}
	time.Sleep(10 * time.Millisecond)
	q.Stop()

	drained := 0
	for _, p := range pendingOps {
		if p.err != nil {
			continue
		}
		select {
		case err := <-p.op.Done:
			if err != nil && !errors.Is(err, ErrStopped) {
				t.Fatalf("unexpected error for drained op: %v", err)
			}
			drained++
		case <-time.After(5 * time.Second):
			t.Fatal("op submitted before Stop never answered: caller would hang")
		}
	}
	if drained == 0 {
		t.Fatal("no drained ops observed; test setup failed to build backlog")
	}
}
