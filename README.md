# go-agent-sdk

一个轻量级 Go AI Agent SDK，提供流式对话、工具调用、历史持久化和断线续传能力，单进程即可运行完整的 Agent 服务。

## 设计理念

大多数 AI Agent SDK 把 SSE 流式转发当作旁路处理——事件要么透传给前端后丢弃，要么依赖外部基础设施（Redis PubSub、Kafka）来做持久化和回放。当需要支持多客户端订阅和断线续传时，问题就变成了基础设施问题。

这个 SDK 走了一条不同的路：**把续传状态放在协议层，而不是基础设施层**。

每条 Message 携带 `[Start, Offset)` —— 该消息产出了哪些事件。客户端只需一个 `start` 值就能精确续读，不需要 cursor 映射表。客户端断开后不保留任何状态（`*Position` 注销即可），重连时 `GetChat(id, start)` 重新创建，状态全在 Store 里。

这使得整个系统可以在零外部依赖的前提下，支持任意数量的客户端同时订阅同一个会话——每个客户端独立推进自己的读取位置，互不干扰。

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

### 事件流与断线续传

每条 Message 携带 `[Start, Offset)` 区间，标记它产出了哪些事件。客户端持有一个 `start` 值即可精确续读——`start < base` 表示事件已归档，从 history 恢复；`start >= base` 表示事件仍在活跃缓冲区，从 entries 增量读取。

多个 Client 同时订阅时，每个 Client 通过 Position 独立推进读取进度，互不阻塞。

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

- **协议层续传 → 零基础设施** — 让 Message 携带 `[Start, Offset)` 区间，把续传变成协议问题而非基础设施问题。结果：零外部依赖、单二进制部署。
- **Position 外挂 → Client 无状态** — 读取进度不记在 Client 上，而是注册到 Store。Client 断开即注销，重连时重新注册，无需 session affinity。
- **Store 双区模型** — entries（易失）+ history（持久）。Reset 取所有 Client 最小已读位置为水位线，清理历史条目。
- **协议/推送事件分离** — LLM SSE 协议事件由 SDK 内部消费，`ClientEvent` 面向前端。换 LLM 提供商只改适配层。
- **主循环持锁运行** — 操作状态时持锁，LLM 调用和工具执行时释放锁，兼顾线程安全与用户输入响应。

## License

[MIT](LICENSE)
