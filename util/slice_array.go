package util

import "sync"

// SliceArraySafe is a thread-safe wrapper around SliceArray.
type SliceArraySafe[T any] struct {
	sliceArray *SliceArray[T]
	lock       *sync.Mutex
}

func NewSliceArraySafe[T any]() *SliceArraySafe[T] {
	return &SliceArraySafe[T]{sliceArray: new(SliceArray[T]), lock: new(sync.Mutex)}
}
func (sa *SliceArraySafe[T]) Append(v T) {
	sa.lock.Lock()
	defer sa.lock.Unlock()
	sa.sliceArray.Append(v)
}
func (sa *SliceArraySafe[T]) Get(index int) T {
	sa.lock.Lock()
	defer sa.lock.Unlock()
	return sa.sliceArray.Get(index)
}
func (sa *SliceArraySafe[T]) Len() int {
	sa.lock.Lock()
	defer sa.lock.Unlock()
	return sa.sliceArray.Len()
}
func (sa *SliceArraySafe[T]) Cap() int {
	sa.lock.Lock()
	defer sa.lock.Unlock()
	return sa.sliceArray.Cap()
}
func (sa *SliceArraySafe[T]) Reset() {
	sa.lock.Lock()
	defer sa.lock.Unlock()
	sa.sliceArray.Reset()
}
func (sa *SliceArraySafe[T]) Slice() []T {
	sa.lock.Lock()
	defer sa.lock.Unlock()
	return sa.sliceArray.Slice()
}

// SliceArray is a generic dynamic array with automatic power-of-2 growth.
// Zero value is valid — first Append allocates the initial buffer.
type SliceArray[T any] struct {
	buf []T
	len int
}

// Append adds an element to the end, growing the buffer if needed.
func (a *SliceArray[T]) Append(v T) {
	if a.len == cap(a.buf) {
		a.grow()
	}
	a.buf[a.len] = v
	a.len++
}

// Get returns the element at index. Panics if index is out of bounds.
func (a *SliceArray[T]) Get(index int) T {
	return a.buf[index]
}

// Len returns the number of elements.
func (a *SliceArray[T]) Len() int { return a.len }

// Cap returns the current capacity of the underlying buffer.
func (a *SliceArray[T]) Cap() int { return cap(a.buf) }

// Reset clears the array, retaining the underlying buffer for reuse.
func (a *SliceArray[T]) Reset() {
	a.len = 0
}

// Slice returns the valid portion of the underlying buffer as a slice.
func (a *SliceArray[T]) Slice() []T {
	return a.buf[:a.len]
}

func (a *SliceArray[T]) grow() {
	c := cap(a.buf)
	newCap := c * 2
	if c == 0 {
		newCap = initialCap
	}
	if newCap > maxInt {
		panic(ErrTooLarge)
	}

	newCap = nextPow2(newCap)
	newBuf := make([]T, newCap)
	if a.len > 0 {
		copy(newBuf, a.buf[:a.len])
	}
	a.buf = newBuf
}
