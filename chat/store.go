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

type Position struct {
	start uint
}

type Store struct {
	sessionId    string
	history      *util.SliceArray[*Message]
	historyStore HistoryStore
	mu           sync.RWMutex
	entries      *util.SliceArray[*EventEntry]
	writeHead    uint // 下一个待分配的事件 seq，entries 被裁空也不回退
	savedLen     int  // 上次 SaveHistory 时的 history 长度
	pending      int  // 自上次 AppendHistory 以来新增的事件数
	positions    *util.SliceArray[*Position]
}

func NewStore(sessionId string, historyStore HistoryStore) *Store {
	return &Store{
		entries:      new(util.SliceArray[*EventEntry]),
		historyStore: historyStore,
		sessionId:    sessionId,
		history:      new(util.SliceArray[*Message]),
		positions:    new(util.SliceArray[*Position]),
	}
}

func (l *Store) Add(event *ClientEvent) uint {
	l.mu.Lock()
	defer l.mu.Unlock()
	event.Seq = l.writeHead
	l.writeHead++
	l.entries.Append(&EventEntry{
		Start:  event.Seq,
		Offset: 1,
		Event:  event,
	})
	l.pending++
	return event.Seq
}

// ReadFrom 从 Position 记录的全局偏移读取下一个事件，若无新事件返回 nil。
func (l *Store) ReadFrom(position *Position) *EventEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.entries.IsEmpty() {
		return nil
	}
	firstSeq := l.entries.Get(0).Start
	start := position.start
	if start < firstSeq {
		start = firstSeq
	}
	idx := int(start - firstSeq)
	if idx >= l.entries.Len() {
		return nil
	}
	entry := l.entries.Get(idx)
	if entry == nil {
		return nil
	}
	position.start += entry.Offset
	return entry
}

// GetPosition 创建并注册一个客户端读取位置。
// start 为初始读取偏移，返回的 Position 由 client 持有并随读取推进。
// 若 start 超过当前写头（如根据持久化历史计算的偏移，而事件流已随服务重启重置），
// 则钳制到写头，保证之后新产生的事件（seq 从写头开始分配）都能被读到。
func (l *Store) GetPosition(start uint) *Position {
	l.mu.Lock()
	defer l.mu.Unlock()
	if start > l.writeHead {
		start = l.writeHead
	}
	position := &Position{start: start}
	l.positions.Append(position)
	return position
}

// RemovePosition 注销客户端读取位置，后续 Reset 不再考虑该 position。
func (l *Store) RemovePosition(position *Position) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.positions.Remove(position)
}

// minPosition 返回所有已注册 position 中 Start 最小的值。
// 若无 position，返回 0。
func (l *Store) minPosition() uint {
	// 调用方已持有 l.mu，无需再加锁
	if l.positions.Len() == 0 {
		return 0
	}
	var min uint
	first := true
	for i := 0; i < l.positions.Len(); i++ {
		v := l.positions.Get(i).start
		if first || v < min {
			min = v
			first = false
		}
	}
	return min
}

// Reset 清理所有客户端均已读取的事件条目，保留未读部分。
// 以所有 position 中 Start 最小的为准，只清理该偏移之前的条目。
func (l *Store) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	defer func() { l.pending = 0 }()
	if l.entries.IsEmpty() {
		return
	}
	firstSeq := l.entries.Get(0).Start
	minPos := l.minPosition()
	if minPos <= firstSeq {
		return
	}
	removeCount := int(minPos - firstSeq)
	if removeCount > l.entries.Len() {
		removeCount = l.entries.Len()
	}
	l.entries.RemoveFront(removeCount)
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
		var head uint
		for i := range msgs {
			l.history.Append(&msgs[i])
			// 取所有消息 start+offset 的最大值作为事件流偏移恢复点
			if end := msgs[i].Start + msgs[i].Offset; end > head {
				head = end
			}
		}
		// 恢复事件流偏移：新事件的 seq 从持久化历史之后接续，
		// 与前端根据历史计算的 start 对齐
		if l.entries.IsEmpty() && head > l.writeHead {
			l.writeHead = head
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

// WriteHead 返回当前事件流写头（下一个事件的 seq）。
func (l *Store) WriteHead() uint {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.writeHead
}

// AppendHistory 将一条消息追加到内存历史。
// 自动计算 Start（该消息关联的第一个事件位置）和 Offset（关联的事件数量）。
func (l *Store) AppendHistory(msg *Message) {
	l.mu.Lock()
	defer l.mu.Unlock()
	msg.Offset = uint(l.pending)
	if l.pending > 0 {
		msg.Start = l.writeHead - uint(l.pending)
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
