package agent

import (
	"context"
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
	Signal bool        `json:"signal"`
	Offset uint64      `json:"offset"`
	Blocks chat.Blocks `json:"blocks"`
}

func NewEvent(no uint64, seq uint64, block chat.Block) *Event {
	return &Event{
		No:     no,
		Start:  seq,
		Signal: false,
		Offset: 1,
		Blocks: []chat.Block{block},
	}
}

func NewSignalEvent(no uint64, seq uint64, block chat.Block) *Event {
	return &Event{
		No:     no,
		Start:  seq,
		Signal: true,
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
	event := NewSignalEvent(no, l.getAndAddStart(), block)
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
	sdklog.Debug("[event] storeStart", "start", start, "session", l.sessionId)
	cur := l.start.Load()
	if start <= cur {
		return
	}
	if l.start.CompareAndSwap(cur, start) {
		return
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
		return events, nil
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
	for _, v := range l.entries.Iter {
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

	// 防呆：start 超过当前序号计数器时，entries 不可能有匹配事件，直接走内存筛选返回空切片
	if start > l.start.Load() {
		return l.greaterEntries(start), nil
	}

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
			l.storeStart(start)
		}
	}
	return events, nil
}
func (l *Transfer) client(ctx context.Context, start uint64) *Client {
	l.mu.Lock()
	defer l.mu.Unlock()
	chatClient := NewClient(ctx, start, l)
	l.chatClients.Append(chatClient)
	sdklog.Debug("[ws] client subscribed", "session", l.sessionId, "start", start)
	return chatClient
}

func (l *Transfer) lastClient(ctx context.Context) *Client {
	l.mu.Lock()
	defer l.mu.Unlock()
	start := l.getAndAddStart()
	chatClient := NewClient(ctx, start, l)
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
}
func (l *Transfer) history() []*chat.Message {
	return l.defaultStore.History()
}
