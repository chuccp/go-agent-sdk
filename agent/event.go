package agent

import (
	"context"
	"iter"
	"log"
	"sort"
	"sync"
	"sync/atomic"

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
	mu               sync.RWMutex
	resetLock        sync.RWMutex
	entries          *util.SliceArray[*Event]
	chatClients      *util.SliceArray[*Client]
	defaultStore     *Store
	messageLastStart uint64
	sessionId        string
	compressor       Compressor
	historyStore     MessageStore
	no               uint64
	seq              atomic.Uint64
}

func NewTransfer(sessionId string, compressor Compressor, historyStore MessageStore) *Transfer {
	transfer := &Transfer{
		sessionId:        sessionId,
		compressor:       compressor,
		historyStore:     historyStore,
		entries:          new(util.SliceArray[*Event]),
		chatClients:      new(util.SliceArray[*Client]),
		messageLastStart: 0,
		no:               0,
	}
	transfer.defaultStore = NewStore(transfer.no, transfer.sessionId, transfer, transfer.compressor, transfer.historyStore)
	return transfer
}
func (l *Transfer) GetDefaultStore() *Store {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.defaultStore
}
func (l *Transfer) GetTempStore() *Store {
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

func (l *Transfer) getSeq() uint64 {
	return l.seq.Load()
}
func (l *Transfer) getAndAddSeq() uint64 {
	return l.seq.Add(1)
}
func (l *Transfer) storeSeq(seq uint64) {
	l.seq.Store(seq)
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
			seq := last.Start + last.Offset
			if seq > l.getSeq() {
				l.storeSeq(seq)
			}
		}
	}
	return events, nil
}
func (l *Transfer) GetChatClient(ctx context.Context, start uint64, handler handler) *Client {
	l.mu.Lock()
	defer l.mu.Unlock()
	if start > l.getSeq() {
		start = l.getSeq()
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
	lastStart, fa := l.defaultStore.hasSplit(l.chatClients.Slice())
	if fa {
		l.reset(lastStart)
		err := l.defaultStore.save(lastStart)
		if err != nil {
			log.Printf("Error offering chat Session: %v", err)
		}
	}
}
func (l *Transfer) history() []*chat.Message {
	return l.defaultStore.History()
}
