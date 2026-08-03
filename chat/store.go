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
	savedLen     int  // 上次 SaveHistory 时的 history 长度
	pending      int  // 自上次 AppendHistory 以来新增的事件数
	ackMu        sync.Mutex
	acks         map[int]uint // clientID → 已确认读取的全局偏移
	clientSeq    int          // 自增 client ID
}

func NewStore(sessionId string, historyStore HistoryStore) *Store {
	return &Store{
		entries:      new(util.SliceArray[*EventEntry]),
		historyStore: historyStore,
		sessionId:    sessionId,
		history:      new(util.SliceArray[*Message]),
		acks:         make(map[int]uint),
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

// ReadFrom 从全局偏移 start 读取下一个事件，若无新事件返回 nil。
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

// AddClient 注册一个事件消费客户端，返回其唯一 ID。
// start 为该客户端的初始读取偏移（通常为 Position()）。
func (l *Store) AddClient(start uint) int {
	l.ackMu.Lock()
	defer l.ackMu.Unlock()
	l.clientSeq++
	l.acks[l.clientSeq] = start
	return l.clientSeq
}

// RemoveClient 注销客户端，后续 Reset 不再考虑其读取位置。
func (l *Store) RemoveClient(id int) {
	l.ackMu.Lock()
	defer l.ackMu.Unlock()
	delete(l.acks, id)
}

// Ack 更新指定客户端已确认读取到的全局偏移。
func (l *Store) Ack(id int, offset uint) {
	l.ackMu.Lock()
	defer l.ackMu.Unlock()
	l.acks[id] = offset
}

// minAck 返回所有已注册客户端中的最小已确认偏移。
// 若无客户端注册，返回 0。
func (l *Store) minAck() uint {
	l.ackMu.Lock()
	defer l.ackMu.Unlock()
	if len(l.acks) == 0 {
		return 0
	}
	var min uint
	first := true
	for _, v := range l.acks {
		if first || v < min {
			min = v
			first = false
		}
	}
	return min
}

// Reset 清理所有客户端均已读取的事件条目，保留未读部分。
// 以所有 client 中已确认偏移最小的为准，只清理该偏移之前的条目。
func (l *Store) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	minAck := l.minAck()
	if minAck <= l.base {
		// 所有 client 尚未读取新条目，不清理
		l.pending = 0
		return
	}
	removeCount := int(minAck - l.base)
	if removeCount > l.entries.Len() {
		removeCount = l.entries.Len()
	}
	l.entries.RemoveFront(removeCount)
	l.base = minAck
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
