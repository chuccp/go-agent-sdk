package chat

import (
	"sync"

	"github.com/chuccp/go-agent-sdk/util"
)

// HistoryStore 聊天记录持久化接口，由主程序实现。
// SDK 在创建会话时调用 LoadHistory 恢复历史，在每轮对话结束后调用 AppendMessages 增量保存。
type HistoryStore interface {
	// LoadHistory 加载指定会话的历史消息。
	// 返回空切片表示新会话，无历史记录。
	LoadHistory(sessionID string) ([]Message, error)

	// AppendMessages 追加本批次新产生的消息到持久化存储。
	AppendMessages(sessionID string, messages []Message) error
}

type Store struct {
	sessionId    string
	history      *util.SliceArray[*Message]
	historyStore HistoryStore
	mu           sync.RWMutex
	entries      *util.SliceArray[*EventEntry]
	base         uint
	savedLen     int // 上次 SaveHistory 时的 history 长度
	pending      int // 自上次 AppendHistory 以来新增的事件数
}

func NewStore(sessionId string, historyStore HistoryStore) *Store {
	return &Store{
		entries:      new(util.SliceArray[*EventEntry]),
		historyStore: historyStore,
		sessionId:    sessionId,
		history:      new(util.SliceArray[*Message]),
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
	l.pending++
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
	l.pending = 0
}

func (l *Store) LoadHistory() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.historyStore == nil {
		return nil
	}
	if l.history.IsEmpty() {
		msgs, err := l.historyStore.LoadHistory(l.sessionId)
		if err != nil {
			return err
		}
		for i := range msgs {
			l.history.Append(&msgs[i])
		}
		l.savedLen = l.history.Len()
	}
	return nil
}

// History 返回当前全量历史消息快照。
func (l *Store) History() []*Message {
	l.mu.RLock()
	defer l.mu.RUnlock()
	result := make([]*Message, l.history.Len())
	copy(result, l.history.Slice())
	return result
}

// Position 返回当前事件流写头（下一个事件的 seq）。
func (l *Store) Position() uint {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.base + uint(l.entries.Len())
}

// Base 返回活跃缓冲区的起始 seq（用于判断客户端 start 是否已过期）。
func (l *Store) Base() uint {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.base
}

// AppendHistory 将一条消息追加到内存历史。
// 自动计算 Start（该消息关联的第一个事件位置）和 Offset（关联的事件数量）。
func (l *Store) AppendHistory(msg *Message) {
	l.mu.Lock()
	defer l.mu.Unlock()
	currentPos := l.base + uint(l.entries.Len())
	msg.Offset = uint(l.pending)
	if l.pending > 0 {
		msg.Start = currentPos - uint(l.pending)
	}
	l.pending = 0
	l.history.Append(msg)
}

// HistoryLen 返回当前历史消息数量。
func (l *Store) HistoryLen() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.history.Len()
}

// SaveHistory 将自上次保存以来新增的消息持久化到存储。
func (l *Store) SaveHistory() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.historyStore == nil {
		return nil
	}
	all := l.history.Slice()
	newCount := len(all) - l.savedLen
	if newCount <= 0 {
		return nil
	}
	batch := all[l.savedLen:]
	msgs := make([]Message, len(batch))
	for i, m := range batch {
		msgs[i] = *m
	}
	err := l.historyStore.AppendMessages(l.sessionId, msgs)
	if err == nil {
		l.savedLen = len(all)
	}
	return err
}
