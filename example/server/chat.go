package server

import (
	"sync"

	"emperror.dev/errors"
	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/example/entity"
	"github.com/chuccp/go-web-frame/util"
)

type Session struct {
	chatManager *agent.ChatManager
	chatClient  *agent.ChatClient
	lock        sync.Mutex
	hasClient   chan bool
}

func (s *Session) HandleChat(message *entity.Message) error {
	client, err := s.getChatClient(message.GetSessionId())
	if err != nil {
		return err
	}
	return client.SendText(message.Message)
}

func (s *Session) HandleStop(message *entity.Message) error {
	client, err := s.getChatClient(message.GetSessionId())
	if err != nil {
		return err
	}
	client.Stop()
	return nil
}

func (s *Session) getChatClient(id string) (*agent.ChatClient, error) {
	if util.IsBlank(id) {
		return nil, errors.New("id is blank")
	}
	s.lock.Lock()

	if s.chatClient != nil {
		s.lock.Unlock()
		return s.chatClient, nil
	}
	s.chatClient = s.chatManager.GetChat(id)
	s.lock.Unlock()
	s.hasClient <- true
	return s.chatClient, nil
}

func (s *Session) ReadEvent() *chat.ClientEvent {

	for {
		if s.chatClient != nil {
			return s.chatClient.ReadEvent()
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

func newSession(chatManager *agent.ChatManager) *Session {
	return &Session{chatManager: chatManager, hasClient: make(chan bool, 1)}
}
