package agent

import (
	"sync"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// HistoryStore 聊天记录持久化接口，由主程序实现。
// SDK 在创建会话时调用 LoadHistory 恢复历史，在每轮对话结束后调用 AppendMessages 增量保存。
type HistoryStore interface {
	// LoadHistory 加载指定会话的历史消息。
	// 返回空切片表示新会话，无历史记录。
	LoadHistory(sessionID string) ([]chat.Message, error)

	// AppendMessages 追加本批次新产生的消息到持久化存储。
	AppendMessages(sessionID string, messages []chat.Message) error
}

type Event struct {
	Seq   uint       `json:"seq"`
	Block chat.Block `json:"block"`
}

func NewEvent(seq uint, block chat.Block) *Event {
	return &Event{
		Seq:   seq,
		Block: block,
	}
}

type Position struct {
	start uint
}

type Store struct {
	sessionId    string
	history0     *util.SliceArray[*chat.Message]
	tempHistory  *util.SliceArray[*chat.Message]
	historyStore HistoryStore
	mu           sync.RWMutex
	entries      *util.SliceArray[*Event]
	seq          uint // 事件序号计数器（下一个 event.Seq），entries 被裁空也不回退
	pending      int  // 自上次 AppendHistory 以来新增的事件数
	positions    *util.SliceArray[*Position]
}

func NewStore(sessionId string, historyStore HistoryStore) *Store {
	return &Store{
		entries:      new(util.SliceArray[*Event]),
		historyStore: historyStore,
		sessionId:    sessionId,
		history0:     new(util.SliceArray[*chat.Message]),
		tempHistory:  new(util.SliceArray[*chat.Message]),
		positions:    new(util.SliceArray[*Position]),
	}
}
func (l *Store) AddBlock(block chat.Block) uint {
	l.mu.Lock()
	defer l.mu.Unlock()
	event := NewEvent(l.seq, block)
	l.seq++
	l.entries.Append(event)
	l.pending++
	return event.Seq
}

// ReadFrom 从 Position 记录的全局偏移读取下一个事件，若无新事件返回 nil。
// 每个事件的 Offset 恒为 1，读后 position 前进 1。
func (l *Store) ReadFrom(position *Position) *Event {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.entries.IsEmpty() {
		return nil
	}
	firstSeq := l.entries.Get(0).Seq
	start := max(position.start, firstSeq)
	idx := int(start - firstSeq)
	if idx >= l.entries.Len() {
		return nil
	}
	event := l.entries.Get(idx)
	if event == nil {
		return nil
	}
	position.start++
	return event
}

// GetPosition 创建并注册一个客户端读取位置。
// start 为初始读取偏移，返回的 Position 由 client 持有并随读取推进。
// 若 start 超过当前写头（如根据持久化历史计算的偏移，而事件流已随服务重启重置），
// 则钳制到写头，保证之后新产生的事件（seq 从写头开始分配）都能被读到。
func (l *Store) GetPosition(start uint) *Position {
	l.mu.Lock()
	defer l.mu.Unlock()
	if start > l.seq {
		start = l.seq
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
	var m uint
	first := true
	for i := 0; i < l.positions.Len(); i++ {
		v := l.positions.Get(i).start
		if first || v < m {
			m = v
			first = false
		}
	}
	return m
}
func (l *Store) ResetAndSave() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.entries.IsEmpty() {
		firstSeq := l.entries.Get(0).Seq
		minStart := l.minPosition()
		if minStart > firstSeq {
			removeCount := min(int(minStart-firstSeq), l.entries.Len())
			l.entries.RemoveFront(removeCount)
		}
	}
	if l.historyStore != nil {
		allTemp := l.tempHistory.Slice()
		if len(allTemp) > 0 {
			msgs := make([]chat.Message, len(allTemp))
			for i, m := range allTemp {
				msgs[i] = *m
				l.history0.Append(m)
			}
			l.tempHistory.Reset()
			err := l.historyStore.AppendMessages(l.sessionId, msgs)
			if err != nil {
				return err
			}
			return err
		}
	}
	return nil

}

func (l *Store) LoadHistory() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.historyStore == nil {
		return nil
	}
	if l.history0.IsEmpty() {
		msgs, err := l.historyStore.LoadHistory(l.sessionId)
		if err != nil {
			return err
		}
		var head uint
		for i := range msgs {
			l.history0.Append(&msgs[i])
			// 取所有消息 start+offset 的最大值作为事件流偏移恢复点
			if end := msgs[i].Start + msgs[i].Offset; end > head {
				head = end
			}
		}
		// 恢复事件流偏移：新事件的 seq 从持久化历史之后接续，
		// 与前端根据历史计算的 start 对齐
		if l.entries.IsEmpty() && head > l.seq {
			l.seq = head
		}
	}
	return nil
}

// History 返回当前全量历史消息快照。
func (l *Store) History() []*chat.Message {
	l.mu.RLock()
	defer l.mu.RUnlock()
	result := make([]*chat.Message, l.history0.Len()+l.tempHistory.Len())
	copy(result, l.history0.Slice())
	copy(result[l.history0.Len():], l.tempHistory.Slice())
	return result
}

// AppendHistory 将一条消息追加到内存历史。
// 自动计算 Start（该消息关联的第一个事件位置）和 Offset（关联的事件数量）。
func (l *Store) AppendHistory(msg *chat.Message) {
	l.mu.Lock()
	defer l.mu.Unlock()
	msg.Offset = uint(l.pending)
	if l.pending > 0 {
		msg.Start = l.seq - uint(l.pending)
	}
	l.pending = 0
	l.tempHistory.Append(msg)
}

// HistoryLen 返回当前历史消息数量。
func (l *Store) HistoryLen() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.history0.Len() + l.tempHistory.Len()
}
