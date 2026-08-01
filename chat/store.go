package chat

import (
	"sync"

	"github.com/chuccp/go-agent-sdk/util"
)

// HistoryStore 聊天记录持久化接口，由主程序实现。
// SDK 在创建会话时调用 LoadHistory 恢复历史，在每轮对话结束后调用 SaveHistory 保存。
type HistoryStore interface {
	// LoadHistory 加载指定会话的历史消息。
	// 返回空切片表示新会话，无历史记录。
	LoadHistory(sessionID string) ([]Message, error)

	// SaveHistory 保存指定会话的完整历史消息。
	// 每次调用传入的是当前会话的完整 history（全量覆盖）。
	SaveHistory(sessionID string, messages []Message) error
}

type Store struct {
	sessionId    string
	history      []Message
	historyStore HistoryStore
	mu           sync.RWMutex
	entries      *util.SliceArray[*EventEntry]
	base         uint
}

func NewStore(sessionId string, historyStore HistoryStore) *Store {
	return &Store{
		entries:      new(util.SliceArray[*EventEntry]),
		historyStore: historyStore,
		sessionId:    sessionId,
	}
}

func (l *Store) Add(event *ClientEvent) uint {
	l.mu.Lock()
	defer l.mu.Unlock()
	event.Seq = l.base + uint(l.entries.Len())
	l.entries.Append(&EventEntry{
		Start:  event.Seq,
		Offset: 1,
		Event:  event,
	})
	return event.Seq
}

// ReadFrom 从全局偏移 start 读取下一个事件，若无新事件返回 nil
func (l *Store) ReadFrom(start uint) *EventEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if start < l.base {
		start = l.base
	}
	idx := int(start - l.base)
	if idx >= l.entries.Len() {
		return nil
	}
	return l.entries.Get(idx)
}

func (l *Store) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.base = l.base + uint(l.entries.Len())
	l.entries.Reset()
}

func (l *Store) LoadHistory() error {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return nil
}
