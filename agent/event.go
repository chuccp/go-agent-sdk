package agent

import (
	"context"
	"iter"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/chuccp/go-agent-sdk/chat"
	sdklog "github.com/chuccp/go-agent-sdk/log"
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
	mu        sync.RWMutex
	resetLock sync.RWMutex
	entries   *util.SliceArray[*Event]

	// signalEvents 存放不需要占用 start 序号的辅助事件（如控制信号、状态通知等）。
	signalEvents *util.SliceArray[*Event]

	chatClients      *util.SliceArray[*Client]
	defaultStore     *Store
	messageLastStart uint64
	sessionId        string
	compressor       Compressor
	historyStore     MessageStore
	no               uint64
	start            atomic.Uint64
	signalStart      atomic.Uint64
}

func NewTransfer(sessionId string, compressor Compressor, historyStore MessageStore) *Transfer {
	transfer := &Transfer{
		sessionId:        sessionId,
		compressor:       compressor,
		historyStore:     historyStore,
		entries:          new(util.SliceArray[*Event]),
		chatClients:      new(util.SliceArray[*Client]),
		signalEvents:     new(util.SliceArray[*Event]),
		messageLastStart: 0,
		no:               0,
	}
	transfer.defaultStore = NewStore(transfer.no, transfer.sessionId, transfer, transfer.compressor, transfer.historyStore)
	return transfer
}
func (l *Transfer) AgentStore() *Store {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.defaultStore
}

// SubAgentStore 为子代理创建隔离的临时 Store：每次调用递增 no 并返回新实例，且不落历史。仅子代理使用。
func (l *Transfer) SubAgentStore() *Store {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.no++
	return NewStore(l.no, l.sessionId, l, l.compressor, nil)
}

func (l *Transfer) LoadMessagesAfter(since uint64) ([]*Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.greaterStart(since)

}

func (l *Transfer) sendEvent(event *Event) {
	l.mu.Lock()
	l.entries.Append(event)
	l.mu.Unlock()
	l.flush()
}

func (l *Transfer) SendBlock(no uint64, block chat.Block) uint64 {
	event := NewEvent(no, l.getAndAddStart(), block)
	l.sendEvent(event)
	return event.Start
}

func (l *Transfer) SendSignalBlock(no uint64, block chat.Block) uint64 {
	event := NewEvent(no, l.signalStart.Add(1), block)
	l.sendEvent(event)
	return event.Start
}

func (l *Transfer) getStart() uint64 {
	return l.start.Load()
}
func (l *Transfer) getAndAddStart() uint64 {
	return l.start.Add(1)
}
func (l *Transfer) storeStart(start uint64) {
	sdklog.Debug("[event] storeSeq", "seq", start, "session", l.sessionId)
	l.start.Store(start)
}
func (l *Transfer) readSignalEvents(cl *Client) ([]*Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// 获取当前客户端需要的信号事件（Start >= cl.signalStart）
	signalEvents := l.greaterSignalEvents(cl.signalStart)
	if len(signalEvents) == 0 {
		return nil, nil
	}

	// 更新客户端的 signalStart 到最新信号事件的终点
	lastEvent := signalEvents[len(signalEvents)-1]
	cl.signalStart = lastEvent.Start + lastEvent.Offset

	// 检查是否所有客户端都已收到这些信号事件，如果是则清理
	l.cleanSignalEvents()

	return signalEvents, nil
}

// greaterSignalEvents 返回 Start >= since 的信号事件（升序）
func (l *Transfer) greaterSignalEvents(since uint64) []*Event {
	events := l.signalEvents.Slice()
	if len(events) == 0 {
		return nil
	}

	// 二分查找第一个 Start >= since 的事件
	idx := sort.Search(len(events), func(i int) bool {
		return events[i].Start >= since
	})

	if idx >= len(events) {
		return nil
	}

	return events[idx:]
}

// cleanSignalEvents 清理所有客户端都已收到的信号事件
func (l *Transfer) cleanSignalEvents() {
	clients := l.chatClients.Slice()
	if len(clients) == 0 {
		return
	}

	// 找到所有客户端中最小的 signalStart
	minSignalStart := uint64(0)
	first := true
	for _, client := range clients {
		if client.isClosed {
			continue
		}
		if first || client.signalStart < minSignalStart {
			minSignalStart = client.signalStart
			first = false
		}
	}

	// 清理 Start + Offset <= minSignalStart 的信号事件
	events := l.signalEvents.Slice()
	idx := 0
	for _, event := range events {
		if event.Start+event.Offset > minSignalStart {
			break
		}
		idx++
	}

	if idx > 0 {
		l.signalEvents.RemoveFront(idx)
		sdklog.Debug("[event] cleanSignalEvents", "cleaned", idx, "remaining", l.signalEvents.Len(), "session", l.sessionId)
	}
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
	lastStart, fa := l.defaultStore.hasSplit(l.chatClients.Slice())
	if fa {
		l.reset(lastStart)
		err := l.defaultStore.save(lastStart)
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
func messageToEvent(m *chat.Message) *Event {

	return &Event{Start: m.Start, Offset: m.Offset, Blocks: m.Content}
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
	messages, err := l.defaultStore.LoadMessagesAfter(start)
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
		event := messageToEvent(msg)
		cache.Append(event)
	}
	events := cache.Slice()
	sort.Slice(events, func(i, j int) bool { return events[i].Start < events[j].Start })

	if l.entries.IsEmpty() {
		if len(events) > 0 {
			last := events[len(events)-1]
			start := last.Start + last.Offset
			if start > l.getStart() {
				l.storeStart(start)
			}
		}
	}
	return events, nil
}
func (l *Transfer) GetChatClient(ctx context.Context, start uint64) *Client {
	l.mu.Lock()
	defer l.mu.Unlock()
	chatClient := NewClient(ctx, start, l)
	chatClient.signalStart = l.signalStart.Load()
	l.chatClients.Append(chatClient)
	sdklog.Debug("[ws] client subscribed", "session", l.sessionId, "start", start)
	return chatClient
}
func (l *Transfer) flush() {
	l.mu.Lock()
	clients := l.chatClients.Slice()
	l.mu.Unlock()
	for _, sub := range clients {
		err := sub.queue.Offer(true)
		if err != nil {
			sdklog.Error("[ws] flush: offer failed", "session", l.sessionId, "error", err)
		}
	}
}
func (l *Transfer) deleteClient(client *Client) {
	sdklog.Debug("[ws] client unsubscribed", "session", l.sessionId, "lastStart", client.start)
	l.chatClients.Remove(client)
	l.resetLock.Lock()
	defer l.resetLock.Unlock()
	lastStart, fa := l.defaultStore.hasSplit(l.chatClients.Slice())
	if fa {
		l.reset(lastStart)
		err := l.defaultStore.save(lastStart)
		if err != nil {
			sdklog.Error("[ws] deleteClient: save failed", "session", l.sessionId, "error", err)
		}
	}
	// 客户端移除后，检查是否可以清理信号事件
	l.cleanSignalEvents()
}
func (l *Transfer) history() []*chat.Message {
	return l.defaultStore.History()
}
