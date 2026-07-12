package writequeue

import (
	"errors"
	"sync"
	"testing"
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
