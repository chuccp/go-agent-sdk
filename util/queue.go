package util

import (
	"errors"
	"sync"
)

var ErrQueueClosed = errors.New("queue closed")

type Queue[T any] struct {
	sliceQueue *SliceQueue[T]
	lock       *sync.Mutex
	cond       *sync.Cond
	closed     bool
}

func (queue *Queue[T]) Offer(value T) error {
	queue.lock.Lock()
	if queue.closed {
		queue.lock.Unlock()
		return ErrQueueClosed
	}
	queue.sliceQueue.Write(value)
	queue.lock.Unlock()
	// 不阻塞，无信号丢失：cond.Signal 唤醒一个等待者，无等待者时直接返回
	queue.cond.Signal()
	return nil
}

func (queue *Queue[T]) DequeueTimer(timer *Timer) (value T, err error) {
	defer timer.Close()

	timedOut := false
	timer.OnFire(func() {
		queue.lock.Lock()
		timedOut = true
		queue.cond.Broadcast()
		queue.lock.Unlock()
	})

	queue.lock.Lock()

	// 快速路径
	if v, err := queue.sliceQueue.Read(); err == nil {
		queue.lock.Unlock()
		return v, nil
	}
	if queue.closed {
		queue.lock.Unlock()
		var zero T
		return zero, ErrQueueClosed
	}

	for !timedOut {
		v, err := queue.sliceQueue.Read()
		if err == nil {
			queue.lock.Unlock()
			return v, nil
		}
		if queue.closed {
			queue.lock.Unlock()
			var zero T
			return zero, ErrQueueClosed
		}
		queue.cond.Wait()
	}
	// 超时后最后尝试一次
	v, err := queue.sliceQueue.Read()
	queue.lock.Unlock()
	if err == nil {
		return v, nil
	}
	var zero T
	return zero, nil
}

func (queue *Queue[T]) Dequeue() (value T, err error) {
	queue.lock.Lock()
	defer queue.lock.Unlock()
	for {
		v, err := queue.sliceQueue.Read()
		if err == nil {
			return v, nil
		}
		if queue.closed {
			var zero T
			return zero, ErrQueueClosed
		}
		queue.cond.Wait()
	}
}

// NewQueue 创建一个新的 Queue。
func NewQueue[T any]() *Queue[T] {
	q := &Queue[T]{
		sliceQueue: new(SliceQueue[T]),
		lock:       new(sync.Mutex),
	}
	q.cond = sync.NewCond(q.lock)
	return q
}

// Close 关闭队列，唤醒所有等待者。幂等，多次调用安全。
func (queue *Queue[T]) Close() {
	queue.lock.Lock()
	if queue.closed {
		queue.lock.Unlock()
		return
	}
	queue.closed = true
	queue.cond.Broadcast()
	queue.lock.Unlock()
}
