package agent

import "github.com/chuccp/go-agent-sdk/chat"

// Lifecycle 会话生命周期钩子接口。
// 实现该接口可监听会话的创建、首条消息、轮次结束和销毁事件。
type Lifecycle interface {
	OnSessionCreated(s *Session)
	OnFirstMessage(ctx Context, msg *chat.Message)
	OnRoundDone(ctx Context)
	OnSessionDestroyed(s *Session)
}

// ── 函数式钩子类型 ──

// OnSessionCreatedFunc 会话创建回调函数。
type OnSessionCreatedFunc func(s *Session)

// OnFirstMessageFunc 首条消息回调函数。
type OnFirstMessageFunc func(ctx Context, msg *chat.Message)

// OnRoundDoneFunc 轮次结束回调函数。
type OnRoundDoneFunc func(ctx Context)

// OnSessionDestroyedFunc 会话销毁回调函数。
type OnSessionDestroyedFunc func(s *Session)

// FuncLifecycle 将独立的函数式回调组合为 Lifecycle 接口实现。
// 每个事件支持注册多个回调，按注册顺序依次执行；未注册的事件为空操作。
type FuncLifecycle struct {
	Created   []OnSessionCreatedFunc
	FirstMsg  []OnFirstMessageFunc
	RoundEnd  []OnRoundDoneFunc
	Destroyed []OnSessionDestroyedFunc
}

func (f *FuncLifecycle) OnSessionCreated(s *Session) {
	for _, fn := range f.Created {
		fn(s)
	}
}

func (f *FuncLifecycle) OnFirstMessage(ctx Context, msg *chat.Message) {
	for _, fn := range f.FirstMsg {
		fn(ctx, msg)
	}
}

func (f *FuncLifecycle) OnRoundDone(ctx Context) {
	for _, fn := range f.RoundEnd {
		fn(ctx)
	}
}

func (f *FuncLifecycle) OnSessionDestroyed(s *Session) {
	for _, fn := range f.Destroyed {
		fn(s)
	}
}

// addCreated 追加会话创建回调。
func (f *FuncLifecycle) addCreated(fn ...OnSessionCreatedFunc) {
	f.Created = append(f.Created, fn...)
}

// addFirstMessage 追加首条消息回调。
func (f *FuncLifecycle) addFirstMessage(fn ...OnFirstMessageFunc) {
	f.FirstMsg = append(f.FirstMsg, fn...)
}

// addRoundEnd 追加轮次结束回调。
func (f *FuncLifecycle) addRoundEnd(fn ...OnRoundDoneFunc) {
	f.RoundEnd = append(f.RoundEnd, fn...)
}

// addDestroyed 追加会话销毁回调。
func (f *FuncLifecycle) addDestroyed(fn ...OnSessionDestroyedFunc) {
	f.Destroyed = append(f.Destroyed, fn...)
}
