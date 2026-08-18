import type { ChatModelAdapter, ChatModelRunResult } from '@assistant-ui/react'

/**
 * WebSocket streaming adapter for the go-agent-sdk chat protocol.
 *
 * Architecture: send/display separation
 *   - Sending is handled externally via sendDirect() (direct WebSocket write).
 *   - This adapter only consumes streaming events (chunk / thinking / done / error)
 *     and yields them as assistant content.
 *   - The trigger mechanism ensures the adapter starts processing only after
 *     the backend has consumed the user message (message_consumed).
 *
 * Protocol (receive only):
 *   { type: "chunk",           content: "text" }
 *   { type: "thinking",        content: "..." }
 *   { type: "command",         message: "cmd", content: "output" }  // 命令执行，输出可增量到达
 *   { type: "tool_execution",  message: "toolName", args: "input", content: "output" }
 *   { type: "done",            done: true }
 *   { type: "error",           message: "error text" }
 */

// ── Module-level streaming bridge ──

type StreamEvent =
  | { kind: 'chunk'; text: string }
  | { kind: 'thinking'; text: string }
  | { kind: 'command'; command: string; output: string }
  | { kind: 'tool_execution'; name: string; input: string; output: string }
  | { kind: 'done' }
  | { kind: 'error'; message: string }

// 内容段：与历史展示的 ⟪think⟫/⟪tool⟫/⟪result⟫/⟪command⟫ 标记一一对应，
// 保证实时渲染与历史渲染（Thread.tsx parseSegments）样式完全一致
interface Segment {
  kind: 'thinking' | 'text' | 'tool' | 'result' | 'command'
  text: string
  /** command 段携带的命令（供序列化与前端分组） */
  command?: string
}

function serializeSegments(segments: Segment[]): string {
  return segments.map(s => {
    switch (s.kind) {
      case 'thinking': return `⟪think⟫${s.text}⟪/think⟫`
      case 'tool': return `⟪tool⟫${s.text}⟪/tool⟫`
      case 'result': return `⟪result⟫${s.text}⟪/result⟫`
      case 'command': return `⟪command⟫${s.command ?? ''}\n${s.text}⟪/command⟫`
      default: return s.text
    }
  }).join('\n\n')
}

let pendingTrigger: Promise<void> | null = null
let triggerResolve: (() => void) | null = null
let pendingBuffer: StreamEvent[] = []
let directDispatch: ((evt: StreamEvent) => void) | null = null
let stopCallback: (() => void) | null = null
let askUserHandler: ((questionsJson: string) => void) | null = null

let runCounter = 0  // 调试：追踪第几轮 run

/**
 * setStopCallback 设置取消时的回调（用于发送 stop 消息到后端）。
 */
export function setStopCallback(cb: () => void): void {
  stopCallback = cb
}

/**
 * setAskUserHandler 设置 ask_user 事件处理器（用于渲染问题卡片）。
 * ask_user 不属于流事件，不进入适配器，单独路由给 UI。
 */
export function setAskUserHandler(cb: (questionsJson: string) => void): void {
  askUserHandler = cb
}

/**
 * triggerStream 触发适配器开始处理流事件。
 * 由 ChatRuntimeProvider 在收到 User block (sent/consume) 后调用。
 * 立即安装桥接分发，保证后续事件进入 pendingBuffer（由 adapter run 排空）。
 */
export function triggerStream(): void {
  console.log('[adapter] triggerStream called')
  // 丢弃触发前缓冲的终结性事件（上一轮残留的 done/error：
  // 当前轮在 message_consumed 之前不可能产生本轮的终结事件）
  pendingBuffer = pendingBuffer.filter(e => e.kind !== 'done' && e.kind !== 'error')
  // 立即安装桥接分发：后续事件进入 pendingBuffer，由 adapter run 排空并接管
  if (!directDispatch) {
    directDispatch = (evt: StreamEvent) => {
      pendingBuffer.push(evt)
    }
  }
  if (triggerResolve) {
    triggerResolve()
    triggerResolve = null
  }
}

/**
 * setupStreamBridge 为 WebSocket 安装全局流事件监听。
 * 在 WebSocket 连接建立后调用。所有 chunk/thinking/done/error 事件
 * 都会被捕获并路由到适配器（缓冲或直接分发）。
 */
export function setupStreamBridge(ws: WebSocket): void {
  // 重置状态
  pendingBuffer = []
  directDispatch = null
  pendingTrigger = new Promise<void>(resolve => { triggerResolve = resolve })
  console.log('[bridge] setupStreamBridge: trigger created, buffer cleared')

  ws.addEventListener('message', streamHandler)
}

function streamHandler(evt: MessageEvent): void {
  try {
    const msg = JSON.parse(evt.data)
    // 后端发送 {seq, block: {type, ...}} 格式
    const block = msg.block
    if (!block) return
    const blockType = block.type as string
    console.log('[streamHandler] block type:', blockType, 'seq:', msg.seq)

    let event: StreamEvent | null = null
    switch (blockType) {
      case 'delta':
        // 流式文本增量 → chunk
        if (block.content) event = { kind: 'chunk', text: block.content }
        break
      case 'text':
        // 完整文本块（工具输出等）→ chunk
        if (block.text) event = { kind: 'chunk', text: block.text }
        break
      case 'thinking':
        // 完整 thinking 块 → thinking
        if (block.thinking) event = { kind: 'thinking', text: block.thinking }
        break
      case 'tool_execution':
        // 工具执行事件：tool_name 为工具名，args 为入参，output 为输出
        if (block.tool_name || block.output) {
          event = {
            kind: 'tool_execution',
            name: block.tool_name || '',
            input: block.args || '',
            output: block.output || '',
          }
        }
        break
      case 'done':
        event = { kind: 'done' }
        console.log('[bridge] done block received')
        break
      case 'error':
        event = { kind: 'error', message: block.message || 'Unknown error' }
        break
      case 'ask_user':
        // ask_user 块：LLM 向用户提问，路由给 UI 渲染问题卡片（非流事件）
        console.log('[bridge] ask_user block received')
        if (askUserHandler && block.text) askUserHandler(block.text)
        return
      case 'start':
        // 流式块开始标记（StartBlock），不产生事件，delta 紧随其后
        return
      case 'usage':
        // token 用量元数据，不产生事件
        return
      default:
        console.log('[streamHandler] unknown block type:', blockType)
        return
    }
    if (!event) return

    if (directDispatch) {
      directDispatch(event)
    } else {
      pendingBuffer.push(event)
    }
  } catch { /* ignore parse errors */ }
}

// ── Adapter factory ──

export function createStreamingAdapter(): ChatModelAdapter {
  return {
    async *run({ abortSignal }): AsyncGenerator<ChatModelRunResult> {
      const myRun = ++runCounter
      console.log(`[adapter] run #${myRun} started, directDispatch before clear =`, !!directDispatch)

      // 每次启动新 run 时，先清理旧的 directDispatch（防止跨 turn 串扰）
      directDispatch = null

      // 等待 trigger（message_consumed 到达后触发）
      if (pendingTrigger) {
        console.log(`[adapter] run #${myRun} waiting for trigger...`)
        await pendingTrigger
        pendingTrigger = null
        console.log(`[adapter] run #${myRun} trigger received`)
      }

      let done = false

      // 按到达顺序维护内容段：不同轮次的 thinking 各自独立成段，
      // 中间穿插文本与工具执行段，与历史展示的段落结构保持一致
      const segments: Segment[] = []

      const appendToLast = (kind: Segment['kind'], text: string) => {
        const last = segments[segments.length - 1]
        if (last && last.kind === kind) {
          last.text += text
        } else {
          segments.push({ kind, text })
        }
      }

      const push = () => {
        const combined = serializeSegments(segments)
        if (combined) {
          queue.push({ content: [{ type: 'text' as const, text: combined }] })
        }
      }

      const queue: ChatModelRunResult[] = []

      const handleEvent = (evt: StreamEvent) => {
        console.log(`[adapter] run #${myRun} handleEvent:`, evt.kind)
        switch (evt.kind) {
          case 'thinking':
            appendToLast('thinking', evt.text)
            push()
            break
          case 'chunk':
            appendToLast('text', evt.text)
            push()
            break
          case 'command': {
            // 同一命令的增量输出聚合到同一个命令段；新命令开启新段
            const last = segments[segments.length - 1]
            if (last && last.kind === 'command' && last.command === evt.command) {
              last.text += evt.output
            } else {
              segments.push({ kind: 'command', command: evt.command, text: evt.output })
            }
            push()
            break
          }
          case 'tool_execution':
            // execute_command 已由专属 command 段展示（命令 + 输出），跳过重复的 tool_execution
            if (evt.name === 'execute_command') break
            // 与历史一致：工具调用段（入参优先，退化到工具名）+ 结果段独立展示
            segments.push({ kind: 'tool', text: evt.input || evt.name })
            if (evt.output) segments.push({ kind: 'result', text: evt.output })
            push()
            break
          case 'done':
            console.log(`[adapter] run #${myRun} DONE received`)
            done = true
            break
          case 'error':
            appendToLast('text', `\n\n❌ ${evt.message}`)
            push()
            done = true
            break
        }
      }

      // 1. 排空缓冲区（trigger 之前到达的事件）
      console.log(`[adapter] run #${myRun} draining buffer, count =`, pendingBuffer.length)
      for (const evt of pendingBuffer) {
        handleEvent(evt)
      }
      pendingBuffer = []

      // 2. 安装直接分发处理器（后续事件不再经过缓冲）
      directDispatch = handleEvent
      console.log(`[adapter] run #${myRun} directDispatch installed, done =`, done)

      // 3. 处理取消
      const onAbort = () => {
        console.log(`[adapter] run #${myRun} abort signal`)
        directDispatch = null
        done = true
        stopCallback?.()
      }
      abortSignal?.addEventListener('abort', onAbort)

      // 4. 流式输出
      while (!done) {
        if (queue.length > 0) {
          yield queue.shift()!
        } else {
          await new Promise(r => setTimeout(r, 50))
        }
      }

      // 5. 自然结束时移除 abort 监听，防止框架取消上一轮时误发 stop
      abortSignal?.removeEventListener('abort', onAbort)

      // 6. 排空剩余
      console.log(`[adapter] run #${myRun} draining remaining, count =`, queue.length)
      while (queue.length > 0) {
        yield queue.shift()!
      }

      directDispatch = null
      console.log(`[adapter] run #${myRun} finished`)
    },
  }
}
