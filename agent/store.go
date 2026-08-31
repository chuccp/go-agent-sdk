package agent

import (
	"sort"
	"sync"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// MessageStore 聊天消息与压缩摘要的持久化接口，由主程序实现。
type MessageStore interface {
	// LoadAfter 读取 Start+Offset > since 的原始消息，按 Start 升序，最多 limit 条。
	// 返回完整历史（含已被摘要取代的旧消息），用于回放与展示。
	LoadAfter(sessionID string, since uint64, limit int) ([]*chat.Message, error)

	// Append 增量追加本批次新产生的消息。
	Append(sessionID string, messages []*chat.Message) error

	// LoadSummary 读取压缩摘要；返回 nil 表示尚未压缩。
	LoadSummary(sessionID string) (*chat.Message, error)

	// SaveSummary 保存压缩摘要（记录分界点），不删除任何历史消息。
	// summary.Start 即分界点：Start < summary.Start 的旧消息在上下文中由摘要取代。
	SaveSummary(sessionID string, summary *chat.Message) error
}

type splitManifest struct {
	starts *util.SliceArray[uint64]
}

func (d *splitManifest) addSplit(lastStart uint64) {
	d.starts.Append(lastStart)
}

func (d *splitManifest) hasSplit(clients []*Client) (uint64, bool) {

	returnStart := uint64(0)
	for {
		if d.starts.IsEmpty() {
			return returnStart, returnStart > 0
		}
		minStart := d.starts.Get(0)
		for _, client := range clients {
			if client.start < minStart {
				return returnStart, returnStart > 0
			}
		}
		d.starts.Delete(0)
		returnStart = minStart
	}
}

type Store struct {
	history           *util.SliceArray[*chat.Message]
	tempHistory       *util.SliceArray[*chat.Message]
	lock              sync.RWMutex
	messageStore      MessageStore
	compressorManager *CompressorManager
	doneManifest      *splitManifest
	sessionID         string
	summary           *chat.Message
}

func (s *Store) IsEmpty() bool {
	s.lock.RLock()
	defer s.lock.RUnlock()
	return s.history.Len() == 0
}

func (s *Store) append(c ...*chat.Message) {
	s.lock.Lock()
	defer s.lock.Unlock()
	for _, m := range c {
		s.history.Append(m)
	}
}
func (s *Store) AppendTemp(c ...*chat.Message) {
	s.lock.Lock()
	defer s.lock.Unlock()
	for _, m := range c {
		s.tempHistory.Append(m)
	}
}

func (s *Store) History() []*chat.Message {
	s.lock.RLock()
	defer s.lock.RUnlock()
	result := make([]*chat.Message, s.history.Len()+s.tempHistory.Len())
	copy(result, s.history.Slice())
	copy(result[s.history.Len():], s.tempHistory.Slice())
	return result
}
func (s *Store) LoadMessagesAfter(since uint64, limit int) ([]*chat.Message, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.summary == nil {
		summary, err := s.messageStore.LoadSummary(s.sessionID)
		if err != nil {
			return nil, err
		}
		if summary == nil {
			s.summary = &chat.Message{
				Start: 0,
			}
		} else {
			s.summary = summary
		}
	}

	messages := make([]*chat.Message, 0)

	if s.history != nil && s.history.Len() > 0 {
		firstMessage := s.history.Get(0)
		if since >= firstMessage.Start {
			s.history.ForEach(func(index int, message *chat.Message) bool {
				if message.Start >= since {
					messages = append(messages, message)
					if len(messages) > limit {
						return false
					}
				}
				return true
			})
		}
	}
	after, err := s.messageStore.LoadAfter(s.sessionID, since, limit)
	if err != nil {
		return nil, err
	}
	if after != nil {
		lastAfter := after[len(after)-1]
		if lastAfter.Start > s.summary.Start {
			if s.history.IsEmpty() {
				for a := range after {
					if after[a].Start > s.summary.Start {
						s.history.Append(after[a])
					}
				}
			} else {
				lastMessage := s.history.Get(s.history.Len() - 1)
				if lastAfter.Start > lastMessage.Start {
					for a := range after {
						if after[a].Start > lastMessage.Start {
							s.history.Append(after[a])
						}
					}

				}
			}
		}
	}

	return after, nil
}

func (s *Store) loadHistory() error {
	if s.messageStore == nil {
		return nil
	}
	if s.history.IsEmpty() {
		messages, err := s.historyStore.LoadHistory(s.loopContext.SessionId())
		if err != nil {
			return err
		}
		sort.Slice(messages, func(i, j int) bool { return messages[i].Start < messages[j].Start })

		s.append(messages...)
	}
	return nil
}
func (s *Store) RecordDone(minStart uint64) {
	s.lock.RLock()
	defer s.lock.RUnlock()
	s.doneManifest.addSplit(minStart)
}

func (s *Store) save(minStart uint64) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.historyStore != nil {
		allTemp := s.tempHistory.Slice()
		if len(allTemp) > 0 {
			var megs []*chat.Message
			for _, m := range allTemp {
				if m.Start <= minStart {
					megs = append(megs, m)
					s.history.Append(m)
				}
			}
			for _, m := range megs {
				s.tempHistory.Remove(m)
			}
			if len(megs) > 0 {
				if err := s.historyStore.AppendMessages(s.loopContext.SessionId(), megs); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func (s *Store) AppendHistory(c *chat.Message) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.tempHistory.Append(c)

}

func (s *Store) hasSplit(slice []*Client) (uint64, bool) {
	return s.doneManifest.hasSplit(slice)
}
func NewStore(sessionId string, compressor Compressor, messageStore MessageStore) *Store {
	return &Store{
		messageStore:      messageStore,
		compressorManager: NewCompressorManager(compressor),
		history:           new(util.SliceArray[*chat.Message]),
		tempHistory:       new(util.SliceArray[*chat.Message]),
		doneManifest: &splitManifest{
			starts: new(util.SliceArray[uint64]),
		},
	}
}
