# go-agent-sdk

一个轻量级 Go AI Agent SDK，提供流式对话、工具调用、历史持久化和断线续传能力，单进程即可运行完整的 Agent 服务。

## 核心特性

- **多客户端订阅** — 同一会话可被多个 Client 同时订阅（多标签页），每个 Client 通过 Position 独立追踪读取进度，互不阻塞
- **断线续传** — 消息自带事件流区间 `[Start, Start+Offset)`，客户端凭一个 `start` 值即可精确续读，无需外部 broker
- **Client 无状态** — `Client` 断开即丢弃，不保留任何会话状态，重连只是换一个 transport
- **流式对话** — SSE 流式输出，实时推送 thinking / text 增量
- **多轮工具调用** — 标准 tool_use → tool_result 循环，兼容 Anthropic Messages API
- **历史持久化** — 内存 + DB 双层存储，增量追加，懒加载
- **多提供商** — Provider Registry 支持注册多个 LLM 后端，运行时选择
- **Block 多态** — content 为接口数组，支持 text / thinking / image / tool_use / tool_result

## 架构概览

```
┌───────────────────────────────────────────────────────────────┐
│  Agent                                                        │
│  ├── ProviderRegistry (多 LLM 后端)                            │
│  ├── ToolExecutors (工具注册)                                  │
│  └── map[id] → session                                        │
│                  └── SessionContext (状态中心)                  │
│                       ├── inbox (消息队列, runMutex 保护)       │
│                       ├── messageProcessor (会话编排器)         │
│                       │    ├── MessageFilter 链 (拦截/过滤消息)  │
│                       │    ├── coreMessageFilter (链最内层)     │
│                       │    │    ├── 入队 + 启动主循环            │
│                       │    │    └── executeRound → doLoop      │
│                       │    └── 工具级过滤器 (ask_user 等)       │
│                       ├── Store                               │
│                       │    ├── entries (活跃事件缓冲区)          │
│                       │    ├── history (全量消息, 持久化)        │
│                       │    ├── positions (客户端读取位置列表)    │
│                       │    └── base (Reset 推进, seq 单调递增)   │
│                       └── Client[] (轻量订阅句柄)               │
└───────────────────────────────────────────────────────────────┘
```

## 包结构

```
go-agent-sdk/
├── agent/          # Agent 层：Agent, SessionContext, Client, messageProcessor,
│                   #   MessageFilter 链, Tool, Turn, Store
├── chat/           # 协议层：Block, Event, Message, Request/Response, Store, Position, Provider
├── tools/          # 内置工具：Command, Todo, AskUserQuestion（平台适配）
├── util/           # 通用工具：SliceArray, SliceQueue, Queue, TimeWheel
└── example/        # 完整示例应用（Go 后端 + React 前端）
    ├── api/chat/   # LLM 提供商适配（anthropic 协议）
    ├── entity/     # DB 实体 + WebSocket 消息定义
    ├── model/      # GORM 模型
    ├── rest/       # REST + WebSocket 路由
    ├── server/     # Agent 服务封装
    ├── service/    # 业务逻辑（HistoryStore 实现）
    └── view/       # React 前端（@assistant-ui/react）
```

## 快速开始

```go
package main

import (
    "github.com/chuccp/go-agent-sdk/agent"
    "github.com/chuccp/go-agent-sdk/chat"
    "github.com/chuccp/go-agent-sdk/tools"
)

func main() {
    // 1. 创建 Agent
    agent := agent.NewAgent(
        chat.WithModel("deepseek-v4-flash"),
        chat.WithMaxTokens(4096),
        chat.WithThinking(chat.ThinkingLow),
    )

    // 2. 注册 LLM 提供商
    agent.RegisterChat("my-provider", myChatService, true)

    // 3. 注册工具（可选）
    agent.AddTools(tools.NewCommandTool())

    // 4. 设置持久化（可选）
    agent.SetHistoryStore(myHistoryStore)

    // 5. 获取客户端（session_id + 起始偏移）
    client, _ := agent.GetClient("session-1", 0)

    // 6. 发送消息
    client.SendText("你好，帮我查看当前目录")

    // 7. 读取事件流
    for {
        event := client.ReadEvent()
        if event == nil {
            break
        }
        switch event.EventType {
        case "chunk":
            print(event.Content) // 流式文本
        case "thinking":
            print(event.Content) // 思考链
        case "tool_execution":
            println("工具执行:", event.Message, event.Content)
        case "done":
            return
        }
    }
}
```

## 核心概念

### Block（内容块）

消息的 `Content` 是 `[]Block` 接口数组，支持多态 JSON 序列化：

```go
type Block interface { Type() ContentType }

// 具体类型
TextBlock       { Text string }
ThinkingBlock   { Thinking string }
ImageBlock      { Source *ImageSource }
ToolUseBlock    { ID, Name string; Input any }
ToolResultBlock { ToolUseID string; Content any }
```

### 事件流与断线续传

每条 Message 携带 `[Start, Offset)` 区间，标记它产出了哪些事件。客户端持有一个 `start` 值即可精确续读——`start < base` 表示事件已归档，从 history 恢复；`start >= base` 表示事件仍在活跃缓冲区，从 entries 增量读取。

多个 Client 同时订阅时，每个 Client 通过 Position 独立推进读取进度，互不阻塞。

## 工具系统

实现 `ToolExecutor` 接口即可注册自定义工具：

```go
type ToolExecutor interface {
    Definition() *chat.ToolFunction                     // 工具元数据（发给 LLM）
    Name() string                                       // 工具唯一名称
    Execute(turn *Turn, writer chat.StreamWriter) error // 执行逻辑
}
```

`Turn` 是每次工具执行的载体，提供 `Args()` 获取工具入参、`Context()` 获取会话上下文（`SessionContext`）。
执行结果通过 `writer` 流式写出，支持逐块输出内容。

### 内置工具

| 工具 | 文件 | 说明 |
|------|------|------|
| `CommandTool` | `tools/command.go` | 本地终端命令执行，带危险命令拦截 + GUI 程序自动 `start` + 30s 超时。`command_unix.go` / `command_windows.go` 提供平台适配 |
| `TodoTool` | `tools/todo.go` | 任务追踪（对齐 Claude Code Task 模型），支持 pending/in_progress/completed 状态，通过 `TodoStore` 跨会话共享 |
| `AskUserQuestionTool` | `tools/ask_user_question.go` | LLM 向用户提问澄清问题，通过 `AskUserQuestionResponse` 收集答案，实现 `MessageFilter` 拦截用户回答 |

### 消息过滤器链

工具可以实现 `MessageFilter` 接口注册到消息过滤器链，拦截和消费用户消息：

```go
type MessageFilter interface {
    HandleRevMessage(chain MessageFilterChain, msg *QueuedMessage) error
}
```

过滤器按注册顺序执行，链的最内层是 `coreMessageFilter`（负责入队并驱动主循环）。
`AskUserQuestionTool` 即通过过滤器链在等待用户回答时拦截消息并消费。

## HistoryStore 接口

由主程序实现持久化策略：

```go
type HistoryStore interface {
    LoadHistory(sessionID string) ([]Message, error)     // 懒加载历史
    AppendMessages(sessionID string, messages []Message) error // 增量追加
}
```

## WebSocket 协议

### 客户端 → 服务端

```json
// 初始化/续接会话（幂等，可在每次发送前调用）
{"type": "create", "session_id": 1, "start": 0}

// 发送消息（thinking 可选：off / low / medium / high）
{"type": "chat", "message": "你好", "thinking": "low"}

// 停止当前生成
{"type": "stop"}

// 心跳
{"type": "ping"}
{"type": "pong"}
```

### 服务端 → 客户端

所有推送事件均为 `ClientEvent` JSON，公共字段为 `seq`, `source`, `type`, `session_id`，其余字段按事件类型出现。

```
# 消息生命周期（send/display 分离）
{"seq": 1, "source": "client", "type": "message_sent",     "message_id": 1, "content": "你好", "session_id": "1"}
{"seq": 2, "source": "client", "type": "message_queued",   "message_id": 1, "content": "你好", "session_id": "1"}
{"seq": 3, "source": "client", "type": "message_consumed", "message_id": 1, "content": "你好", "session_id": "1"}

# AI 流式输出
{"seq": 4, "source": "ai",     "type": "thinking",        "content": "让我看看当前目录...",           "session_id": "1"}
{"seq": 5, "source": "ai",     "type": "chunk",           "content": "你好！当前目录是：",            "session_id": "1"}
{"seq": 6, "source": "ai",     "type": "chunk",           "content": "\n\nproject/",                  "session_id": "1"}

# 工具执行（如果有 tool_use）
{"seq": 7, "source": "ai",     "type": "tool_execution",  "message": "execute_command", "content": "...", "session_id": "1"}

# LLM 向用户提问（AskUserQuestion 工具）
{"seq": 8, "source": "ai",     "type": "ask_user",        "questions": [...],                        "session_id": "1"}

# 本轮结束
{"seq": 9, "source": "ai",     "type": "done",            "done": true,                              "session_id": "1"}

# 错误
{"seq": 10, "source": "system", "type": "error",          "message": "network timeout",              "session_id": "1"}
```

前端采用 **send/display 分离**：消息通过 WebSocket 直接发送（`sendDirect`），不在本地构造用户消息 UI。收到 `message_consumed` 后才将用户消息追加到对话框并启动流式适配器，确保显示顺序与后端事件流严格一致。当消息进入等待队列时返回 `message_queued`，前端可显示"待处理"状态。

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
agent.NewAgent(
    chat.WithModel("claude-opus-4-8"),     // 模型名称
    chat.WithMaxTokens(8192),              // 最大生成 token
    chat.WithMaxContext(50),               // 最大上下文消息条数，超出时截断（0=不限制）
    chat.WithTemperature(0.7),             // 采样温度
    chat.WithTopP(0.9),                    // nucleus 采样
    chat.WithTopK(40),                     // top-k 采样
    chat.WithStopSequences("\n\nHuman:"),  // 停止序列
    chat.WithStream(true),                 // 流式模式（默认 true）
    chat.WithThinking(chat.ThinkingHigh),  // 扩展思考级别
)
```

## License

[MIT](LICENSE)
