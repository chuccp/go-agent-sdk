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
	LoadHistory(sessionID string) ([]*chat.Message, error)

	// AppendMessages 追加本批次新产生的消息到持久化存储。
	AppendMessages(sessionID string, messages []*chat.Message) error
}

type Store struct {
	history      *util.SliceArray[*chat.Message]
	tempHistory  *util.SliceArray[*chat.Message]
	lock         sync.RWMutex
	historyStore HistoryStore
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
func (s *Store) loadHistory(sessionId string) error {
	if s.historyStore == nil {
		return nil
	}
	if s.history.IsEmpty() {
		messages, err := s.historyStore.LoadHistory(sessionId)
		if err != nil {
			return err
		}
		s.append(messages...)
	}
	return nil
}

func (s *Store) Save(sessionId string) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.historyStore != nil {
		allTemp := s.tempHistory.Slice()
		if len(allTemp) > 0 {
			msgs := make([]*chat.Message, len(allTemp))
			for i, m := range allTemp {
				msgs[i] = m
				s.history.Append(m)
			}
			s.tempHistory.Reset()
			err := s.historyStore.AppendMessages(sessionId, msgs)
			if err != nil {
				return err
			}
			return err
		}
	}
	return nil
}
func (s *Store) AppendHistory(c *chat.Message) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.tempHistory.Append(c)

}
func NewStore() *Store {
	return &Store{
		history:     new(util.SliceArray[*chat.Message]),
		tempHistory: new(util.SliceArray[*chat.Message]),
	}
}
