package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/chuccp/go-agent-sdk/chat"
)

// TestTransfer_ConcurrentSubscribeCloseFlush 钉住 chatClients 的并发安全：订阅、推送
// （flush 会遍历订阅列表）、关闭（deleteClient 就地位移底层数组）三条路并发跑。
//
// 配 go test -race 才有意义：关闭不加 l.mu、flush 又直接迭代 Slice() 返回的别名切片时，
// 这里会立刻报数据竞争。
func TestTransfer_ConcurrentSubscribeCloseFlush(t *testing.T) {
	tr := newTestTransfer()
	ctx := context.Background()

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// 推送：每次 SendBlock 都会 flush 一遍订阅列表
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		for i := 0; i < 500; i++ {
			tr.SendBlock(0, chat.NewFullTextBlock("x"))
		}
	}()

	// 订阅 + 关闭：反复建客户端再关掉
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				c := tr.client(ctx, 0)
				c.Close()
			}
		}()
	}
	wg.Wait()

	if tr.chatClients.Len() != 0 {
		t.Errorf("客户端关闭后应全部注销，实际残留 %d 个", tr.chatClients.Len())
	}
}
