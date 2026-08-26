package agent

import (
	"sort"
	"sync"

	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/util"
)

// HistoryStore 聊天记录持久化接口，由主程序实现。
// SDK 在创建会话时调用 LoadHistory 恢复历史，在每轮对话结束后调用 AppendMessages 增量保存。
type HistoryStore interface {
	// LoadHistory 加载指定会话的历史消息。
	// 返回空切片表示新会话，无历史记录。
	LoadHistory(sessionID string) ([]*chat.Message, error)

	// AppendMessages 追加本批次新产生的消息到持久化存储。
	AppendMessages(sessionID string, messages []*chat.Message) error
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
	useHistory        *util.SliceArray[*chat.Message]
	lock              sync.RWMutex
	historyStore      HistoryStore
	compressorManager *CompressorManager
	loopContext       LoopContext
	doneManifest      *splitManifest
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
func (s *Store) loadHistory() error {
	if s.historyStore == nil {
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
func (s *Store) RecordStart(minStart uint64) {
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
func NewStore(loopContext LoopContext, compressor Compressor, historyStore HistoryStore) *Store {
	return &Store{
		loopContext:       loopContext,
		historyStore:      historyStore,
		compressorManager: NewCompressorManager(compressor),
		history:           new(util.SliceArray[*chat.Message]),
		tempHistory:       new(util.SliceArray[*chat.Message]),
		doneManifest: &splitManifest{
			starts: new(util.SliceArray[uint64]),
		},
	}
}
