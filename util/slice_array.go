package util

import "sync"

// SliceArraySafe is a thread-safe wrapper around SliceArray.
type SliceArraySafe[T comparable] struct {
	sliceArray *SliceArray[T]
	lock       *sync.Mutex
}

func NewSliceArraySafe[T comparable]() *SliceArraySafe[T] {
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
func (sa *SliceArraySafe[T]) Delete(index int) {
	sa.lock.Lock()
	defer sa.lock.Unlock()
	sa.sliceArray.Delete(index)
}
func (sa *SliceArraySafe[T]) Remove(t T) (T, bool) {
	sa.lock.Lock()
	defer sa.lock.Unlock()
	return sa.sliceArray.Remove(t)
}

// SliceArray is a generic dynamic array with automatic power-of-2 growth.
// Zero value is valid — first Append allocates the initial buffer.
type SliceArray[T comparable] struct {
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
func (a *SliceArray[T]) First() T {
	return a.buf[0]
}
func (a *SliceArray[T]) Last() T {
	return a.buf[a.len-1]
}
func (a *SliceArray[T]) ForEach(fn func(index int, value T) bool) {
	if a == nil {
		return
	}
	for i, v := range a.Slice() {
		if !fn(i, v) {
			break
		}
	}
}

func (a *SliceArray[T]) Iter(yield func(index int, value T) bool) {
	if a == nil {
		return
	}
	for i, v := range a.Slice() {
		if !yield(i, v) {
			return
		}
	}
}

// Get returns the element at index. Panics if index is out of bounds.
func (a *SliceArray[T]) Get(index int) T {
	return a.buf[index]
}

// Delete removes the element at index, preserving order.
// Elements after index are shifted left. Panics if index is out of bounds.
func (a *SliceArray[T]) Delete(index int) {
	copy(a.buf[index:], a.buf[index+1:a.len])
	a.len--
}

// Remove finds the first element equal to t, removes it, and returns it.
// Returns (zero, false) if not found.
func (a *SliceArray[T]) Remove(t T) (T, bool) {
	for i := 0; i < a.len; i++ {
		if a.buf[i] == t {
			v := a.buf[i]
			a.Delete(i)
			return v, true
		}
	}
	var zero T
	return zero, false
}

// Len returns the number of elements.
func (a *SliceArray[T]) Len() int { return a.len }

func (a *SliceArray[T]) IsEmpty() bool { return a.len == 0 }

// Cap returns the current capacity of the underlying buffer.
func (a *SliceArray[T]) Cap() int { return cap(a.buf) }

// RemoveFront removes the first n elements, shifting the rest left.
// If n >= len, the array is cleared. Panics if n < 0.
func (a *SliceArray[T]) RemoveFront(n int) {
	if n <= 0 {
		return
	}
	if n >= a.len {
		a.len = 0
		return
	}
	copy(a.buf, a.buf[n:a.len])
	a.len -= n
}

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
