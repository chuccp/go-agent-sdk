package agent

import (
	"testing"

	"github.com/chuccp/go-agent-sdk/chat"
)

// TestStore_CompressorHistoryConcurrentWithSave 钉住一条并发约束：Store.history 不只
// agent loop 在写——客户端读事件那条路（Transfer.readEvents → store.save → save0）也会
// 往 history 里追加，两边的锁不能省。
//
// 用 go test -race 才有意义：把 snapshotHistory / replaceHistory 的锁摘掉，这里立刻报
// 「replaceHistory 与 save0/append 数据竞争」；带着锁跑则干净。
func TestStore_CompressorHistoryConcurrentWithSave(t *testing.T) {
	store := NewStore(1, "probe", nil, &CutCompressor{},
		&CompressorOptions{maxContextLength: 100, keepRatio: 0.5}, &seedMessageStore{})
	store.compressorManager.UpdateUsage(&chat.Usage{InputTokens: 100, OutputTokens: 1}) // 超水位
	for i := 1; i <= 9; i++ {
		store.history.Append(seedMsg(uint64(i), 1))
	}

	done := make(chan struct{})
	go func() { // 模拟客户端读事件触发的落盘
		defer close(done)
		for i := 0; i < 2000; i++ {
			store.AppendHistory(seedMsg(uint64(1000+i), 1))
			if err := store.save(uint64(1000 + i)); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for i := 0; i < 2000; i++ { // 模拟 agent loop 每轮取历史
		store.compressorHistory(nil)
	}
	<-done
}
