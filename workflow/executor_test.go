package workflow

import (
	"testing"

	"github.com/chuccp/go-agent-sdk/workflow/node"
)

func TestNode(t *testing.T) {

	chat := node.NewChatNodeBuilder("chat").Build()
	workflow := Of(chat)

}
