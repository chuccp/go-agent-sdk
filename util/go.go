package util

import (
	"fmt"
	"log"
	"runtime/debug"
)

// Go 安全地启动一个 goroutine，内部 recover panic，避免单个协程崩溃导致整个进程退出。
func Go(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[Go] goroutine panic recovered: %v\n%s", r, debug.Stack())
			}
		}()
		fn()
	}()
}

// GoWithRecover 安全地启动一个 goroutine，panic 时调用自定义的 recoverHandler 进行处理。
// 如果 recoverHandler 为 nil，则退化为默认日志输出。
func GoWithRecover(fn func(), recoverHandler func(r any)) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				if recoverHandler != nil {
					recoverHandler(r)
				} else {
					log.Printf("[Go] goroutine panic recovered: %v\n%s", r, debug.Stack())
				}
			}
		}()
		fn()
	}()
}

// Recover 同步执行 fn，捕获其中的 panic 并以 error 返回；fn 正常返回时 error 为 nil。
// 只保留 panic 值、不带堆栈：调用方常把这个错误回给模型，带堆栈会污染上下文。
func Recover(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	return fn()
}
