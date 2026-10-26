# go-agent-sdk

> 用这个项目可以帮你实现 Agent，但没必要。你完全可以让 AI 看完这个项目，再重新实现一个。
> 本项目唯一有价值的是「多端同步显示」的思路——AI 看一眼就学会了。

这不是自谦，是使用建议。下面先说清楚为什么，再把唯一值得参考的那个设计讲透。

## 为什么没必要

- **Agent 循环是标准件。** 流式请求、`tool_use` → `tool_result` 循环、上下文裁剪，每家模型文档里都有。让 AI 按你的 provider 写一遍，比理解本项目的 `Session` / `Store` / `Transfer` 分层更快。
- **这套抽象只是本项目的取舍。** 不是行业标准，也不是最佳实践。为了复用它而引入依赖，你得连带接受它的历史包袱和升级节奏。
- **真正贵的是维护，不是编写。** 模型、API、协议一直在变。通用 SDK 的适配成本最终会落到使用方；按业务重新生成的代码反而更轻。
- **它不生产级。** 单进程、无分布式、无沙箱、无评测、无权限体系。能跑，但不值得当成基础设施。

所以：**把它当参考实现，不要当依赖。** 如果你只想要一个 Agent，用官方 SDK 加几十行循环就够了；如果你想要一个能多端同步的 Agent，把下面这一节看完就够了。

具体做法：把仓库丢给 AI，告诉它你用的模型 provider、部署方式和 UI，让它只保留需要的那几个文件，或者干脆从零写一份——它需要的只是下面这个思路。

## 唯一值得参考的：多端同步显示

### 问题

同一场对话，用户可能同时开着：手机、电脑、两个浏览器标签页、一个命令行。它们要看到同一份流式输出。约束是：

- 每个客户端独立推进，慢的不能拖住快的，也不能拖住服务端生成；
- 断线重连后从断点继续，不丢、不重、不乱序；
- 内存不能随会话无限增长；
- 服务重启后序号能接上；
- 不想为了这个引入 broker / 消息队列。

### 一句话版本

> 把 Agent 的输出当成一条**只追加、带全局递增序号的事件日志**；每个客户端只保存自己的**游标** `start`；事件先进内存缓冲，客户端各自从游标处读；所有客户端都读过的前缀才落盘并从内存裁掉；落后于缓冲头的客户端回源补读。重连 = 拿旧游标新建一个客户端。

### 数据模型

```go
type Event struct {
    No     uint64      // 哪个 Agent：主 Agent / 子 Agent，用于前端分流
    Start  uint64      // 全局序号，事件日志中的绝对位置
    Signal bool        // 信号事件：只推在线客户端，不落历史
    Offset uint64      // 通常为 1；消息用 [Start, Start+Offset) 覆盖它产出的事件
    Blocks []chat.Block
}
```

关键只有两点：

- `Start` 单调递增，永不复用；
- 客户端只需要一个 `Start`，不需要知道会话、历史、WebSocket 连接的任何别的状态。

### 结构

```
                 Agent（唯一生产者）
                       │  Publish(block)：取 seq，追加到 entries
                       ▼
          ┌──────────────────────────────┐
          │ entries  [e8 e9 e10 e11 ...] │  活跃事件窗口
          └──────────────┬───────────────┘
                         │ 每个客户端从自己的 start 往后读
        ┌────────────────┼────────────────┬────────────────┐
        ▼                ▼                ▼                ▼
    Client A         Client B         Client C        Client D（新标签页）
    start=8          start=9          start=10        start=11
     手机              电脑             慢网络           只看最新
                         │
            所有在线客户端都越过水位 W 后：
            entries 裁掉 W 之前 → 历史写入 MessageStore
            落后于 entries 头部的客户端 → MessageStore.LoadAfter(start) 补读
```

### 五条规则

1. **单写多读，全局序号。** 只有 Agent 循环往日志里追加；每次追加原子地取一个 `seq`。所有客户端看到的是同一条有序流。子 Agent 用 `No` 区分，但不另开一条流。
2. **客户端只有游标，没有状态。** `ReadEvents()` 返回 `Start >= cursor` 的事件，然后把游标推到 `last.Start + last.Offset`。断开就丢掉这个对象；重连带旧游标新建一个。服务端生成不依赖任何客户端在线。
3. **先缓冲，后落盘。** 新事件先追加到 `entries`。不是每条都写库，而是只在**消息自成完整历史**的位置记一个水位：用户消息被消费、`tool_use` 和对应的 `tool_result` 都齐了、本轮 `done`。所有在线客户端都越过某个水位后，才把水位之前的历史写入 `MessageStore`，并把 `entries` 裁到水位之后。
4. **落后就回放。** 客户端的 `start` 早于 `entries` 头部时，从 `MessageStore.LoadAfter(start)` 补读，再无缝接上内存里的新事件。这样慢客户端或断线很久的客户端也能拿到完整历史，而内存只保留「最慢的那个客户端还没读完」的窗口。服务重启后从持久化历史恢复 `seq`，新事件无缝接续。
5. **新订阅看最新。** 新开的标签页通常不想重放历史：`LastClient(ctx, start)` 从「当前最新位置」和 `start` 中较大者开始，只接之后的新事件。想回放历史就用 `Client(ctx, start)`，配合 REST 历史接口翻页。

### 两个决定成败的细节

**水位必须落在完整回合上。** 助手消息里有 `tool_use` 时不能记水位——那时工具还没执行，落盘会得到一条悬空的 `tool_use`；等对应的 `tool_result` 也产生后再记。否则客户端重连回放时，模型会收到残缺的 tool 配对，Anthropic 直接 400。

**落盘和内存必须同源。** `MessageStore.Append` 写进去的批次，必须能被 `LoadAfter` 原样读回（`Start` / `Offset` / `Content` 不变，按 `Start` 升序）。内存只保留窗口，窗口之前靠存储回源；两者对不上，重连就会丢内容或错位。

还有一个工程约束：**缓冲要有上限，就必须敢踢客户端。** 某个客户端长期落后于最老的水位，或者空闲超时，直接关掉它——它重连时会带着游标回来补读。不踢，`entries` 就会无界增长。

### 骨架

伪代码，省略锁粒度、错误处理和批量落盘：

```go
type Event struct {
    No     uint64
    Start  uint64
    Signal bool
    Offset uint64
    Blocks []Block
}

type Transfer struct {
    mu      sync.Mutex
    seq     uint64
    entries []*Event       // 活跃窗口
    clients []*Client      // 在线订阅
    store   MessageStore   // 可选：持久层
    splits  []uint64       // 待结算的落盘水位
}

func (t *Transfer) Publish(no uint64, b Block) {
    t.mu.Lock()
    t.entries = append(t.entries, &Event{
        No: no, Start: t.seq, Offset: 1, Blocks: []Block{b},
    })
    t.seq++
    clients := append([]*Client(nil), t.clients...)
    t.mu.Unlock()

    for _, c := range clients {
        c.wake() // 唤醒阻塞在 ReadEvents 的客户端
    }
}

func (t *Transfer) Read(c *Client) ([]*Event, error) {
    t.mu.Lock()
    defer t.mu.Unlock()

    // 游标早于窗口头部（或窗口为空）：先回源补历史
    if t.needReplay(c.start) {
        return t.replay(c)
    }

    out := t.after(c.start) // Start >= c.start
    if n := len(out); n > 0 {
        last := out[n-1]
        c.start = last.Start + last.Offset
        t.settle() // 所有客户端都越过水位 → 落盘 + 裁 entries
    }
    return out, nil
}

func (t *Transfer) settle() {
    // 取最慢客户端的位置，结算所有已被全体越过的水位：
    // store.Append(水位之前的历史); entries = entries[水位之后]
}
```

`replay` 里做两件事：`store.LoadAfter(c.start)` → 转成 `Event` → 推进游标；然后把内存里 `Start >= cursor` 的事件接上。真实实现还要处理分页（`LoadAfter` 返回不足 `limit` 即结束）和「历史还没加载完时新标签页不要回放」的边界。

### 前端怎么配合

多端同步不只是后端推流，前端也要守三条：

- **发送和显示分离。** 发送走 REST，接收走 WebSocket，WebSocket 只推不收。这样多端不会各自发一遍，也不用处理「谁有权发」的竞争。
- **用户消息用状态事件同步。** `queued` / `sent` 用信号事件即时推给在线客户端，`consume` 作为本轮用户消息进入历史；所有端收到 `consume` 后再把消息放进对话框并开始渲染。否则每个标签页都会先显示自己那条，造成重复。
- **把事件当 append-only 日志。** 按 `start` 去重、排序，不要用本地状态推断服务端状态。刷新页面用 REST 的 `since` 补历史，再用 WebSocket 的 `start` 接实时流。

`signal: true` 的事件（`queued` / `sent`、内部错误等）只推给当时在线的客户端，不落历史；缓冲区裁过之后，重连补读也不会再看到它。这是有意的：它们描述的是「此刻的 UI 状态」，不是对话内容；而 `consume` 那条是对话内容，会随历史回放。

整个过程不需要 broker，也不需要 sticky session；客户端断开不中断生成，事件继续缓冲，重连后凭 `start` 补读。同一套结构不只适用于 Agent——任何「一个生产者、多个可能断线的读者」的流式场景（构建日志、任务进度、协同回放）都能直接套。

## 项目里其余的东西

| 目录 | 是什么 | 评价 |
|------|--------|------|
| `agent/` | Agent 循环、Session、Client、Transfer、Store、压缩 | 常规实现；只有 `Transfer` / `Store` 的水位逻辑值得看 |
| `chat/` + `api/chat/anthropic/` | 协议层与 Anthropic 适配 | 跟着官方 API 写，别抄；模型和协议变化很快 |
| `tools/` | 命令行、任务、提问、HTTP、搜索 | 常规封装，按需自己写更贴合 |
| `workflow/` | 实验性的 DAG 流程编排 | 未完成，不建议依赖 |
| `jsonx/` | 动态 JSON 值类型 | 可以独立用，但不是必须 |
| `example/` | Go + React 多端聊天示例 | 用来看多端同步显示怎么落地 |
| `util/`、`log/` | 队列、时间轮、日志门面 | 零散工具 |

## 如果你还是想用

```bash
cd example
cp application.example.yml application.yml   # 填 API Key 与数据库
go run main.go

cd view
cp .env.example .env                          # 指向后端端口
pnpm install && pnpm dev                      # http://localhost:5175
```

最小 Go 用法：

```go
package main

import (
    "context"
    "fmt"

    "github.com/chuccp/go-agent-sdk/agent"
    "github.com/chuccp/go-agent-sdk/api/chat/anthropic"
    "github.com/chuccp/go-agent-sdk/chat"
    "github.com/chuccp/go-agent-sdk/tools"
)

func main() {
    apiKey := "your-api-key"
    config := agent.NewConfig()
    config.ChatOption(
        chat.WithMaxTokens(8192),
        chat.WithSystemPrompt("你是一个智能助手。"),
    )
    config.RegisterChat(anthropic.NewService(
        "anthropic", "https://api.anthropic.com", apiKey, "claude-opus-5-5",
    ))
    config.AddTools(tools.NewCommandTool())
    a := config.CreateServer(context.Background())

    session := a.GetOrCreateSession("demo")
    session.WriteText("你好")

    client := session.Client(context.Background(), 0) // start=0 回放全部历史
    defer client.Close()
    for {
        events, err := client.ReadEvents()
        if err != nil {
            break
        }
        for _, e := range events {
            for _, b := range e.Blocks {
                switch block := b.(type) {
                case *chat.DeltaBlock:
                    fmt.Print(block.Content)
                case *chat.DoneBlock:
                    fmt.Println()
                    return
                }
            }
        }
    }
}
```

完整的 API 手册（Session、工具、压缩、`MessageStore`、REST / WebSocket 协议、配置项）在 [`REFERENCE.md`](REFERENCE.md)。那是重写前的版本，细节仍然准确。

## License

[MIT](LICENSE)
