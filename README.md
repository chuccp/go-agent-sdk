# go-agent-sdk

> 用这个项目可以帮你实现 Agent，但没必要。你完全可以让 AI 看完这个项目，再重新实现一个。
> 本项目唯一值得参考的是「多端同步显示」的思路——AI 看一眼就学会了。

## 为什么没必要

因为现在让 AI 写一个简单的 Agent 很容易：流式请求、`tool_use` 循环、上下文裁剪，都是标准动作，按你的 provider 生成一份就能跑。没必要为了这些引入一个 SDK，更没必要为此理解它的一整套抽象。

**把它当参考实现，不要当依赖。**

## 唯一值得参考的：多端同步显示

一句话：

> 把 Agent 输出当成一条带全局递增序号的事件日志；每个客户端只记一个游标 `start`；所有客户端读过的前缀才落盘并从内存裁掉；落后的客户端回源补读。重连就是拿旧游标新建客户端。

```
       Agent（唯一生产者）
             │  seq++
             ▼
   entries [e8 e9 e10 ...]   ← 活跃事件窗口
      │       │       │
   Client A Client B Client C   各自从 start 往后读
   start=8  start=9  start=10
             │
   所有客户端越过水位 W：
   entries 裁到 W 之后，历史写入 MessageStore
   落后于窗口的客户端：MessageStore.LoadAfter(start) 补读
```

几条规则：

1. **全局序号。** 事件只追加，`Start` 单调递增；消息用 `[Start, Start+Offset)` 标记它产出的事件。
2. **客户端无状态。** 只保存 `start`，读到哪里推到哪里；断开即丢，重连带旧 `start`。
3. **先缓冲，后落盘。** 只在完整回合处记水位（用户消息、配对的 `tool_result`、本轮 `done`）；所有在线客户端越过水位后，才落盘并裁掉内存。落后太多或超时的客户端直接关掉，重连能补。
4. **落后就回放。** `start` 早于缓冲头时，从 `MessageStore.LoadAfter(start)` 补读，再无缝接上新事件；新标签页用 `LastClient` 只看最新。
5. **水位不能切在半个回合上。** 只有 `tool_use`、还没等到 `tool_result` 时不能落盘，否则回放会得到残缺配对，Anthropic 直接 400。另外 `Append` 写进去的必须能被 `LoadAfter` 原样读回。

前端配合：发送走 REST、接收走 WebSocket（只推不收）；用户消息用 `queued` / `sent` / `consume` 同步状态；事件按 `start` 去重、排序，别用本地状态猜服务端状态。

## 其余部分

Agent 循环、工具、压缩、Provider 适配都是常规实现，详细文档见 [`REFERENCE.md`](REFERENCE.md)。想跑示例：

```bash
cd example
cp application.example.yml application.yml
go run main.go
```

## License

[MIT](LICENSE)
