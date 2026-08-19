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
│  ├── ProviderRegistry (多 LLM 后端)                              │
│  ├── ToolExecutors (工具注册)                                     │
│  └── map[id] → session                                        │
│                  └── SessionContext (状态中心)                    │
│                       ├── inbox (消息队列, runLock 保护)            │
│                       ├── messageProcessor (会话编排器)            │
│                       │    ├── HandleRevMessage (入队+启动主循环)    │
│                       │    └── doLoop                         │
│                       │    │    ├── executeRound (构建请求+LLM)   │
│                       │    │    └── executeTools (tool_result)│
│                       ├── Store                               │
│                       │    ├── entries (活跃事件缓冲区)              │
│                       │    ├── history0 + tempHistory          │
│                       │    │   (全量消息: 已持久化 + 待保存)          │
│                       │    ├── positions (客户端读取位置列表)          │
│                       │    └── seq (事件序号, 单调递增)               │
│                       └── Client[] (轻量订阅句柄)                   │
└───────────────────────────────────────────────────────────────┘
```

## 包结构

```
go-agent-sdk/
├── agent/          # Agent 层：Agent, SessionContext, Client, messageProcessor,
│                   #   ToolExecutor, Turn, Store, HistoryStore, Position
├── chat/           # 协议层：Block, Event, Message, Request, Service, Options
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
    "fmt"

    "github.com/chuccp/go-agent-sdk/agent"
    "github.com/chuccp/go-agent-sdk/chat"
    "github.com/chuccp/go-agent-sdk/tools"
)

func main() {
    // 1. 创建 Agent
    a := agent.NewAgent()
    a.ChatOption(
        chat.WithModel("deepseek-v4-flash"),
        chat.WithMaxTokens(4096),
        chat.WithThinking(chat.ThinkingLow),
    )

    // 2. 注册 LLM 提供商
    a.RegisterChat("my-provider", myChatService, true)

    // 3. 注册工具（可选）
    a.AddTools(tools.NewCommandTool())

    // 4. 设置持久化（可选）
    a.SetHistoryStore(myHistoryStore)

    // 5. 获取客户端（session_id + 起始偏移）
    client, _ := a.GetClient("session-1", 0)

    // 6. 发送消息
    client.SendText("你好，帮我查看当前目录")

    // 7. 读取事件流（Event.Block 按 type 字段多态分发）
    for {
        event := client.ReadEvent()
        if event == nil {
            break
        }
        switch b := event.Block.(type) {
        case *chat.DeltaBlock:
            fmt.Print(b.Content) // 流式增量
        case *chat.DoneBlock:
            fmt.Println()
            return
        }
    }
}
```

## 核心概念

### Block（内容块）

消息的 `Content` 是 `Blocks`（`[]Block` 接口数组），支持多态 JSON 序列化。每个具体块都带 `Type BlockType` 字段，反序列化时按 `type` 分发还原（`Blocks` 实现自定义 `UnmarshalJSON`），历史持久化加载后可无损往返。

```go
type Block interface { ForContext() bool }  // 声明该块是否进入 LLM 上下文

// 具体类型（均带 Type BlockType 字段）
TextBlock       { Text string; TextType TextType }  // TextType: "" / error / cmd / flow_progress
ThinkingBlock   { Thinking string }
ImageBlock      { Source *ImageSource }
ToolUseBlock    { ID, Name string; Input *value.Object }
ToolResultBlock { ToolUseID string; Content Blocks }
UsageBlock      { Usage *Usage }                    // token 用量元数据（不进上下文、不发前端）
```

### 事件流与断线续传

每条 Message 携带事件区间 `[Start, Start+Offset)`，标记它产出了哪些事件，区间与全局单调递增的事件序号 `seq` 对齐。客户端持有一个绝对偏移 `start`（由持久化历史计算得到）即可从活跃事件缓冲区（`entries`）增量续读——已被所有客户端读过的旧事件随 `ResetAndSave` 裁掉（同时把待保存历史迁入持久层），`start` 早于缓冲区头部时自动钳制；服务重启后 `LoadHistory` 从历史恢复 `seq`，新事件无缝接续。

多个 Client 同时订阅时，每个 Client 通过 Position 独立推进读取进度，互不阻塞。

## 工具系统

实现 `ToolExecutor` 接口即可注册自定义工具：

```go
type ToolExecutor interface {
    Definition() *chat.ToolFunction                     // 工具元数据（发给 LLM）
    Name() string                                       // 工具唯一名称
    UsagePrompt() string                                // 工具引导提示词（随每轮 System 注入）
    Execute(turn *Turn, writer *chat.BlockStream)     // 执行逻辑（错误经 ErrorText 以文本写入）
}
```

`Turn` 是每次工具执行的载体，提供 `Args()` 获取工具入参、`Context()` 获取会话上下文（`SessionContext`）。
执行结果通过 `writer`（统一的 `chat.BlockStream`）写出，支持逐块输出内容；
LLM 流式输出与工具输出共用同一 BlockStream（停止原因/用量统一为 Block 收集，错误经 ErrorText 以文本写入）。

### 内置工具

| 工具 | 文件 | 说明 |
|------|------|------|
| `CommandTool` | `tools/command.go` | 本地终端命令执行，带危险命令拦截 + GUI 程序自动 `start` + 30s 超时。`command_unix.go` / `command_windows.go` 提供平台适配 |
| `TodoTool` | `tools/todo.go` | 任务追踪（对齐 Claude Code Task 模型），支持 pending/in_progress/completed 状态，通过 `TodoStore` 跨会话共享 |
| `AskUserQuestionTool` | `tools/ask_user_question.go` | LLM 向用户提问澄清问题：推送 `ask_user` 事件（问题列表 JSON）并置 `user_wait` 停止原因后立即返回（不阻塞）；主循环据此结束本轮（跳过携带 tool_result 的 LLM 收尾调用），用户的回答作为下一条普通消息进入会话触发新一轮 |

## HistoryStore 接口

由主程序实现持久化策略：

```go
type HistoryStore interface {
    LoadHistory(sessionID string) ([]chat.Message, error)     // 懒加载历史
    AppendMessages(sessionID string, messages []chat.Message) error // 增量追加
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

所有推送事件均为 `{seq, block}` 格式：`seq` 为全局单调递增的事件序号，`block` 为多态内容块（按 `type` 字段区分）。

```
# 用户消息生命周期（block.type = "User"，block.block_user_type 区分状态，id 为稳定消息 ID）
{"seq": 1, "block": {"type": "User", "block_user_type": "sent",    "id": "1", "content": [{"type": "text", "text": "你好"}]}}
{"seq": 2, "block": {"type": "User", "block_user_type": "consume", "id": "1", "content": [{"type": "text", "text": "你好"}]}}

# AI 流式输出（start 标记块类型，delta 携带增量）
{"seq": 3, "block": {"type": "start", "block": {"type": "thinking"}}}
{"seq": 4, "block": {"type": "delta", "content": "让我看看当前目录..."}}
{"seq": 5, "block": {"type": "start", "block": {"type": "text"}}}
{"seq": 6, "block": {"type": "delta", "content": "你好！当前目录是："}}

# LLM 向用户提问（AskUserQuestion 工具）
{"seq": 7, "block": {"type": "ask_user", "text": "[...问题列表 JSON...]"}}

# 本轮结束
{"seq": 8, "block": {"type": "done"}}

# 错误
{"seq": 9, "block": {"type": "error", "text": "network timeout"}}
```

前端采用 **send/display 分离**：消息通过 WebSocket 直接发送（`sendDirect`），不在本地构造用户消息 UI。收到 `User` 块（`block_user_type=consume`）后才将用户消息追加到对话框并启动流式适配器，确保显示顺序与后端事件流严格一致；`User` 块携带稳定 `id`（sent/queued/consume 同一条消息共享），前端据此做队列状态迁移与清理。

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
a := agent.NewAgent()
a.ChatOption(
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
