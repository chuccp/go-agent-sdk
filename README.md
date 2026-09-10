# go-agent-sdk

一个轻量级 Go AI Agent SDK，提供流式对话、工具调用、历史持久化和断线续传能力，单进程即可运行完整的 Agent 服务。

## 核心特性

- **多客户端订阅** — 同一会话可被多个 Client 同时订阅（多标签页），每个 Client 通过 `start` 独立追踪读取进度，互不阻塞
- **断线续传** — 消息自带事件流区间 `[Start, Start+Offset)`，客户端凭一个 `start` 值即可精确续读，无需外部 broker
- **Client 无状态** — `Client` 断开即丢弃，不保留任何会话状态，重连只是换一个 transport
- **最新位订阅** — `Session.LastClient()` 无需传入 `start`，自动从当前最新事件位置开始订阅
- **消息发送与接收分离** — Session 负责发送消息，Client 仅负责接收事件流
- **流式对话** — WebSocket 流式输出，实时推送 thinking / text 增量
- **多轮工具调用** — 标准 tool_use → tool_result 循环，兼容 Anthropic Messages API
- **历史持久化** — 内存 + DB 双层存储，增量追加，懒加载
- **会话超时** — 支持 Session / Client 级别的空闲超时自动销毁
- **生命周期钩子** — 会话创建 / 消息到达 / 轮次结束 / 会话销毁四类回调
- **多提供商** — ServiceStore 支持注册多个 LLM 后端，运行时选择
- **Block 多态** — content 为接口数组，支持 text / thinking / image / tool_use / tool_result / custom_text / server_tool_use / stop

## 架构概览

```
┌───────────────────────────────────────────────────────────────┐
│  Server (Agent 管理器)                                         │
│  ├── Config (构建期配置)                                        │
│  └── Sessions map[id] → Session                               │
│                  └── Session                                   │
│                       ├── SessionContext (状态中心, 实现 Context)
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

`Context` 是会话生命周期的核心接口，由 `SessionContext` 实现，提供会话状态访问和事件发送能力：

```go
type Context interface {
    context.Context
    SessionId() string
    GetChat() *chat.Chat
    SubAgentStore() *Store
    AgentStore() *Store

    SendBlock(no uint64, block chat.Block) uint64       // 发送事件块
    SendSignalBlock(no uint64, block chat.Block) uint64 // 发送信号事件块
    GetTransferStart() uint64                           // 获取当前传输起始位置

    AppendMainAssistantMessage(blocks *chat.BlockGroup)
    AppendMainUserMessage(blocks *chat.BlockGroup)
}
```

`SendBlock` 是事件发送的统一入口，通过 `Transfer` 实现，将事件分发给所有订阅的客户端。`SendSignalBlock` 用于发送信号事件（如用户消息状态变更），不会持久化。

## 包结构

```
go-agent-sdk/
├── agent/          # Agent 层：Server, Agent, Session, Client,
│                   #   Transfer, ToolExecutor, Turn, Store, MessageStore
├── api/chat/       # LLM 提供商适配
│   └── anthropic/  # Anthropic 协议实现（Service, Request, ThinkingConfig）
├── chat/           # 协议层：Block, Event, Message, Config, Service, Option
├── tools/          # 内置工具：Command, Todo, AskUserQuestion, HttpRequest, Search（平台适配）
├── util/           # 通用工具：SliceArray, SliceQueue, Queue, TimeWheel
├── value/          # 动态值类型：Object, Array, Value（支持命名类型）
└── example/        # 完整示例应用（Go 后端 + React 前端）
    ├── entity/     # DB 实体 + WebSocket 消息定义
    ├── model/      # GORM 模型
    ├── rest/       # REST + WebSocket 路由
    ├── server/     # Agent 服务封装
    ├── service/    # 业务逻辑（MessageStore 实现）
    └── view/       # React 前端（@assistant-ui/react）
```

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
		chat.WithModel("claude-sonnet-4-6"),
		chat.WithMaxTokens(4096),
		chat.WithThinking(chat.ThinkingLow),
	)

	// 2. 注册 LLM 提供商（Service 通过 ID() 标识自身，首个注册的为默认）
	config.RegisterChat(anthropic.NewService("my-provider", "https://api.anthropic.com", "your-api-key", "claude-sonnet-4-6"))

	// 3. 注册工具（可选）
	config.AddTools(tools.NewCommandTool())

	// 4. 设置持久化（可选，实现 MessageStore 接口）
	// config.HistoryStore(myMessageStore)

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
		events, err := client.ReadEvents()
		if err != nil {
			fmt.Println("error:", err)
			break
		}
		if events == nil {
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

`Session` 是会话的容器，包含 `SessionContext`（实现 `Context` 接口）和多个 `Client`。`Context` 提供：

- **会话状态访问** — `GetChat()`, `AgentStore()`, `SubAgentStore()`
- **事件发送** — `SendBlock(no, block)` 通过 `Transfer` 分发事件；`SendSignalBlock(no, block)` 发送信号事件
- **传输状态** — `GetTransferStart()` 获取当前传输起始位置
- **历史管理** — `AppendMainAssistantMessage()`, `AppendMainUserMessage()`

`Agent`（会话编排器）通过 `Context` 与会话交互，不直接持有 `Transfer` 或 `Store`，实现了关注点分离。

`Session` 自身的门面方法：

```go
session.ID()                // 会话 ID
session.Client(ctx, start)  // 从绝对位置 start 订阅事件流（断线续传时传入断线前的 start）
session.LastClient(ctx)     // 从当前最新位置订阅：不补历史，只看之后的新事件
session.Stop()              // 停止当前轮次（只对单轮生效），后续用户消息不受影响
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

### Done 回调

`Agent` 支持通过 `Done` 结构体在轮次结束后执行自定义逻辑：

```go
// HandleDoneMessage 创建一个 Done 对象，用于在轮次结束后执行回调
done := session.GetAgent().HandleDoneMessage(blocks)
done.Done(func() {
    // 轮次结束后的自定义逻辑
    fmt.Println("round completed")
})
```

`Done.Done(f)` 注册回调函数 `f`，在当前轮次完成（发送 `DoneBlock` 并持久化）后自动执行。

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
UserBlock          { BlockUserType string; ID uint64; Content Blocks } // 用户消息状态
ErrorBlock         { Text string }
```

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

事件发送通过 `Transfer.SendBlock` 统一处理，`Agent.SendBlock` 和 `SessionContext.SendBlock` 均委托给它。这种方式将事件生成与传输解耦，确保多客户端订阅时的一致性。

`doneManifest` 追踪每轮结束点，当所有客户端均已消费到某个结束点时，旧事件被裁掉（`reset`），待保存历史迁入持久层（`save`）。`start` 早于缓冲区头部时自动钳制；服务重启后从历史恢复 `seq`，新事件无缝接续。

多个 Client 同时订阅时，每个 Client 通过各自的 `start` 独立推进读取进度，互不阻塞。不关心历史、只看新事件的场景可用 `Session.LastClient()`，自动从当前最新位置开始订阅。

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

`Turn` 是每次工具执行的载体，提供 `Args()` 获取工具入参、`Context()` 获取会话上下文。
执行结果通过 `writer`（`chat.ToolResultBlockStream`）写出，自动关联 `tool_use_id`。

### 内置工具

| 工具 | 文件 | 说明 |
|------|------|------|
| `CommandTool` | `tools/command.go` | 本地终端命令执行，带危险命令拦截 + 30s 超时 |
| `TodoTool` | `tools/todo.go` | 任务追踪（对齐 Claude Code Task 模型），支持依赖关系 |
| `AskUserQuestionTool` | `tools/ask_user_question.go` | LLM 向用户提问：问题随 tool_use 入参下发，置 `user_wait` 结束本轮，用户回答作为下一条消息 |
| `HttpRequestTool` | `tools/http_request.go` | HTTP 请求（GET/POST/PUT/DELETE/PATCH），适用于无命令行环境，响应超 8KB 截断 |
| `SearchTool` | `tools/search.go` | 联网搜索 `web_search`，复用已注册 Anthropic Service 的 baseUrl / apiKey（`tools.NewSearchTool(chatInst)`） |

## MessageStore 接口

由主程序实现持久化策略：

```go
type MessageStore interface {
    // LoadAfter 读取 Start >= since 的原始消息，按 Start 升序，最多 limit 条
    LoadAfter(sessionID string, since uint64, limit int) ([]*chat.Message, error)
    // Append 增量追加本批次新产生的消息
    Append(sessionID string, messages []*chat.Message) error
    // LoadSummary 读取压缩摘要；返回 nil 表示尚未压缩
    LoadSummary(sessionID string) (*chat.Message, error)
    // SaveSummary 保存压缩摘要
    SaveSummary(sessionID string, summary *chat.Message) error
}
```

## REST API

### 消息历史

```
GET /api/chat/sessions/:id/messages?since=0
```

- `since` — 起始 `start` 位置（返回 `Start >= since` 的事件），默认 0

通过 `session.LoadMessagesAfter(since)` 从 agent 内存 + 持久化统一获取。

## WebSocket 协议

### 连接

WebSocket 连接通过 URL 参数传递会话 ID 和起始位置：

```
ws://localhost:19009/ws/chat/:id?start=0
```

- `:id` — 会话 ID
- `start` — 起始事件位置（用于断线续传），默认 0

### 客户端 → 服务端

```json
// 发送消息
{"type": "chat", "message": "你好"}

// 停止当前生成
{"type": "stop"}
```

### 服务端 → 客户端

所有推送事件均为 `{no, start, offset, blocks: [...]}` 格式，`blocks` 为多态内容块数组。

```
# 用户消息生命周期
{"no":0,"start":0,"offset":1,"blocks":[{"type":"User","block_user_type":"sent","content":[...]}]}
{"no":0,"start":1,"offset":1,"blocks":[{"type":"User","block_user_type":"consume","content":[...]}]}

# AI 流式输出（内容块以 stop 收尾，start → delta… → stop）
{"no":0,"start":2,"offset":1,"blocks":[{"type":"start","block":{"type":"thinking"}}]}
{"no":0,"start":3,"offset":1,"blocks":[{"type":"delta","content":"让我看看..."}]}
{"no":0,"start":4,"offset":1,"blocks":[{"type":"stop"}]}
{"no":0,"start":5,"offset":1,"blocks":[{"type":"start","block":{"type":"text"}}]}
{"no":0,"start":6,"offset":1,"blocks":[{"type":"delta","content":"你好！"}]}
{"no":0,"start":7,"offset":1,"blocks":[{"type":"stop"}]}

# 工具输出（携带 tool_use_id 关联对应 tool_use）
{"no":0,"start":8,"offset":1,"blocks":[{"type":"start","block":{"type":"text","tool_use_id":"call_00"}}]}
{"no":0,"start":9,"offset":1,"blocks":[{"type":"delta","content":"OS Name: ..."}]}
{"no":0,"start":10,"offset":1,"blocks":[{"type":"stop"}]}

# AskUser 提问：问题随 ask_user_question 的 tool_use 入参流式到达（前端解析 args.questions 渲染卡片），本轮随即结束
{"no":0,"start":11,"offset":1,"blocks":[{"type":"start","block":{"type":"tool_use","id":"call_00","name":"ask_user_question"}}]}
{"no":0,"start":12,"offset":1,"blocks":[{"type":"delta","content":"{\"questions\":[...]}"}]}
{"no":0,"start":13,"offset":1,"blocks":[{"type":"stop"}]}

# 本轮结束
{"no":0,"start":14,"offset":1,"blocks":[{"type":"done"}]}

# 错误
{"no":0,"start":15,"offset":1,"blocks":[{"type":"error","text":"network timeout"}]}
```

> **块形态**：实时流以 `start` + `delta` + `stop` 增量块推送（每个内容块以 `stop` 收尾）；连接后从持久化回放的历史消息返回完整块——文本/思考为完整 `text` / `thinking` 块，工具调用为 `tool_use`（含 `input`）+ `tool_result`（含完整 `content`），token 用量为 `message_start` / `message_delta`。客户端需同时处理增量与完整两种形态。

前端采用 **send/display 分离**：消息通过 REST API 发送，收到 `User` 块（`block_user_type=consume`）后才将用户消息追加到对话框并启动流式适配器。

## 运行示例

```bash
# 后端（需要配置 application.yml 中的 LLM API Key）
cd example
go run main.go
# → http://localhost:19009

# 前端
cd example/view
pnpm install
pnpm dev
# → http://localhost:5173
```

### REST API

示例应用还提供了 REST API 用于发送消息、停止生成和设置思考程度：

```bash
# 发送消息
POST /api/chat/sessions/:id/messages
Content-Type: application/json
{"message": "你好"}

# 停止生成
POST /api/chat/sessions/:id/stop

# 设置思考程度（off / low / medium / high）
PUT /api/chat/sessions/:id/thinking
Content-Type: application/json
{"level": "low"}
```

## 配置选项

```go
config := agent.NewConfig()

// ChatOption 配置 LLM 请求参数（含系统提示词）
config.ChatOption(
    chat.WithModel("claude-opus-4-7"),
    chat.WithMaxTokens(8192),
    chat.WithThinking(chat.ThinkingHigh),
    chat.WithSystemPrompt("你是一个智能助手。"),
)

// 超时配置（秒）
config.SessionTimeout(600)  // 会话空闲超时，到期自动销毁
config.ClientTimeout(300)   // 客户端空闲超时

// HistoryStore 设置持久化（实现 MessageStore 接口）
config.HistoryStore(myMessageStore)

// Compressor 设置上下文压缩策略（可选）
config.Compressor(myCompressor)

// RegisterChat 注册 LLM 提供商（可注册多个，首个为默认）
config.RegisterChat(anthropic.NewService("provider-id", baseUrl, apiKey, model))

// CreateServer 基于配置创建 Server（内部启动后台超时清理循环）
a := config.CreateServer(context.Background())
```

## License

[MIT](LICENSE)
