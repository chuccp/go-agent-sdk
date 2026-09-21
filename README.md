# go-agent-sdk

一个轻量级 Go AI Agent SDK，提供流式对话、工具调用、历史持久化和断线续传能力，单进程即可运行完整的 Agent 服务。

> **定位：为 Web 端 Agent 优化。** 客户端是浏览器、网络随时可能断开、同一会话常被多个标签页订阅，所以「多客户端订阅 + 断线续传」做成了内建能力。
> 如果只是在本机进程内跑 Agent（没有浏览器、不存在断线重连），断线续传基本用不上，按 `Session` 发送 + `Client` 读取的基础用法即可。

## 核心特性

- **多客户端订阅** — 同一会话可被多个 Client 同时订阅（多标签页），每个 Client 通过 `start` 独立追踪读取进度，互不阻塞
- **断线续传** — 消息自带事件流区间 `[Start, Start+Offset)`，客户端凭一个 `start` 值即可精确续读，无需外部 broker
- **Client 无状态** — `Client` 断开即丢弃，不保留任何会话状态，重连只是换一个 transport
- **最新位订阅** — `Session.LastClient(ctx, start)` 从 `max(当前最新位置, start)` 起订阅，只补之后的新事件
- **消息发送与接收分离** — Session 负责发送消息，Client 仅负责接收事件流
- **流式对话** — 增量块推送 thinking / text / 工具输出，传输无关（示例应用用 WebSocket 承接）
- **多轮工具调用** — 标准 tool_use → tool_result 循环，兼容 Anthropic Messages API
- **历史持久化** — 内存 + DB 双层存储，增量追加，懒加载
- **上下文压缩** — 用量超过上限后按比例丢弃旧历史；压缩方式可插拔：强行切割（零 LLM 调用）或大模型摘要（可指定更便宜的模型），分界点持久化、重启不回流
- **会话超时** — 支持 Session / Client 级别的空闲超时自动销毁
- **生命周期钩子** — 会话创建 / 消息到达 / 轮次结束 / 会话销毁四类回调
- **多提供商** — ServiceStore 支持注册多个 LLM 后端，按请求选择
- **Block 多态** — content 为接口数组，支持 text / thinking / image / tool_use / tool_result / custom_text / server_tool_use / stop

## 架构概览

```
┌───────────────────────────────────────────────────────────────┐
│  Server (Agent 管理器)                                         │
│  ├── Config (构建期配置)                                        │
│  └── Sessions map[id] → Session                               │
│                  └── Session                                   │
│                       ├── SessionContext (状态中心)
│                       │    └── RunContext (运行期 Context 实现: 会话 + 自己的 Store)
│                       │    ├── Agent (会话编排器, runLock 保护)  │
│                       │    │    ├── inbox (消息队列)            │
│                       │    │    ├── HandleMessage (入队+启动)    │
│                       │    │    ├── done (轮次结束回调)          │
│                       │    │    └── do → loop → chatWithStream  │
│                       │    │         ├── executeTools           │
│                       │    │         └── appendMessage          │
│                       │    └── Transfer (事件中转层, 负责事件发送)
│                       │         ├── entries (活跃事件缓冲区)     │
│                       │         ├── chatClients (订阅列表)       │
│                       │         ├── SendBlock (事件发送入口)     │
│                       │         └── Store (消息存储)             │
│                       ├── checkTimeout (会话超时守护)            │
│                       └── Client[] (轻量订阅句柄)                │
└───────────────────────────────────────────────────────────────┘
```

### Context 接口

`Context` 是 Agent 运行期的上下文接口，由 `RunContext` 实现：把 `SessionContext` 与这个 Agent 自己的 `Store` 拼在一起，工具、压缩器、生命周期钩子拿到的都是它。

```go
type Context interface {
    SessionId() string
    GetChat() *chat.Chat
    Store() *Store                                     // 本 Agent 的 Store（子代理是独立 Store）
    Ctx() context.Context                              // 本轮 context，见下

    SendBlock(block chat.Block) uint64                 // 发送事件块（形状与 chat.BlockReceiver 一致）
    SendSignalBlock(block chat.Block) uint64           // 发送信号事件块
    AppendUserMessage(blocks *chat.BlockGroup)         // 追加用户消息到历史
    AppendAssistantMessage(blocks *chat.BlockGroup)    // 追加助手消息到历史
    AppendHistory(msg *chat.Message)                   // 直接追加任意消息

    GetTransferStart() uint64                          // 事件流当前位置
    ChatWithStream(m *chat.Messages, w chat.BlockWriter) error // 临时调一次 LLM
    GetConfig() *chat.Config                           // 会话配置（含 chat.WithUserId 写入的用户标识）
}
```

- `SendBlock` 自动带上当前 Store 的 `no`，把事件分发给订阅该 Store 的所有客户端；`SendSignalBlock` 发的是信号事件（用户消息状态、内部错误等）——只推给当时在线的客户端，不落历史，重连补读时不会重现。因为签名与 `chat.BlockReceiver` 同形，`chat.NewBlockStream(ctx)` 可以直接拿 Context 当接收者。
- `Context` 不再内嵌 `context.Context`，要拿底层 context 用 `Ctx()`：轮次进行中返回本轮 `loopContext`（`Session.Stop()` 取消的就是它），轮次外回落到会话 context（`context.WithoutCancel(session)`，不随会话销毁而取消）。
- **哪个 Store 由 `RunContext` 决定**：会话主 Agent 用默认 Store（`SessionContext.AgentStore()`），子代理用隔离的临时 Store（`SubAgentStore()`）。`SessionContext` 自己不是 `Context`——它没有「我在哪个 Store 上跑」这个信息，而且它的 `SendBlock(no, block)` 多一个调用方自带的 `no` 参数。所以工具的 `Turn.AgentContext()`、压缩器拿到的都是 `RunContext`。
- 在 Agent 循环之外构造 `Turn` 时也走同一个口子：
  ```go
  sctx := manager.SessionContext(id)
  turn := agent.NewTurnWithContext(agent.NewRunContext(sctx, sctx.AgentStore()), args)
  ```

## 包结构

```
go-agent-sdk/
├── agent/          # Agent 层：Server, Agent, Session, Client, RunContext,
│                   #   Transfer, ToolExecutor, Turn, Store, MessageStore,
│                   #   Compressor / CompressorManager / Summary
├── api/chat/       # LLM 提供商适配
│   └── anthropic/  # Anthropic 协议实现（Service, Request, ThinkingConfig）
├── chat/           # 协议层：Block, Message, Config, Stream(BlockWriter), Service, Option
├── tools/          # 内置工具：Command, Todo(Task*), AskUserQuestion, HttpRequest, Search（平台适配）
├── util/           # 通用工具：SliceArray, SliceQueue, Queue, TimeWheel, Go/GoWithRecover/Recover
├── value/          # 动态值类型：Object, Array, Value（支持命名类型）
├── log/            # 日志门面（Debug/Info/Warn/Error）
└── example/        # 完整示例应用（Go 后端 + React 前端）
    ├── api/chat/   # 自定义 provider 预留位（当前为空壳）
    ├── entity/     # DB 实体 + WebSocket 消息定义
    ├── model/      # 数据模型（go-web-frame EntryModel）
    ├── rest/       # REST + WebSocket 路由
    ├── server/     # Agent 组装：Config → 注册 provider/工具/Store → Server
    ├── service/    # 业务逻辑 + MessageStore 实现
    └── view/       # React 前端（@assistant-ui/react）
```

示例应用建在 [`go-web-frame`](https://github.com/chuccp/go-web-frame) 之上（依赖注入 + REST/WebSocket 路由 + DB），SDK 本身不依赖它。

## 快速开始

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
	// 1. 创建配置
	config := agent.NewConfig()
	config.ChatOption(
		chat.WithMaxTokens(4096),
		chat.WithThinking(chat.ThinkingLow),
	)

	// 2. 注册 LLM 提供商（Service 通过 ID() 标识自身，首个注册的为默认；model 也由这里给）
	config.RegisterChat(anthropic.NewService("my-provider", "https://api.anthropic.com", "your-api-key", "claude-sonnet-5"))

	// 3. 注册工具（可选）
	config.AddTools(tools.NewCommandTool())

	// 4. 设置持久化（可选，实现 MessageStore 接口）
	// config.MessageStore(myMessageStore)

	// 5. 设置超时（可选，秒）
	config.SessionTimeout(600) // 会话空闲超时
	config.ClientTimeout(300)  // 客户端空闲超时

	// 6. 基于配置创建 Server（内部启动后台超时清理循环）
	a := config.CreateServer(context.Background())

	// 7. 获取会话
	session := a.GetOrCreateSession("session-1")

	// 8. 发送消息（Session 负责发送，Client 仅负责接收事件流）
	session.WriteText("你好，帮我查看当前目录")

	// 9. 创建客户端读取事件流（事件按 Start 升序返回，Event.Blocks 按 type 字段多态分发）
	client := session.Client(context.Background(), 0)
	defer client.Close()

	for {
		// 阻塞直到有新事件；客户端被关闭（或 Close）后返回错误
		events, err := client.ReadEvents()
		if err != nil {
			fmt.Println("closed:", err)
			break
		}
		for _, event := range events {
			for _, b := range event.Blocks {
				switch block := b.(type) {
				case *chat.DeltaBlock:
					fmt.Print(block.Content) // 流式增量
				case *chat.DoneBlock:
					fmt.Println()
					return
				}
			}
		}
	}
}
```

## 核心概念

### Session 与 Context

`Session` 是会话的容器，包含 `SessionContext`（会话状态中心）和多个 `Client`。`SessionContext` 本身不是 `Context`；每个 `Agent` 由它派生出自己的 `RunContext`（会话 + 该 Agent 的 `Store`），`Context` 提供：

- **会话状态访问** — `SessionId()`, `GetChat()`, `Store()`
- **事件发送** — `SendBlock(block)` 通过 `Transfer` 分发事件（自动带当前 Store 的 `no`）；`SendSignalBlock(no, block)` 发送信号事件
- **历史管理** — `AppendUserMessage()`, `AppendAssistantMessage()`

`Agent`（会话编排器）通过 `Context` 与会话交互，不直接持有 `Transfer`，实现了关注点分离；需要会话级能力（如取子代理 Store、传输进度）时才回落到 `SessionContext`。

`Session` 自身的门面方法：

```go
session.ID()                            // 会话 ID
session.WriteText("你好")                // 发送一条纯文本用户消息
session.WriteBlocks(blocks...)          // 发送任意 blocks
session.WriteTextRound("你好")           // 同上并返回本轮句柄 Done（可注册轮次结束回调）
session.WriteBlocksRound(blocks...)     // 任意 blocks 版本的本轮句柄
session.Client(ctx, start)              // 从绝对位置 start 订阅事件流（断线续传时传入断线前的 start）
session.LastClient(ctx, start)          // 从 max(当前最新位置, start) 订阅：不补历史，只看之后的新事件
session.LoadMessagesAfter(since)        // 取 since 之后的历史事件 []*Event（内存 + 持久层统一获取）
session.GetAgent()                      // 取会话编排器（高级用法）
session.GetSubAgent(prompt, tools...)   // 创建子 Agent：独立临时 Store、不落历史，主循环收发不受影响
session.UpdateChatOption(opt...)        // 运行时更新 LLM 请求参数
session.SessionTimeout(sec)             // 覆盖会话空闲超时
session.ClientTimeout(sec)              // 覆盖客户端空闲超时
session.Stop()                          // 停止当前轮次（只对单轮生效），后续用户消息不受影响
session.Destroy()                       // 销毁会话：移除、取消 ctx，并把未提交消息落盘
```

### 生命周期钩子

`Config` 提供四类函数式钩子，覆盖会话的完整生命周期：

```go
config.OnSessionCreated(func(s *agent.Session) { ... })                  // 会话创建
config.OnMessage(func(ctx agent.Context, block *chat.UserBlock) { ... }) // 每条用户消息到达
config.OnRoundDone(func(ctx agent.Context) { ... })                      // 每轮结束
config.OnSessionDestroyed(func(s *agent.Session) { ... })                // 会话销毁
```

也可实现 `agent.Lifecycle` 接口后通过 `config.AddLifecycle(l)` 一次性注册；每类事件支持多个回调，按注册顺序执行。

### 本轮结束回调（Done）

`Done` 是「本轮句柄」：`Done(f)` 才真正把消息发出去，并在本轮结束（发出并持久化 `DoneBlock`）后执行回调 `f`。

```go
// 写入消息并注册回调，本轮结束时触发
session.WriteTextRound("统计当前目录的文件数").Done(func() {
    // 轮次结束后的自定义逻辑
    fmt.Println("round completed")
})
```

`Session.WriteTextRound / WriteBlocksRound` 是 `Agent.HandleRoundMessage(blocks)` 的便捷包装，拿到句柄后也可稍后再调 `Done`。

> 注意：完成回调挂在 `Agent` 上是单槽位，多客户端场景会互相覆盖，只适合单客户端使用。

### Block（内容块）

消息的 `Content` 是 `Blocks`（`[]Block` 接口数组），支持多态 JSON 序列化。每个具体块都带 `Type BlockType` 字段，反序列化时按 `type` 分发还原（`Blocks` 实现自定义 `UnmarshalJSON`），历史持久化加载后可无损往返。

```go
type Block interface {
    ForContext() bool       // 声明该块是否进入 LLM 上下文
    GetType() BlockType     // 块类型标识
}

// 具体类型（均嵌入 BaseBlock）
TextBlock          { Text string; TextType TextType; ToolUseId string }
ThinkingBlock      { Thinking string }
ImageBlock         { Source *ImageSource }
ToolUseBlock       { ID, Name string; Input *value.Object }
ToolResultBlock    { ToolUseID string; Content Blocks }
ServerToolUseBlock { ID, Name string; Input json.RawMessage }           // Anthropic 服务端内置工具调用（如 web_search）
CustomTextBlock    { Text string; TextType TextType; ToolUseId string }  // 业务扩展（不进上下文）
MessageStartBlock  { Usage *Usage }
MessageDeltaBlock  { Usage *Usage }
StartBlock         { Block UseDeltaBlock }                              // 流式块起始标记
DeltaBlock         { Content string }                                  // 流式增量
StopBlock          { }                                                 // 流式块结束标记
DoneBlock          { Usage *Usage }                                    // 本轮结束（携带 token 用量）
UserBlock          { BlockUserType BlockUserType; ID uint64; Content Blocks } // 用户消息状态
ErrorBlock         { Text string }
```

`ForContext()` 决定块是否进入 LLM 上下文：进的是 `TextBlock` / `ImageBlock` / `ToolUseBlock` / `ToolResultBlock` / `UserBlock`；thinking、流式标记（start/delta/stop）、用量（message_start/message_delta）、错误、custom_text 都不进。

`TextType` 标在 `TextBlock` / `CustomTextBlock` 上——这两种块形状一样，靠它给前端分流：

```go
ErrorTextType       = "error"          // 错误文本
CMDTextType         = "cmd"            // 命令输出
FlowProgressType    = "flow_progress"  // 工作流进度
AskUserTextType     = "ask_user"       // 提问卡片（历史残留：问题现已随 tool_use 入参下发）
InternalTextType    = "internal"       // 只给 LLM 看的文本，前端不显示
CompressionTextType = "compression"    // 压缩摘要正文，前端不显示
```

`UserBlock.BlockUserType` 标用户消息的生命周期：`queued`（上一轮还没结束，先排队）→ `sent`（已入队，马上开始）→ `consume`（已被消费，本轮启动）。前端据此决定何时把用户消息放进对话框。

### 动态值（value）

`value` 包提供一套可独立使用的动态 JSON 值类型（`Object` / `Array` / `Text` / `Number` / `Bool` / `Null` / `Stream`），可作为工具入参等动态结构的统一载体：

```go
import "github.com/chuccp/go-agent-sdk/value"

obj := value.NewObject()
obj.PutAny("level", chat.ThinkingHigh) // 原生类型 / 命名类型自动转换
obj.Put("n", value.NewInt(3))          // int/float 区分保留，序列化为 3 而非 3.0
obj.GetString("level")                 // "high"
obj.GetInt("n")                        // 3

raw := obj.ToJSON()                    // 序列化，字符串不二次转义
```

命名类型（如 `ThinkingLevel` / `Role`）经反射兜底不会漏成 null；所有读方法对 nil 接收者安全，返回零值而非 panic。工具入参 `ToolUseBlock.Input` 即由 `*value.Object` 承载。

### 事件流与断线续传

每条 Message 携带事件区间 `[Start, Start+Offset)`，标记它产出了哪些事件，区间与全局单调递增的事件序号 `seq` 对齐。客户端持有一个绝对偏移 `start` 即可从活跃事件缓冲区（`entries`）增量续读。事件按 **Start 升序** 返回。

事件发送通过 `Transfer.SendBlock` 统一处理，`RunContext.SendBlock`（补上当前 Store 的 `no`）和 `SessionContext.SendBlock`（调用方自带 `no`）都委托给它。这种方式将事件生成与传输解耦，确保多客户端订阅时的一致性。

`doneManifest` 记录一串「落盘水位」：用户输入消息、已配对的 `tool_result`、每轮结束的 `done` 块各记一次。水位只在消息自成完整历史时才记录——助手消息含 `tool_use` 时不记，因为那时本轮工具还没执行，记下水位会让单独的 `tool_use` 落盘成悬空配对。当所有客户端都已消费到某个水位时，旧事件被裁掉（`reset`），对应历史迁入持久层（`save`）；`start` 早于缓冲区头部时回落到底层存储补读。服务重启后从历史恢复 `seq`，新事件无缝接续。

多个 Client 同时订阅时，每个 Client 通过各自的 `start` 独立推进读取进度，互不阻塞。不关心历史、只看新事件的场景可用 `Session.LastClient(ctx, 0)`，自动从当前最新位置开始订阅。

整个过程不依赖任何 broker、单进程即可完成，运维成本为零；且生成与传输解耦——客户端断开不中断服务端生成，事件继续缓冲，重连后凭 `start` 补读积压事件，像什么都没发生过。

## 工具系统

实现 `ToolExecutor` 接口即可注册自定义工具：

```go
type ToolExecutor interface {
    Definition() *chat.ToolFunction                     // 工具元数据（发给 LLM）
    Name() string                                       // 工具唯一名称
    UsagePrompt() string                                // 工具引导提示词（随每轮 System 注入）
    Execute(turn *Turn, writer *chat.ToolResultBlockStream) // 执行逻辑
}
```

`Turn` 是每次工具执行的载体：`Args()` 取工具入参（`*value.Object`），`AgentContext()` 取会话上下文（`agent.Context`，调用 LLM、发事件、读配置都靠它），`Context()` 取普通 `context.Context`（本轮可取消）。

执行结果通过 `writer`（`chat.ToolResultBlockStream`）写出，自动关联 `tool_use_id`。几个约定：

- **错误不向外返回**，用 `writer.ErrorText(err)` 写成文本，随 `tool_result` 回传给模型（退出码非零、参数缺失都走这条路）。
- **工具 panic 不会炸掉会话**：SDK 会 recover 并补一条 `工具执行异常: ...` 的 `tool_result`，保证每个 `tool_use` 都有配对的 `tool_result`（否则 Anthropic 会直接 400）。
- **想在本轮暂停等用户**（`ask_user_question` 就是这么做的）：写完后调 `writer.StopReason(chat.StopReasonUserWait)`，本轮立即收尾，用户的回答作为下一条普通消息开启新一轮。
- 工具在 `config.AddTools()`（或建会话时的 `a.GetOrCreateSession(id, agent.WithToolExecutor(...))`）阶段注册，之后没有追加口子。

### 内置工具

| 工具名 | 构造 | 说明 |
|--------|------|------|
| `execute_command` | `tools.NewCommandTool()` | 本地终端命令执行：Windows 走 `cmd /c chcp 65001`、类 Unix 走 `sh -c`；危险命令模式拦截（`rm -rf /`、`mkfs`、`dd if=`、fork 炸弹等）、30s 硬超时（不可配）、输出非 UTF-8 时按 GBK 兜底解码 |
| `task_create` / `task_update` / `task_list` / `task_get` | `tools.NewTodoTools()` | 任务追踪，对齐 Claude Code Task 模型：一次返回四个工具、共享一个 `TodoStore`；支持 `blocks` / `blocked_by` 双向依赖，`in_progress` 会被未完成的前置任务挡住 |
| `ask_user_question` | `tools.NewAskUserQuestionTool()` | LLM 向用户提问：问题随 `tool_use` 入参下发（前端解析 `args.questions` 渲染卡片），置 `user_wait` 结束本轮，用户回答作为下一条消息进入新一轮 |
| `http_request` | `tools.NewHttpRequestTool(opts...)` | HTTP 请求（GET/POST/PUT/DELETE/PATCH/HEAD/OPTIONS），适用于无命令行环境；响应体超 8KB 截断，可用 `WithHTTPTimeout` / `WithHTTPBaseURL` 定制 |
| `web_search` | `tools.NewSearchTool(chatInst)` | 调用搜索端点 `{baseUrl}/v1/search`（`WithSearchEndpoint` 可改），复用已注册 Anthropic Service 的 baseUrl / apiKey；**只对 `*anthropic.Service` 生效**，换别的 provider 会在执行时报错 |

> `web_search` 工具和 `chat.WithWebSearch(true)` 不是一回事：前者是 SDK 自己发 HTTP 请求拿结果，后者是把 Anthropic 服务端的 `web_search_20250305` 内置工具挂进请求、由 Anthropic 侧执行。

## MessageStore 接口

由主程序实现持久化策略：

```go
type MessageStore interface {
    // LoadAfter 读取 Start+Offset > since 的原始消息，按 Start 升序，最多 limit 条。
    // 返回完整历史（含已被摘要取代的旧消息），用于回放与展示。
    // 返回条数少于 limit 必须表示「已无更多数据」（调用方以此判定分页结束）。
    LoadAfter(sessionID string, since uint64, limit int) ([]*chat.Message, error)
    // Append 增量追加本批次新产生的消息，按 Start 升序、分批调用
    Append(sessionID string, messages []*chat.Message) error
    // LoadSummary 读取压缩摘要；返回 nil 表示尚未压缩（等价于分界点 0）
    LoadSummary(sessionID string) (*chat.Message, error)
    // SaveSummary 保存压缩摘要（记录分界点），不删除任何历史消息
    SaveSummary(sessionID string, summary *chat.Message) error
}
```

实现要点：

- **写进去的要能读回来。** `Append` 的批次必须能被后续 `LoadAfter` 原样读回（`Start` / `Offset` / `Content` 不变，整体按 `Start` 升序）。内存只保留「最慢的客户端还没读完」那段窗口，更早的位置靠 `LoadAfter` 回源补读——两者对不上，断线重连就会丢内容或错位。
- **短读即结束。** `LoadAfter` 返回条数少于 `limit` 必须表示已无更多数据，SDK 据此判定分页结束。
- **并发**：同一 `sessionID` 的调用已被 Store 串行化；不同 `sessionID` 会并发（共用同一个实例），实现需并发安全。
- **无重试**：`Append` 返回 error 时该批消息已移出待落盘队列、不会重投，实现应尽量在内部保证成功。

## 上下文压缩

长会话会把上下文撑爆，SDK 在每轮 `buildRequest` 之前自动压一次：**超过水位才压，按比例切割，压缩方式可插拔**。

### 什么时候压、切多少

- **水位**：上一轮 API 返回的用量（`input + output + cache`）达到 `maxContextLength` 才触发。注意用量是上一轮才知道的，所以**第一轮不压**。
- **切割**：保留末尾 `keepRatio` 比例的消息（默认 0.5，向上取整、至少留一条），前面整条丢掉。
- **切点避让**：不会把 `tool_use` / `tool_result` 这一对切成两半（留下 tool_result、丢掉 tool_use，Anthropic 会直接判 400）；落在这种消息上就把切点往后推，推到底则本轮不切。
- **分界点**：压缩后上下文变成「分界消息 + 保留段」。分界消息的 `Start` = 被切段的末尾 = 保留段第一条的 `Start`，`Start < 分界点` 的旧消息在 LLM 上下文里由它取代。**历史消息本身不删除**，回放/展示仍是完整的。

### 两种现成方案

```go
// 方案一：强行切割。不调 LLM，零延迟；代价是被丢掉的对话不可恢复。
config.Compressor(&agent.CutCompressor{},
    agent.WithMaxContextLength(100_000), // 用量超它才压
    agent.WithKeepRatio(0.5),            // 压完保留末尾一半
)

// 方案二：大模型摘要。把切掉的那段交给 LLM 压成一段摘要，不丢上下文；
// 代价是每次压缩多一次同步调用（压在 buildRequest 里，本轮首字会晚一个来回）。
config.Compressor(&agent.SummaryCompressor{
    Prompt:    "",            // 留空用内置提示词；历史里已有摘要时提示词会要求新旧合并
    ServiceID: "cheap-model", // 摘要是后台活儿，可以指到更便宜的已注册 Service
    MaxTokens: 1024,
}, agent.WithMaxContextLength(100_000), agent.WithKeepRatio(0.5))
```

`SummaryCompressor` 在调用失败或模型没吐出文本时返回 nil：切割照常生效，这一轮只记分界点、不塞消息（退化成 `CutCompressor`）——宁可丢上下文，也不让这一轮发不出去。

摘要走 `chat.CompressionBlockStream`（`SummaryCompressor` 已内置）：provider 照常写，落到事件流里的块带 `text_type=compression`，前端据此与助手正文区分（示例前端直接忽略，不混进对话）。

### 分界点持久化

分界点通过 `Summary` 接口落盘。两个接口的方法名一致，一个结构体同时满足就行（示例应用里就是 `ChatSessionService` 一手包办）：

```go
// SaveSummary 保存压缩摘要（记录分界点），不删除任何历史消息
// LoadSummary 读取压缩摘要；返回 nil 表示尚未压缩（等价于分界点 0）
```

不接持久化也能跑，只是重启后分界点丢失、旧历史会被重新加载回来再压一次。

### 自定义压缩策略

`Compressor` 只负责「把被切掉的那段压成分界消息」，切割由 `CompressorManager` 做：

```go
type MyCompressor struct{}

func (c *MyCompressor) Compress(ctx agent.Context, dropped []*chat.Message) *chat.Message {
    // dropped = 被切掉的那段历史；返回值会拼在保留段前面，Start 即新分界点
    last := dropped[len(dropped)-1]
    return &chat.Message{
        Start:   last.Start + last.Offset,
        Role:    chat.RoleUser,
        Content: chat.Blocks{chat.NewFullTextBlock("……")},
    }
}
```

**实现约束**：`Compress` 跑在 Store 的锁之外（可以先取快照再压缩），所以调 LLM、往事件流发块都行；但不要去抓 `Store` 的锁或直接改历史——切哪些、留哪些只由返回值决定。慢一点没关系，别一边持锁一边等外部调用。

## REST API

示例应用（`example/rest`）的端点。所有响应统一为 `{code, msg, data}` 信封，`code == 200` 表示成功，业务数据在 `data` 里。

```bash
# 会话：列表 / 创建 / 删除
GET    /api/chat/sessions                  # 按 updated_at 倒序
POST   /api/chat/sessions                  # {"title": "新会话"}
DELETE /api/chat/sessions/:id              # 销毁会话，并连带删除其消息与压缩摘要

# 历史事件：与 WebSocket 推送格式完全一致，可直接回放
GET    /api/chat/sessions/:id/messages?since=0

# 对话操作
POST   /api/chat/sessions/:id/messages     # {"message": "你好"} —— 发送用户消息
POST   /api/chat/sessions/:id/stop         # 停止当前轮次
PUT    /api/chat/sessions/:id/thinking     # {"level": "low"} —— off / low / medium / high
```

`since` 是起始 `start` 位置（默认 0），事件按 `Start` 升序返回，每条带 `{no, start, signal, offset, blocks}`。取值来自 `session.LoadMessagesAfter(since)`，内存与持久层统一获取；前端翻页时把它设为「上一条的 `start + offset`」，读空即到头。

## WebSocket 协议

### 连接

```
ws://localhost:8082/ws/chat/:id?start=0&level=off
```

- `:id` — 会话 ID
- `start` — 起始事件位置（断线续传时传断线前的值），默认 0
- `level` — 思考等级 `off` / `low` / `medium` / `high`，可选，等价于建连时先设一次思考等级

### 单向推送

**这条连接只推不收**：服务端不读客户端消息，发消息、停止生成、改思考等级一律走 REST。客户端断开只影响这条订阅——服务端生成照常继续、事件继续缓冲，重连带上 `start` 即可补读。

所有推送事件均为 `{no, start, signal, offset, blocks: [...]}` 格式，`blocks` 为多态内容块数组。`signal: true` 的是信号事件（用户消息状态、内部错误等），只推给在线客户端、不落历史，所以重连补读时不会重现。

```
# 用户消息生命周期（信号事件，signal=true；上一轮没结束时先 queued 排队）
{"no":0,"start":0,"signal":true,"offset":1,"blocks":[{"type":"User","block_user_type":"queued","content":[...]}]}
{"no":0,"start":1,"signal":true,"offset":1,"blocks":[{"type":"User","block_user_type":"sent","content":[...]}]}
{"no":0,"start":2,"signal":true,"offset":1,"blocks":[{"type":"User","block_user_type":"consume","content":[...]}]}

# AI 流式输出（内容块以 stop 收尾，start → delta… → stop）
{"no":0,"start":3,"offset":1,"blocks":[{"type":"start","block":{"type":"thinking"}}]}
{"no":0,"start":4,"offset":1,"blocks":[{"type":"delta","content":"让我看看..."}]}
{"no":0,"start":5,"offset":1,"blocks":[{"type":"stop"}]}
{"no":0,"start":6,"offset":1,"blocks":[{"type":"start","block":{"type":"text"}}]}
{"no":0,"start":7,"offset":1,"blocks":[{"type":"delta","content":"你好！"}]}
{"no":0,"start":8,"offset":1,"blocks":[{"type":"stop"}]}

# 工具输出（携带 tool_use_id 关联对应 tool_use）
{"no":0,"start":9,"offset":1,"blocks":[{"type":"start","block":{"type":"text","tool_use_id":"call_00"}}]}
{"no":0,"start":10,"offset":1,"blocks":[{"type":"delta","content":"OS Name: ..."}]}
{"no":0,"start":11,"offset":1,"blocks":[{"type":"stop"}]}

# AskUser 提问：问题随 ask_user_question 的 tool_use 入参流式到达（前端解析 args.questions 渲染卡片），本轮随即结束
{"no":0,"start":12,"offset":1,"blocks":[{"type":"start","block":{"type":"tool_use","id":"call_00","name":"ask_user_question"}}]}
{"no":0,"start":13,"offset":1,"blocks":[{"type":"delta","content":"{\"questions\":[...]}"}]}
{"no":0,"start":14,"offset":1,"blocks":[{"type":"stop"}]}

# 本轮结束
{"no":0,"start":15,"offset":1,"blocks":[{"type":"done"}]}

# 错误（信号事件）
{"no":0,"start":16,"signal":true,"offset":1,"blocks":[{"type":"error","text":"network timeout"}]}
```

> **块形态**：实时流以 `start` + `delta` + `stop` 增量块推送（每个内容块以 `stop` 收尾）；连接后从持久化回放的历史消息返回完整块——文本/思考为完整 `text` / `thinking` 块，工具调用为 `tool_use`（含 `input`）+ `tool_result`（含完整 `content`），token 用量为 `message_start` / `message_delta`。客户端需同时处理增量与完整两种形态。

前端采用 **send/display 分离**：消息通过 REST API 发送，收到 `User` 块（`block_user_type=consume`）后才将用户消息追加到对话框并启动流式适配器。

## 运行示例

```bash
# 1. 后端：复制配置模板，填 LLM API Key 与数据库
cd example
cp application.example.yml application.yml
go run main.go          # 监听 application.yml 的 web.server.port

# 2. 前端：把 .env 指到同一个端口
cd view
cp .env.example .env    # VITE_API_BASE / VITE_WS_BASE 与上面的端口保持一致
pnpm install
pnpm dev                # → http://localhost:5175
```

前端 dev server 端口在 `example/view/vite.config.ts` 里写死为 5175；`/api` 与 `/ws` 分别代理到 `.env` 里的 `VITE_API_BASE` / `VITE_WS_BASE`。

### 示例应用的接口

发送消息走 REST，接收事件流走 WebSocket（send/display 分离），完整端点见上面的 [REST API](#rest-api) 与 [WebSocket 协议](#websocket-协议) 两节。

## 配置选项

```go
config := agent.NewConfig()

// ChatOption 配置 LLM 请求参数（含系统提示词）
config.ChatOption(
    chat.WithModel("claude-sonnet-5"),
    chat.WithMaxTokens(8192),                    // 默认 4096
    chat.WithThinking(chat.ThinkingHigh),        // 思考预算：low 8192 / medium 16384 / high 32768
    chat.WithSystemPrompt("你是一个智能助手。"),
    chat.WithWebSearch(true),   // 启用 Anthropic 服务端内置 web_search（可选）
    chat.WithUserId("user-1"),  // 业务侧用户标识，可从 Context.GetConfig() 取回
)

// AddTools 注册工具；AddLifecycle / OnXxx 注册生命周期钩子
config.AddTools(tools.NewCommandTool())

// 超时配置（秒），缺省 600 / 300；填 0 关闭
config.SessionTimeout(600)  // 会话空闲超时，到期自动销毁
config.ClientTimeout(300)   // 客户端空闲超时

// MessageStore 设置持久化（实现 MessageStore 接口）
config.MessageStore(myMessageStore)

// Compressor 设置上下文压缩策略（可选）：压缩器 + 水位/保留比例
// 不调 LLM 就 &agent.CutCompressor{}；要摘要就 &agent.SummaryCompressor{...}
// 缺省水位 100_000、保留比例 0.5
config.Compressor(&agent.CutCompressor{},
    agent.WithMaxContextLength(100_000), // 用量超过它才压
    agent.WithKeepRatio(0.5),            // 压完保留末尾一半
)

// RegisterChat 注册 LLM 提供商（可注册多个，首个为默认）
// 自定义 provider 实现 chat.Service：ChatWithStream(ctx, *Messages, chat.BlockWriter) + ID()
// —— 写的是 BlockWriter 接口，不绑具体流实现
config.RegisterChat(anthropic.NewService("provider-id", baseUrl, apiKey, model))

// CreateServer 基于配置创建 Server（内部启动后台超时清理循环）
a := config.CreateServer(context.Background())
```

几个容易踩的点：

- **`anthropic.NewService` 的 model 参数优先于 `chat.WithModel`。** 请求时按 `chat.Combine(会话配置, Service 自身的配置)` 合并，后者在后、覆盖前者，而 `NewService` 无论传什么都写了 model 键。所以多提供商要用**运行时**选（`session.UpdateChatOption(chat.WithId("provider-id"))`），而不是用 `WithModel` 换模型。
- **会话级 `Option` 只在会话创建时生效**：`a.GetOrCreateSession(id, agent.WithChatOption(...), agent.WithToolExecutor(...))`，会话已存在时传了也白传。要改已存在会话用 `session.UpdateChatOption(...)`。
- **`CreateServer` 会 `Copy()` 一份配置**，之后改原 `Config` 不影响已创建的 `Server`。
- **日志走 `log.SetLogger(l log.Logger)`** 注入（实现 `Debug/Info/Warn/Error` 四个方法即可），默认是 slog 打到 stderr、Debug 级别；SDK 只做门面，不带轮转。

## License

[MIT](LICENSE)
