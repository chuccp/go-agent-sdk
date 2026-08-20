package agent

import (
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

type Position struct {
	start uint64
}
type Store struct {
	history     *util.SliceArray[*chat.Message]
	tempHistory *util.SliceArray[*chat.Message]
}

func (s *Store) IsEmpty() bool {
	return s.history.Len() == 0
}

func (s *Store) Append(c *chat.Message) {
	s.history.Append(c)
}

func NewStore() *Store {
	return &Store{
		history:     new(util.SliceArray[*chat.Message]),
		tempHistory: new(util.SliceArray[*chat.Message]),
	}
}

type SendEvent struct {
	sessionId    string
	seq          uint64
	mu           sync.RWMutex
	entries      *util.SliceArray[*Event]
	pending      uint64
	positions    *util.SliceArray[*Position]
	historyStore HistoryStore
	messageStore *Store
}

func NewSendEvent(sessionId string, historyStore HistoryStore) *SendEvent {
	return &SendEvent{
		sessionId:    sessionId,
		entries:      new(util.SliceArray[*Event]),
		positions:    new(util.SliceArray[*Position]),
		historyStore: historyStore,
		messageStore: NewStore(),
	}

}
func (l *SendEvent) GetMessageStore() *Store {
	return l.messageStore
}

func (l *SendEvent) LoadHistory() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.historyStore == nil {
		return nil
	}
	if l.messageStore.IsEmpty() {
		msgs, err := l.historyStore.LoadHistory(l.sessionId)
		if err != nil {
			return err
		}
		for i := range msgs {
			l.messageStore.Append(&msgs[i])
		}
	}
	return nil
}

func (l *SendEvent) SendBlock(no uint64, block chat.Block) uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	event := NewEvent(no, l.seq, block)
	l.seq++
	l.entries.Append(event)
	l.pending++
	return event.Start
}

func (l *SendEvent) ReadEvents(position *Position) []*Event {
	l.mu.RLock()
	defer l.mu.RUnlock()
	events := l.greaterStart(position.start)
	if len(events) > 0 {
		firstEvent := events[0]
		position.start = firstEvent.Start + firstEvent.Offset
	}
	return events
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

func (l *SendEvent) greaterStart(start uint64) []*Event {

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

func (l *SendEvent) GetPosition(start uint64) *Position {
	l.mu.Lock()
	defer l.mu.Unlock()
	if start > l.seq {
		start = l.seq
	}
	position := &Position{
		start: start,
	}
	l.positions.Append(position)
	return position
}
