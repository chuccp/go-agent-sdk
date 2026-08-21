package agent

import (
	"log"
	"sync"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

type Event struct {
	No     uint64      `json:"no"`
	Start  uint64      `json:"start"`
	Offset uint64      `json:"offset"`
	Blocks chat.Blocks `json:"block"`
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
	sessionId    string
	seq          uint64
	mu           sync.RWMutex
	entries      *util.SliceArray[*Event]
	pending      uint64
	chatClients  *util.SliceArray[*Client]
	messageStore *Store
}

func NewTransfer(sessionId string, historyStore HistoryStore) *Transfer {
	return &Transfer{
		sessionId:   sessionId,
		entries:     new(util.SliceArray[*Event]),
		chatClients: new(util.SliceArray[*Client]),
		messageStore: &Store{
			history:      new(util.SliceArray[*chat.Message]),
			tempHistory:  new(util.SliceArray[*chat.Message]),
			historyStore: historyStore,
		},
	}

}
func (l *Transfer) GetStore() *Store {
	return l.messageStore
}

func (l *Transfer) LoadHistory() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.messageStore.loadHistory(l.sessionId)
}

func (l *Transfer) SendBlock(no uint64, block chat.Block) uint64 {
	l.mu.Lock()
	event := NewEvent(no, l.seq, block)
	l.seq++
	l.entries.Append(event)
	l.pending++
	l.mu.Unlock()
	l.flush()
	return event.Start
}

func reverseNew(s []*Event) []*Event {
	res := make([]*Event, 0, len(s))
	for i := len(s) - 1; i >= 0; i-- {
		res = append(res, s[i])
	}
	return res
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
	return reverseNew(events)
}

type Events struct {
	events []*Event
}

func (e *Events) addEvent(event *Event) {
	e.events = append(e.events, event)
}

func NewEvents() *Events {
	return &Events{
		events: make([]*Event, 0),
	}
}

func (l *Transfer) greaterStart(start uint64) []*Event {

	cache := new(util.SliceArray[*Event])

	// 从 entries 取数据
	indexLen := l.entries.Len()
	for index := indexLen - 1; index >= 0; index-- {
		event := l.entries.Get(index)
		if event.Start+event.Offset > start {
			cache.Append(event)
		} else {
			return cache.Slice()
		}
	}

	tempLen := l.messageStore.tempHistory.Len()
	// 清理重复数据
	if tempLen > 0 {
		lastEvent := l.messageStore.tempHistory.Get(tempLen - 1)
		for index := cache.Len() - 1; index >= 0; index-- {
			event := cache.Get(index)
			if event.Start < lastEvent.Start+lastEvent.Offset {
				cache.Delete(index)
			}
		}
	}
	// 从 tempHistory 取数据
	for index := tempLen - 1; index >= 0; index-- {
		event := l.entries.Get(index)
		if event.Start+event.Offset > start {
			cache.Append(event)
		} else {
			return cache.Slice()
		}
	}

	hLen := l.messageStore.history.Len()
	// 清理重复数据
	if tempLen == 0 && hLen > 0 {
		lastEvent := l.messageStore.history.Get(hLen - 1)
		for index := cache.Len() - 1; index >= 0; index-- {
			event := cache.Get(index)
			if event.Start < lastEvent.Start+lastEvent.Offset {
				cache.Delete(index)
			}
		}
	}

	// 从 history 取数据
	for index := hLen - 1; index >= 0; index-- {
		event := l.entries.Get(index)
		if event.Start+event.Offset > start {
			cache.Append(event)
		} else {
			return cache.Slice()
		}
	}
	return cache.Slice()
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
			log.Printf("Error offering chat session: %v", err)
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
