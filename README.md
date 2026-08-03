# go-agent-sdk

一个轻量级 Go AI Agent SDK，提供流式对话、工具调用、历史持久化和断线续传能力，单进程即可运行完整的 Agent 服务。

## 设计理念

大多数 AI Agent SDK 把 SSE 流式转发当作旁路处理——事件要么透传给前端后丢弃，要么依赖外部基础设施（Redis PubSub、Kafka）来做持久化和回放。当需要支持多客户端订阅和断线续传时，问题就变成了基础设施问题。

这个 SDK 走了一条不同的路：**把续传状态放在协议层，而不是基础设施层**。

每条 Message 携带 `[Start, Offset)` —— 该消息产出了哪些事件。客户端只需一个 `start` 值就能精确续读，不需要 cursor 映射表。客户端断开后不保留任何状态（`*Position` 注销即可），重连时 `GetChat(id, start)` 重新创建，状态全在 Store 里。

这使得整个系统可以在零外部依赖的前提下，支持任意数量的客户端同时订阅同一个会话——每个客户端独立推进自己的读取位置，互不干扰。

**定位：** 这是一个聚焦于"SSE → 存储 → 多客户端推送"链路的嵌入式 SDK，不是编排框架或多 Agent 协作平台。它适合作为聊天应用的 Agent 层，不适合需要复杂工作流引擎的场景。

## 核心特性

- **多客户端订阅** — 同一会话可被多个 Client 同时订阅（多标签页），每个 Client 通过 Position 独立追踪读取进度，互不阻塞
- **断线续传** — 消息自带事件流区间 `[Start, Start+Offset)`，客户端凭一个 `start` 值即可精确续读，无需外部 broker
- **Client 无状态** — `ChatClient` 断开即丢弃，不保留任何会话状态，重连只是换一个 transport
- **流式对话** — SSE 流式输出，实时推送 thinking / text 增量
- **多轮工具调用** — 标准 tool_use → tool_result 循环，兼容 Anthropic Messages API
- **历史持久化** — 内存 + DB 双层存储，增量追加，懒加载
- **多提供商** — Provider Registry 支持注册多个 LLM 后端，运行时选择
- **Block 多态** — content 为接口数组，支持 text / thinking / image / tool_use / tool_result

## 架构概览

```
┌─────────────────────────────────────────────────────────────┐
│  ChatManager                                                │
│  ├── ProviderRegistry (多 LLM 后端)                          │
│  ├── ToolExecutors (工具注册)                                │
│  └── map[id] → ChatSession                                  │
│                  ├── inbox (消息队列, runMutex 保护)          │
│                  ├── messageProcessor                       │
│                  │    ├── run loop (持锁 → 释放I/O → 重持锁)  │
│                  │    └── stream → tool → persist            │
│                  ├── Store                                   │
│                  │    ├── entries (活跃事件缓冲区, 仅当前轮)    │
│                  │    ├── history (全量消息, 持久化)            │
│                  │    ├── positions (客户端读取位置列表)        │
│                  │    └── base (Reset 推进, seq 单调递增)       │
│                  └── ChatClient[] (轻量订阅句柄, 持有 Position) │
└─────────────────────────────────────────────────────────────┘
```

## 包结构

```
go-agent-sdk/
├── agent/          # Agent 层：ChatManager, ChatSession, ChatClient, messageProcessor, Tool, Options
├── chat/           # 协议层：Block, Event, Message, Request/Response, Store, Position, Provider
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
)

func main() {
    // 1. 创建 Manager
    manager := agent.NewChatManager(
        agent.WithModel("deepseek-v4-flash"),
        agent.WithMaxTokens(4096),
        agent.WithThinking(agent.ThinkingLow),
    )

    // 2. 注册 LLM 提供商
    manager.RegisterChat("my-provider", myChatService, true)

    // 3. 注册工具（可选）
    manager.AddTool(agent.NewCommandTool())

    // 4. 设置持久化（可选）
    manager.SetHistoryStore(myHistoryStore)

    // 5. 获取客户端（session_id + 起始偏移）
    client, _ := manager.GetChat("session-1", 0)

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

### Event（事件）双层模型

| 层 | 类型 | 用途 |
|---|---|---|
| 协议层 | `MessageStartEvent`, `ContentBlockDeltaEvent`, ... | 与 LLM SSE 一一对应，SDK 内部消费 |
| 推送层 | `ClientEvent` (chunk / thinking / done / tool_execution) | 面向前端，WS/SSE 直接推送 |

### 客户端读取位置（Position）

每个 `ChatClient` 持有一个 `*Position`，记录它在事件流中的读取偏移。调用 `ReadEvent()` 时，`Store.ReadFrom(position)` 返回下一个未读事件并自动推进 `position.start`，无需调用方手动管理偏移或发送 Ack。

`Reset()` 以所有已注册 Position 中最小的 `start` 为安全水位线，仅清理所有客户端均已读取的事件条目。

### Message 事件流区间

每条消息携带 `[Start, Start+Offset)` —— 该消息在事件流上的产出区间：

```
user msg:      Start=1, Offset=1   → 覆盖 [seq 1]     (MessageConsumed)
assistant msg: Start=2, Offset=21  → 覆盖 [seq 2..22] (thinking + chunk + done)
```

客户端重连时传入 `start`：
- `start < base` → 事件已 Reset，从 history 读完成消息
- `start >= base` → 事件在活缓冲区，从 entries 精确续读

### Store 生命周期

```
用户消息 → consumeMessage(): 入 history, 发 Consumed 事件
         → streamResponse(): 流式事件累积
         → appendAssistantMessage(): assistant msg 入 history, 回填 Offset
         → [tool_use]: executeTools() → tool_result 入 history
         → end_turn: SaveHistory(增量写 DB) → Reset(清空已读条目, 推进 base)
```

### run 循环锁模型

`messageProcessor.run()` 持有 `runMutex` 运行主循环，仅在 I/O 操作期间释放：

- **持有锁**：读取 inbox、构建请求、写入 history — 纯内存操作，保证状态一致性
- **释放锁**：LLM 网络调用、工具执行 — 耗时外部 I/O，释放锁期间允许新消息入队
- **重持锁**：I/O 完成后重新获取锁，检查取消状态，继续下一轮

这样在不引入额外 goroutine 通信机制的情况下，既保证了内存状态的线程安全，又不会在 I/O 期间阻塞用户输入。

## 工具系统

实现 `ToolExecutor` 接口即可注册自定义工具：

```go
type ToolExecutor interface {
    Definition() *chat.ToolFunction          // 工具元数据（发给 LLM）
    Execute(args map[string]any) (string, error) // 执行逻辑
}
```

内置 `CommandTool`：本地终端命令执行，带危险命令拦截 + GUI 程序自动 `start` + 30s 超时。

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
{"seq": 2, "source": "client", "type": "message_consumed", "message_id": 1, "content": "你好", "session_id": "1"}

# AI 流式输出
{"seq": 3, "source": "ai",     "type": "thinking",        "content": "让我看看当前目录...",           "session_id": "1"}
{"seq": 4, "source": "ai",     "type": "chunk",           "content": "你好！当前目录是：",            "session_id": "1"}
{"seq": 5, "source": "ai",     "type": "chunk",           "content": "\n\nproject/",                  "session_id": "1"}

# 工具执行（如果有 tool_use）
{"seq": 6, "source": "ai",     "type": "tool_execution",  "message": "execute_command", "content": "...", "session_id": "1"}

# 本轮结束
{"seq": 7, "source": "ai",     "type": "done",            "done": true,                              "session_id": "1"}

# 错误
{"seq": 8, "source": "system", "type": "error",           "message": "network timeout",              "session_id": "1"}
```

前端采用 **send/display 分离**：消息通过 WebSocket 直接发送（`sendDirect`），不在本地构造用户消息 UI。收到 `message_consumed` 后才将用户消息追加到对话框并启动流式适配器，确保显示顺序与后端事件流严格一致。

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
agent.NewChatManager(
    agent.WithModel("claude-opus-4-8"),     // 模型名称
    agent.WithMaxTokens(8192),              // 最大生成 token
    agent.WithTemperature(0.7),             // 采样温度
    agent.WithTopP(0.9),                    // nucleus 采样
    agent.WithTopK(40),                     // top-k 采样
    agent.WithStopSequences("\n\nHuman:"),  // 停止序列
    agent.WithStream(true),                 // 流式模式（默认 true）
    agent.WithThinking(agent.ThinkingHigh), // 扩展思考级别
)
```

## 关键设计决策

- **协议层续传 → 零基础设施** — 不同于 Mastra/Durable Streams 等依赖 Redis 或 CDN 做事件回放，这里让 Message 携带 `[Start, Offset)` 区间，把续传变成纯协议问题。结果：零外部依赖、单二进制部署。
- **Position 外挂 → Client 无状态** — 读取进度不记在 Client 上，而是注册到 Store 的 `positions` 列表。Client 断开即注销，不影响其他 Client 的 Reset 水位计算。重连时重新注册，无需 session affinity。
- **Store 双区模型** — entries（易失事件缓冲区）+ history（持久消息）。`Reset()` 取所有 Position 中最小的 `start` 作为安全水位线，只清理所有客户端均已读取的条目。
- **协议/推送事件分离** — LLM SSE 协议事件（`ContentBlockDelta` 等）→ SDK 内部消费；`ClientEvent`（chunk / thinking / done）→ 面向前端。换 LLM 提供商只改协议适配层。
- **runMutex 三段式** — 主循环持锁运行（操作 inbox、history），LLM 调用和工具执行期间释放锁。同步可追踪的调用链，不依赖 channel 通信。

## License

[MIT](LICENSE)
