package util

import (
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
