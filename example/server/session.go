package server

import (
	"context"
	"sync"

	"emperror.dev/errors"
	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/example/entity"
	"github.com/chuccp/go-web-frame/util"
)

type Session struct {
	chatManager *agent.Agent
	chatClient  *agent.Client
	lock        sync.Mutex
	hasClient   chan bool
}

func (s *Session) HandleChat(message *entity.WsChatMessage) error {
	if s.chatClient == nil {
		return errors.New("no chat client")
	}
	var opts []chat.Option
	if message.Thinking != "" {
		opts = append(opts, chat.WithThinking(chat.ThinkingLevel(message.Thinking)))
	}
	s.chatClient.WriteText(message.Message)
	return nil
}
func (s *Session) CreateChat(message *entity.WsCreateMessage) error {
	err := s.getChatClient(message.GetSessionId(), message.Start)
	if err != nil {
		return err
	}
	return nil
}

func (s *Session) HandleStop() error {
	if s.chatClient == nil {
		return errors.New("no chat client")
	}
	s.chatClient.Stop()
	return nil
}

func (s *Session) getChatClient(ctx context.Context, id string, start uint64) error {
	if util.IsBlank(id) {
		return errors.New("id is blank")
	}
	s.lock.Lock()

	if s.chatClient != nil {
		s.lock.Unlock()
		return nil
	}
	chatClient, err := s.chatManager.GetClient(id, start)
	if err != nil {
		s.lock.Unlock()
		return err
	}
	s.chatClient = chatClient
	s.lock.Unlock()
	s.hasClient <- true
	return nil
}

func (s *Session) ReadEvent() []*agent.Event {

	for {
		if s.chatClient != nil {
			return s.chatClient.ReadEvents()
		}
		if !<-s.hasClient {
			break
		}
	}
	return nil

}

func (s *Session) Release() {
	s.lock.Lock()
	defer s.lock.Unlock()
	close(s.hasClient)
	if s.chatClient != nil {
		s.chatClient.Close()
	}
}

func newSession(chatManager *agent.Agent) *Session {
	return &Session{chatManager: chatManager, hasClient: make(chan bool, 1)}
}
