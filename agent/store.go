package agent

import (
	"sort"
	"sync"

	"github.com/chuccp/go-agent-sdk/chat"
	sdklog "github.com/chuccp/go-agent-sdk/log"
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

type SendEvent interface {
	sendEvent(event *Event)
	getStart() uint64
	storeStart(seq uint64)
	getAndAddStart() uint64
}

type splitManifest struct {
	starts *util.SliceArray[uint64]
}

func (d *splitManifest) addSplit(lastStart uint64) {
	d.starts.Append(lastStart)
}

func (d *splitManifest) hasSplit(clients []*Client) (uint64, bool) {

	//if sub.isTimeout() {
	//	sub.Close()
	//}

	returnStart := uint64(0)
	for {
		if d.starts.IsEmpty() {
			return returnStart, returnStart > 0
		}

		num := d.starts.Len()

		minStart := d.starts.Get(0)
		hasMin := false
		for _, client := range clients {
			if client.isClosed.Load() {
				continue
			}
			if client.start < minStart {
				if num > 3 {
					client.Close()
				} else {
					hasMin = true
					if client.isTimeout() {
						client.Close()
					}
				}
			}
		}
		if hasMin {
			return returnStart, returnStart > 0
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
	loaded            bool
	summary           *chat.Message
	maxBatchSize      int
	sendEvent         SendEvent
	no                uint64
	startSet          map[uint64]bool
}

func (s *Store) IsEmpty() bool {
	s.lock.RLock()
	defer s.lock.RUnlock()
	return s.history.Len() == 0
}

func (s *Store) HistoryLen() int {
	s.lock.RLock()
	defer s.lock.RUnlock()
	return s.history.Len()
}

func (s *Store) History() []*chat.Message {
	s.lock.RLock()
	defer s.lock.RUnlock()
	result := make([]*chat.Message, s.history.Len()+s.tempHistory.Len())
	copy(result, s.history.Slice())
	copy(result[s.history.Len():], s.tempHistory.Slice())
	return result
}

func (s *Store) LoadAllHistory() error {
	s.lock.Lock()
	defer s.lock.Unlock()
	err := s.loadSummary()
	if err != nil {
		return err
	}
	if s.loaded {
		return nil
	}
	start := s.summary.Start
	if !s.history.IsEmpty() {
		last := s.history.Last()
		start = last.Start + last.Offset
	}
	sdklog.Debug("[store] LoadAllHistory", "session", s.sessionID, "start", start, "loaded", s.loaded)
	for {
		after, err := s.messageStore.LoadAfter(s.sessionID, start, s.maxBatchSize)
		if err != nil {
			return err
		}
		s.mergeHistory(after)
		sdklog.Debug("[store] LoadAllHistory merged", "session", s.sessionID, "loaded", len(after), "historyLen", s.history.Len())
		if len(after) < s.maxBatchSize {
			s.lastStoreStart()
			break
		} else {
			last := s.history.Last()
			start = last.Start + last.Offset
		}
	}
	return nil
}

func (s *Store) loadSummary() error {
	if s.messageStore == nil {
		return nil
	}
	if s.summary == nil {
		summary, err := s.messageStore.LoadSummary(s.sessionID)
		if err != nil {
			return err
		}
		if summary == nil {
			s.summary = &chat.Message{
				Start: 0,
			}
		} else {
			s.summary = summary
		}
	}
	return nil
}

func (s *Store) LoadMessagesAfter(since uint64) ([]*chat.Message, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	limit := s.maxBatchSize
	if s.messageStore == nil {
		return nil, nil
	}
	if limit <= 0 {
		return nil, nil
	}
	err := s.loadSummary()
	if err != nil {
		return nil, err
	}
	// 冷启动预热：缓存为空且 since 越过压缩节点时，把 [summary.Start, since] 的活跃
	// 历史拉进缓存，保证 History()（LLM 上下文）完整。压缩节点之前（Start <= summary.Start）
	// 的旧消息已被摘要取代，不进内存缓存。
	if s.history.IsEmpty() && since > s.summary.Start {
		start := s.summary.Start
		for {
			after, err := s.messageStore.LoadAfter(s.sessionID, start, limit)
			if err != nil {
				return nil, err
			}
			if len(after) == 0 {
				break
			}
			for _, m := range after {
				s.append(m)
			}
			if len(after) < limit {
				break
			}
			lastStart := after[len(after)-1].Start + after[len(after)-1].Offset
			if lastStart < since {
				start = lastStart
			} else {
				break
			}
		}
	}

	// 缓存已覆盖 since 时从缓存回放。
	if s.history.Len() > 0 {
		first := s.history.First()
		last := s.history.Last()
		if since >= first.Start && since < last.Start+last.Offset {
			messages := make([]*chat.Message, 0, limit)
			s.history.ForEach(func(index int, message *chat.Message) bool {
				if message.Start+message.Offset > since {
					messages = append(messages, message)
					if len(messages) >= limit {
						return false
					}
				}
				return true
			})
			return messages, nil
		}
	}

	// 缓存未覆盖（since 在压缩节点之前，或超出缓存末尾）：直接从 DB 读完整原始历史
	// 返回，不因压缩节点丢掉用户历史；只把活跃消息（Start > summary.Start）合并进缓存。
	after, err := s.messageStore.LoadAfter(s.sessionID, since, limit)
	if err != nil {
		return nil, err
	}
	s.mergeHistory(after)
	if len(after) == 0 || len(after) < limit {
		s.lastStoreStart()
	}

	return after, nil
}
func (s *Store) lastStoreStart() {
	if !s.loaded {
		if !s.history.IsEmpty() {
			history := s.history.Last()
			if s.sendEvent != nil {
				s.sendEvent.storeStart(history.Start + history.Offset)
			}
		}
		s.startSet = make(map[uint64]bool)
		s.loaded = true
		historySlice := s.history.Slice()
		sort.Slice(historySlice, func(i, j int) bool { return historySlice[i].Start < historySlice[j].Start })
	}
}
func (s *Store) append(msg *chat.Message) {

	if !s.loaded {
		start := msg.Start
		_, ok := s.startSet[start]
		if !ok {
			s.startSet[start] = true
		} else {
			return
		}
	}
	s.history.Append(msg)

}

// mergeHistory 把回源结果中的活跃消息（Start > summary.Start）合并进缓存，跳过已缓存
// 区间。压缩节点之前（Start <= summary.Start）的旧消息不进内存。
func (s *Store) mergeHistory(after []*chat.Message) {
	if len(after) == 0 {
		return
	}
	boundary := s.summary.Start
	if s.history.Len() > 0 {
		if end := s.history.Last().Start + s.history.Last().Offset; end > boundary {
			boundary = end
		}
	}
	for _, m := range after {
		if m.Start > s.summary.Start && m.Start+m.Offset > boundary {
			s.append(m)
		}
	}
}

func (s *Store) RecordDone(minStart uint64) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.doneManifest.addSplit(minStart)
}

func (s *Store) save(minStart uint64) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.messageStore != nil {
		// 拷贝一份快照，避免遍历期间 Remove 修改底层数组导致跳过/重复元素
		snapshot := append([]*chat.Message(nil), s.tempHistory.Slice()...)
		var megs []*chat.Message
		for _, m := range snapshot {
			if m.Start <= minStart {
				megs = append(megs, m)
				s.append(m)
				s.tempHistory.Remove(m)
			}
		}
		if len(megs) > 0 {
			sdklog.Debug("[store] Append messages to messageStore", "session", s.sessionID, "count", len(megs), "minStart", minStart)
			if err := s.messageStore.Append(s.sessionID, megs); err != nil {
				return err
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
	s.lock.RLock()
	defer s.lock.RUnlock()
	return s.doneManifest.hasSplit(slice)
}
func (s *Store) No() uint64 {
	return s.no
}
func NewStore(no uint64, sessionId string, sendEvent SendEvent, compressor Compressor, messageStore MessageStore) *Store {
	return &Store{
		no:                no,
		sendEvent:         sendEvent,
		maxBatchSize:      10,
		sessionID:         sessionId,
		messageStore:      messageStore,
		compressorManager: NewCompressorManager(compressor),
		history:           new(util.SliceArray[*chat.Message]),
		tempHistory:       new(util.SliceArray[*chat.Message]),
		loaded:            messageStore == nil,
		startSet:          make(map[uint64]bool),
		doneManifest: &splitManifest{
			starts: new(util.SliceArray[uint64]),
		},
	}
}
