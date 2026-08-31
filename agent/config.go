package agent

import "github.com/chuccp/go-agent-sdk/chat"

type Config struct {
	chat          *chat.Chat
	toolExecutors []ToolExecutor
	system        string
	config        *chat.Config
	historyStore  MessageStore
	compressor    Compressor
}

func NewConfig() *Config {
	return &Config{
		toolExecutors: make([]ToolExecutor, 0),
		system:        "",
		config:        chat.DefaultConfig(),
		historyStore:  nil,
		compressor:    nil,
		chat:          chat.NewChat(),
	}

}
