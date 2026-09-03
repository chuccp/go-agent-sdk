# go-agent-sdk

一个轻量级 Go AI Agent SDK，提供流式对话、工具调用、历史持久化和断线续传能力，单进程即可运行完整的 Agent 服务。

## 核心特性

- **多客户端订阅** — 同一会话可被多个 Client 同时订阅（多标签页），每个 Client 通过 `start` 独立追踪读取进度，互不阻塞
- **断线续传** — 消息自带事件流区间 `[Start, Start+Offset)`，客户端凭一个 `start` 值即可精确续读，无需外部 broker
- **Client 无状态** — `Client` 断开即丢弃，不保留任何会话状态，重连只是换一个 transport
- **流式对话** — SSE 流式输出，实时推送 thinking / text 增量
- **多轮工具调用** — 标准 tool_use → tool_result 循环，兼容 Anthropic Messages API
- **历史持久化** — 内存 + DB 双层存储，增量追加，懒加载
- **会话超时** — 支持 Session / Client 级别的空闲超时自动销毁
- **多提供商** — Provider Registry 支持注册多个 LLM 后端，运行时选择
- **Block 多态** — content 为接口数组，支持 text / thinking / image / tool_use / tool_result / custom_text

## 设计优势

### 低成本断线续传（无 broker）

断线续传没有走「消息队列 / 消费组 / ack」的常规路线，而是把它退化成一个**单调递增序号 + 区间判断**的纯内存问题：

- 每条消息携带事件区间 `[Start, Start+Offset)`，与全局单调 `seq` 对齐；
- 客户端只持有一个 `uint64` 的 `start` 游标，重连 = 带 `start` 重新挂上；
- `Client` 无状态，断开即丢，服务端不感知也不关心客户端断过。

因此**不依赖 Kafka / Redis / broker，单进程即可**，运维成本为零，也规避了「谁负责 ack」的分布式难题。

### 生成与传输解耦

客户端断开不会中断服务端生成：`Session`/`Loop`/`Transfer` 照常运行，事件继续进入活跃缓冲区 `entries`，客户端重连后凭 `start` 一次性补读积压事件，像什么都没发生过。

## 架构概览

```
┌───────────────────────────────────────────────────────────────┐
│  Agent                                                        │
│  ├── ProviderRegistry (多 LLM 后端)                            │
│  ├── ToolExecutors (工具注册)                                   │
│  └── Sessions map[id] → Session                               │
│                  └── Session                                   │
│                       ├── SessionContext (状态中心)              │
│                       │    ├── Loop (会话编排器, runLock 保护)   │
│                       │    │    ├── inbox (消息队列)            │
│                       │    │    ├── HandleMessage (入队+启动)    │
│                       │    │    └── do → loop → chatWithStream  │
│                       │    │         ├── executeTools           │
│                       │    │         └── appendMessage          │
│                       │    └── Transfer (事件中转层)             │
│                       │         ├── entries (活跃事件缓冲区)     │
│                       │         ├── chatClients (订阅列表)       │
│                       │         └── Store (消息存储)             │
│                       ├── checkTimeout (会话超时守护)            │
│                       └── Client[] (轻量订阅句柄)                │
└───────────────────────────────────────────────────────────────┘
```

## 包结构

```
go-agent-sdk/
├── agent/          # Agent 层：Agent, Session, Client, Loop,
│                   #   Transfer, ToolExecutor, Turn, Store, MessageStore
├── api/chat/       # LLM 提供商适配
│   └── anthropic/  # Anthropic 协议实现（Service, Request, ThinkingConfig）
├── chat/           # 协议层：Block, Event, Message, Config, Service, Option
├── tools/          # 内置工具：Command, Todo, AskUserQuestion（平台适配）
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
	config.RegisterChat(anthropic.NewService("my-provider", baseUrl, apiKey, "claude-sonnet-4-6"))

	// 3. 注册工具（可选）
	config.AddTools(tools.NewCommandTool())

	// 4. 设置持久化（可选，实现 MessageStore 接口）
	config.HistoryStore(myMessageStore)

	// 5. 设置超时（可选，秒）
	config.SessionTimeout(600) // 会话空闲超时
	config.ClientTimeout(300)  // 客户端空闲超时

	// 6. 基于配置创建 Agent（内部启动后台超时清理循环）
	a := config.CreateAgent(context.Background())

	// 7. 获取会话 + 创建客户端
	session := a.GetOrCreateSession("session-1")
	client := session.CreateClient(context.Background(), 0)

	// 8. 发送消息
	client.WriteText("你好，帮我查看当前目录")

	// 9. 读取事件流（事件按 Start 升序返回，Event.Blocks 按 type 字段多态分发）
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

### Block（内容块）

消息的 `Content` 是 `Blocks`（`[]Block` 接口数组），支持多态 JSON 序列化。每个具体块都带 `Type BlockType` 字段，反序列化时按 `type` 分发还原（`Blocks` 实现自定义 `UnmarshalJSON`），历史持久化加载后可无损往返。

```go
type Block interface {
    ForContext() bool       // 声明该块是否进入 LLM 上下文
    GetStart() uint64       // 该块在事件流中的序号（供 relay 按 block 粒度去重）
    SetStart(uint64)
    GetType() BlockType
}

// 具体类型（均嵌入 BaseBlock）
TextBlock          { Text string; TextType TextType; ToolUseId string }
ThinkingBlock      { Thinking string }
ImageBlock         { Source *ImageSource }
ToolUseBlock       { ID, Name string; Input *value.Object }
ToolResultBlock    { ToolUseID string; Content Blocks }
CustomTextBlock    { Text string; TextType TextType; ToolUseId string }  // 业务扩展（不进上下文）
MessageStartBlock  { Usage *Usage }
MessageDeltaBlock  { Usage *Usage }
StartBlock         { Block UseDeltaBlock }                              // 流式块起始标记
DeltaBlock         { Content string }                                  // 流式增量
DoneBlock          { Usage *Usage }                                    // 本轮结束（携带 token 用量）
UserBlock          { BlockUserType string; ID uint64; Content Blocks } // 用户消息状态
ErrorBlock         { Text string }
```

### 事件流与断线续传

每条 Message 携带事件区间 `[Start, Start+Offset)`，标记它产出了哪些事件，区间与全局单调递增的事件序号 `seq` 对齐。客户端持有一个绝对偏移 `start` 即可从活跃事件缓冲区（`entries`）增量续读。事件按 **Start 升序** 返回。

`doneManifest` 追踪每轮结束点，当所有客户端均已消费到某个结束点时，旧事件被裁掉（`reset`），待保存历史迁入持久层（`save`）。`start` 早于缓冲区头部时自动钳制；服务重启后从历史恢复 `seq`，新事件无缝接续。

多个 Client 同时订阅时，每个 Client 通过各自的 `start` 独立推进读取进度，互不阻塞。

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
| `AskUserQuestionTool` | `tools/ask_user_question.go` | LLM 向用户提问：推送 `ask_user` 事件并置 `user_wait` 后返回，用户回答作为下一条消息 |

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

通过 `agent.History(sessionId, since)` → `session.LoadMessagesAfter(since)` 从 agent 内存 + 持久化统一获取。

## WebSocket 协议

### 客户端 → 服务端

```json
// 初始化/续接会话（幂等，可在每次发送前调用）
{"type": "create", "session_id": 1, "start": 0}

// 发送消息（thinking 可选：off / low / medium / high）
{"type": "chat", "message": "你好", "thinking": "low"}

// 停止当前生成
{"type": "stop"}
```

### 服务端 → 客户端

所有推送事件均为 `{no, start, offset, blocks: [...]}` 格式，`blocks` 为多态内容块数组。

```
# 用户消息生命周期
{"no":0,"start":0,"offset":1,"blocks":[{"type":"User","block_user_type":"sent","content":[...]}]}
{"no":0,"start":1,"offset":1,"blocks":[{"type":"User","block_user_type":"consume","content":[...]}]}

# AI 流式输出
{"no":0,"start":2,"offset":1,"blocks":[{"type":"start","block":{"type":"thinking"}}]}
{"no":0,"start":3,"offset":1,"blocks":[{"type":"delta","content":"让我看看..."}]}
{"no":0,"start":4,"offset":1,"blocks":[{"type":"start","block":{"type":"text"}}]}
{"no":0,"start":5,"offset":1,"blocks":[{"type":"delta","content":"你好！"}]}

# 工具输出（携带 tool_use_id 关联对应 tool_use）
{"no":0,"start":6,"offset":1,"blocks":[{"type":"start","block":{"type":"text","tool_use_id":"call_00"}}]}
{"no":0,"start":7,"offset":1,"blocks":[{"type":"delta","content":"OS Name: ..."}]}

# AskUser 提问
{"no":0,"start":8,"offset":1,"blocks":[{"type":"custom_text","text_type":"ask_user","text":"[...]"}]}

# 本轮结束
{"no":0,"start":9,"offset":1,"blocks":[{"type":"done"}]}

# 错误
{"no":0,"start":10,"offset":1,"blocks":[{"type":"error","text":"network timeout"}]}
```

> **块形态**：实时流以 `start` + `delta` 增量块推送；`create` 后从持久化回放的历史消息返回完整块——文本/思考为完整 `text` / `thinking` 块，工具调用为 `tool_use`（含 `input`）+ `tool_result`（含完整 `content`），token 用量为 `message_start` / `message_delta`。客户端需同时处理增量与完整两种形态。

前端采用 **send/display 分离**：消息通过 WebSocket 直接发送，收到 `User` 块（`block_user_type=consume`）后才将用户消息追加到对话框并启动流式适配器。

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

## 配置选项

```go
config := agent.NewConfig()

// ChatOption 配置 LLM 请求参数
config.ChatOption(
    chat.WithModel("claude-opus-4-7"),
    chat.WithMaxTokens(8192),
    chat.WithThinking(chat.ThinkingHigh),
)

// SystemPrompt 设置全局系统提示词
config.SystemPrompt("你是一个智能助手。")

// 超时配置（秒）
config.SessionTimeout(600)  // 会话空闲超时，到期自动销毁
config.ClientTimeout(300)   // 客户端空闲超时

// HistoryStore 设置持久化（实现 MessageStore 接口）
config.HistoryStore(myMessageStore)

// Compressor 设置上下文压缩策略
config.Compressor(myCompressor)

// CreateAgent 基于配置创建 Agent（内部启动后台超时清理循环）
a := config.CreateAgent(context.Background())
```

## License

[MIT](LICENSE)
