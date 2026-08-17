package util

import (
	"errors"
	"io"
	"sync"
)

const initialCap = 64

var ErrTooLarge = errors.New("sliceQueue: too large")

// maxInt is the maximum int value on this platform.
const maxInt = int(^uint(0) >> 1)

// nextPow2 returns the smallest power of two >= n.
func nextPow2(n int) int {
	if n <= 0 {
		return 1
	}
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	n |= n >> 32
	return n + 1
}

type SliceQueueSafe[T any] struct {
	sliceQueue *SliceQueue[T]
	lock       *sync.Mutex
}

func NewSliceQueueSafe[T any]() *SliceQueueSafe[T] {
	return &SliceQueueSafe[T]{sliceQueue: new(SliceQueue[T]), lock: new(sync.Mutex)}
}
func (sqs *SliceQueueSafe[T]) Reset() {
	sqs.lock.Lock()
	defer sqs.lock.Unlock()
	sqs.sliceQueue.Reset()
}
func (sqs *SliceQueueSafe[T]) Write(c T) error {
	sqs.lock.Lock()
	defer sqs.lock.Unlock()
	return sqs.sliceQueue.Write(c)
}
func (sqs *SliceQueueSafe[T]) Read() (T, error) {
	sqs.lock.Lock()
	defer sqs.lock.Unlock()
	return sqs.sliceQueue.Read()
}
func (sqs *SliceQueueSafe[T]) Len() int {
	sqs.lock.Lock()
	defer sqs.lock.Unlock()
	return sqs.sliceQueue.Len()
}

// SliceQueue is a generic ring buffer queue.
// Zero value is valid — first Write allocates the initial buffer.
type SliceQueue[T any] struct {
	buf   []T
	read  int // next element to read
	write int // next slot to write
	count int // number of elements in the queue
}

// Len returns the number of elements in the queue.
func (q *SliceQueue[T]) Len() int { return q.count }

// Reset clears the queue, retaining the underlying buffer for reuse.
func (q *SliceQueue[T]) Reset() {
	q.read = 0
	q.write = 0
	q.count = 0
}
func (q *SliceQueue[T]) IsEmpty() bool { return q.empty() }

func (q *SliceQueue[T]) cap() int  { return cap(q.buf) }
func (q *SliceQueue[T]) mask() int { return q.cap() - 1 }

func (q *SliceQueue[T]) full() bool  { return q.count == q.cap() }
func (q *SliceQueue[T]) empty() bool { return q.count == 0 }

// grow doubles the capacity and reorders elements linearly.
func (q *SliceQueue[T]) grow() {
	c := q.cap()
	newCap := c * 2
	if c == 0 {
		newCap = initialCap
	}
	if newCap > maxInt {
		panic(ErrTooLarge)
	}

	// allocate power-of-2 size for efficient & modulus
	newCap = nextPow2(newCap)
	newBuf := make([]T, newCap)
	if q.count > 0 {
		first := q.read
		end := q.read + q.count
		if end <= c {
			copy(newBuf, q.buf[first:end])
		} else {
			n := copy(newBuf, q.buf[first:])
			copy(newBuf[n:], q.buf[:end-c])
		}
	}
	q.buf = newBuf
	q.read = 0
	q.write = q.count
}

// Write enqueues an element. Returns an error if the operation fails.
func (q *SliceQueue[T]) Write(c T) error {
	if q.full() {
		q.grow()
	}
	q.buf[q.write] = c
	q.write = (q.write + 1) & q.mask()
	q.count++
	return nil
}

// Read dequeues and returns an element. Returns io.EOF if the queue is empty.
func (q *SliceQueue[T]) Read() (T, error) {
	if q.empty() {
		var zero T
		return zero, io.EOF
	}
	c := q.buf[q.read]
	q.read = (q.read + 1) & q.mask()
	q.count--
	return c, nil
}

// ReadAll dequeues all elements and returns them in order.
// The queue is emptied after the call.
func (q *SliceQueue[T]) ReadAll() ([]T, bool) {
	if q.empty() {
		return nil, false
	}
	result := make([]T, q.count)
	first := q.read
	end := q.read + q.count
	if end <= q.cap() {
		copy(result, q.buf[first:end])
	} else {
		n := copy(result, q.buf[first:])
		copy(result[n:], q.buf[:end-q.cap()])
	}
	q.read = 0
	q.write = 0
	q.count = 0
	return result, true
}
