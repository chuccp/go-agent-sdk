package agent

import (
	"context"
	"iter"
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
	seq              uint64
	mu               sync.RWMutex
	resetLock        sync.RWMutex
	entries          *util.SliceArray[*Event]
	pending          uint64
	chatClients      *util.SliceArray[*Client]
	messageStore     *Store
	maxBatchSize     int
	messageLastStart uint64
}

func NewTransfer(sessionId string, compressor Compressor, historyStore MessageStore) *Transfer {
	return &Transfer{
		entries:          new(util.SliceArray[*Event]),
		chatClients:      new(util.SliceArray[*Client]),
		messageStore:     NewStore(sessionId, compressor, historyStore),
		maxBatchSize:     10,
		messageLastStart: 0,
	}

}
func (l *Transfer) GetStore() *Store {
	return l.messageStore
}

func (l *Transfer) LoadMessagesAfter(since uint64) ([]*Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	events, err := l.greaterStart(since)
	if err != nil {
		return nil, err
	}
	if events == nil {
		return nil, nil
	}
	if len(events) > 0 {
		last := events[len(events)-1]
		if len(last.Blocks) > 0 {
			lastBlock := last.Blocks[len(last.Blocks)-1]
			l.seq = lastBlock.GetStart() + 1
		} else {
			l.seq = last.Start + last.Offset
		}
	}
	return events, nil
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
func (l *Transfer) readEvents(cl *Client) ([]*Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	events, err := l.greaterStart(cl.start)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}
	// events 按 Start 升序排列，最后一个元素是最新事件。
	lastEvent := events[len(events)-1]
	cl.start = lastEvent.Start + lastEvent.Offset
	l.resetLock.Lock()
	defer l.resetLock.Unlock()
	lastStart, fa := l.messageStore.hasSplit(l.chatClients.Slice())
	if fa {
		l.reset(lastStart)
		err := l.messageStore.save(lastStart)
		if err != nil {
			lastEvent.Blocks = append(lastEvent.Blocks, chat.NewErrorBlock(err.Error()))
		}
	}
	return events, nil
}
func (l *Transfer) reset(minStart uint64) {
	for {
		if l.entries.IsEmpty() {
			return
		}
		firstSeq := l.entries.Get(0).Start
		if minStart >= firstSeq {
			l.entries.Delete(0)
		} else {
			return
		}
	}
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

// greaterEntries 从内存 entries 中筛选 Start >= start 的事件，按 Start 升序返回。
func (l *Transfer) greaterEntries(start uint64) []*Event {
	cache := new(util.SliceArray[*Event])
	for _, v := range iter.Seq2[int, *Event](l.entries.Iter) {
		if v.Start >= start {
			cache.Append(v)
		}
	}
	events := cache.Slice()
	sort.Slice(events, func(i, j int) bool { return events[i].Start < events[j].Start })
	return events
}

func (l *Transfer) greaterStart(start uint64) ([]*Event, error) {
	cache := new(util.SliceArray[*Event])

	// 1. 从 entries 取数据（当前会话的运行时事件）
	if !l.entries.IsEmpty() {
		firstEvent := l.entries.First()
		if firstEvent.Start <= start {
			return l.greaterEntries(start), nil
		}
	}

	// 2. 从持久化存储加载历史消息
	messages, err := l.messageStore.LoadMessagesAfter(start, l.maxBatchSize)
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		if !l.entries.IsEmpty() {
			return l.greaterEntries(start), nil
		}
		return nil, nil
	}
	for _, msg := range messages {
		event := messageToEvent(msg, start)
		cache.Append(event)
	}
	events := cache.Slice()
	sort.Slice(events, func(i, j int) bool { return events[i].Start < events[j].Start })

	return events, nil
}

// mergeMessages 去重后将 messages 中 >start 的消息合并进 cache。
// 逐条 message 去重，且按 block 级别区间过滤：对 cache 中每个 event 的每个 block，
// 判断其 start 是否落在该 message 覆盖区间 [Start, Start+Offset) 内，是则删除整个
// event（被持久化版本取代）。用 block.start 而非 event.Start，避免 message 合并后
// event.Start 为旧值导致判断错位。
//func mergeMessages(cache *util.SliceArray[*Event], messages *util.SliceArray[*chat.Message], start uint64) {
//	for index := messages.Len() - 1; index >= 0; index-- {
//		msg := messages.Get(index)
//		if msg.Start+msg.Offset <= start {
//			break // 该消息及更早的消息已全部被消费
//		}
//		msgEnd := msg.Start + msg.Offset
//		for i := cache.Len() - 1; i >= 0; i-- {
//			ev := cache.Get(i)
//			for _, b := range ev.Blocks {
//				bs := b.GetStart()
//				if bs >= msg.Start && bs < msgEnd {
//					cache.Delete(i)
//					break
//				}
//			}
//		}
//		cache.Append(messageToEvent(msg, start))
//	}
//}

func (l *Transfer) GetChatClient(ctx context.Context, start uint64, handler handler) *Client {
	l.mu.Lock()
	defer l.mu.Unlock()
	if start > l.seq {
		start = l.seq
	}
	chatClient := NewClient(ctx, handler, start, l)
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
	l.resetLock.Lock()
	defer l.resetLock.Unlock()
	lastStart, fa := l.messageStore.hasSplit(l.chatClients.Slice())
	if fa {
		l.reset(lastStart)
		err := l.messageStore.save(lastStart)
		if err != nil {
			log.Printf("Error offering chat Session: %v", err)
		}
	}
}
func (l *Transfer) history() []*chat.Message {
	return l.messageStore.History()
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
