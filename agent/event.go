package agent

import (
	"log"
	"sort"
	"sync"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

type Event struct {
	No     uint64      `json:"no"`
	Start  uint64      `json:"start"`
	Offset uint64      `json:"offset"`
	Blocks chat.Blocks `json:"blocks"`
}

func NewEvent(no uint64, seq uint64, block chat.Block) *Event {
	return &Event{
		No:     no,
		Start:  seq,
		Offset: 1,
		Blocks: []chat.Block{block},
	}
}

type Transfer struct {
	seq          uint64
	mu           sync.RWMutex
	entries      *util.SliceArray[*Event]
	pending      uint64
	chatClients  *util.SliceArray[*Client]
	messageStore *Store
}

func NewTransfer(loopContext LoopContext, compressor Compressor, historyStore HistoryStore) *Transfer {
	return &Transfer{
		entries:      new(util.SliceArray[*Event]),
		chatClients:  new(util.SliceArray[*Client]),
		messageStore: NewStore(loopContext, compressor, historyStore),
	}

}
func (l *Transfer) GetStore() *Store {
	return l.messageStore
}

func (l *Transfer) LoadHistory() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.messageStore.loadHistory()
}

func (l *Transfer) SendBlock(no uint64, block chat.Block) uint64 {
	l.mu.Lock()
	event := NewEvent(no, l.seq, block)
	block.SetStart(event.Start)
	l.seq++
	l.entries.Append(event)
	l.pending++
	l.mu.Unlock()
	l.flush()
	return event.Start
}
func (l *Transfer) readEvents(cl *Client) []*Event {
	l.mu.RLock()
	defer l.mu.RUnlock()
	events := l.greaterStart(cl.start)
	if len(events) == 0 {
		return nil
	}
	// events 按 Start 降序排列，第一个元素是最新事件。
	lastEvent := events[0]
	cl.start = lastEvent.Start + lastEvent.Offset
	sort.Slice(events, func(i, j int) bool { return events[i].Start < events[j].Start })
	return events
}

// messageToEvent 将 chat.Message 包装为 Event，供 greaterStart 统一返回。
// 过滤逻辑：从「最后一个 start <= start 的 block」作为起点往后取——起点之前的 block
// 已被客户端消费，起点（含等于 start）及之后的 block 需重发，保证内容不丢。
// start == 0 的 block（未记录序号）不参与起点定位，始终保留。
func messageToEvent(m *chat.Message, start uint64) *Event {
	from := 0
	for i, b := range m.Content {
		if s := b.GetStart(); s != 0 && s <= start {
			from = i
		}
	}
	blocks := m.Content[from:]

	// 按过滤后的 blocks 重新计算 Start/Offset：
	// Start 取最小 start，Offset = 最大 start - 最小 start + 1。
	// ToolResultBlock 等内容跨多个 start 的复合块，顶层 GetStart() 只返回内容里的最小
	// start，若不下钻会漏掉末尾子块、把 Offset 算小（导致 cl.start 推进不足、消息被重复发送）。
	var minStart, maxStart uint64
	consider := func(s uint64) {
		if s == 0 {
			return
		}
		if minStart == 0 || s < minStart {
			minStart = s
		}
		if s > maxStart {
			maxStart = s
		}
	}
	for _, b := range blocks {
		consider(b.GetStart())
		if tr, ok := b.(*chat.ToolResultBlock); ok {
			for _, c := range tr.Content {
				consider(c.GetStart())
			}
		}
	}
	evStart, evOffset := m.Start, m.Offset
	if minStart > 0 {
		evStart = minStart
		evOffset = maxStart - minStart + 1
	}
	return &Event{Start: evStart, Offset: evOffset, Blocks: blocks}
}

func (l *Transfer) greaterStart(start uint64) []*Event {
	cache := new(util.SliceArray[*Event])

	// 1. 从 entries 取数据（当前会话的运行时事件，倒序遍历）
	indexLen := l.entries.Len()
	for index := indexLen - 1; index >= 0; index-- {
		event := l.entries.Get(index)
		if event.Start+event.Offset > start {
			cache.Append(event)
		} else {
			break
		}
	}

	// 2. 合并 history（持久化消息优先于运行时事件）。
	// tempHistory 无需读取：save() 在 Reset() 之前执行，tempHistory 非空时
	// entries 尚未被 Reset 清理、仍保有全量 live 事件，可由上方 entries 兜底；
	// save() 之后 message 已移入 history。
	mergeMessages(cache, l.messageStore.history, start)

	// 4. mergeMessages 追加 message 会破坏降序，重新按 Start 降序排列，
	//    保证 readEvents 里 events[0] 是最大 Start（cl.start 才能正确推进）。
	events := cache.Slice()
	sort.Slice(events, func(i, j int) bool { return events[i].Start > events[j].Start })
	return events
}

// mergeMessages 去重后将 messages 中 >start 的消息合并进 cache。
// 逐条 message 去重，且按 block 级别区间过滤：对 cache 中每个 event 的每个 block，
// 判断其 start 是否落在该 message 覆盖区间 [Start, Start+Offset) 内，是则删除整个
// event（被持久化版本取代）。用 block.start 而非 event.Start，避免 message 合并后
// event.Start 为旧值导致判断错位。
func mergeMessages(cache *util.SliceArray[*Event], messages *util.SliceArray[*chat.Message], start uint64) {
	for index := messages.Len() - 1; index >= 0; index-- {
		msg := messages.Get(index)
		if msg.Start+msg.Offset <= start {
			break // 该消息及更早的消息已全部被消费
		}
		msgEnd := msg.Start + msg.Offset
		for i := cache.Len() - 1; i >= 0; i-- {
			ev := cache.Get(i)
			for _, b := range ev.Blocks {
				bs := b.GetStart()
				if bs >= msg.Start && bs < msgEnd {
					cache.Delete(i)
					break
				}
			}
		}
		cache.Append(messageToEvent(msg, start))
	}
}

func (l *Transfer) GetChatClient(start uint64, handler handler) *Client {
	l.mu.Lock()
	defer l.mu.Unlock()
	if start > l.seq {
		start = l.seq
	}
	chatClient := &Client{
		queue:      util.NewQueue[bool](),
		handler:    handler,
		start:      start,
		readEvents: l,
	}
	l.chatClients.Append(chatClient)
	return chatClient
}
func (l *Transfer) flush() {
	l.mu.Lock()
	clients := l.chatClients.Slice()
	l.mu.Unlock()
	for _, sub := range clients {
		err := sub.queue.Offer(true)
		if err != nil {
			log.Printf("Error offering chat Session: %v", err)
		}
	}
}
func (l *Transfer) deleteClient(client *Client) {
	l.chatClients.Remove(client)
}
func (l *Transfer) history() []*chat.Message {
	return l.messageStore.History()
}
func (l *Transfer) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.entries.IsEmpty() {
		firstSeq := l.entries.Get(0).Start
		minStart := l.minPosition()
		if minStart > firstSeq {
			removeCount := min(int(minStart-firstSeq), l.entries.Len())
			l.entries.RemoveFront(removeCount)
		}
	}
}
func (l *Transfer) minPosition() uint64 {
	// 调用方已持有 l.mu，无需再加锁
	if l.chatClients.Len() == 0 {
		return 0
	}
	var m uint64
	first := true
	for i := 0; i < l.chatClients.Len(); i++ {
		v := l.chatClients.Get(i).start
		if first || v < m {
			m = v
			first = false
		}
	}
	return m
}
