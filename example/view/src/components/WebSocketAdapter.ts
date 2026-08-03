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
 *   { type: "chunk",    content: "text" }
 *   { type: "thinking", content: "..." }
 *   { type: "done",     done: true }
 *   { type: "error",    message: "error text" }
 */

// ── Module-level streaming bridge ──

type StreamEvent =
  | { kind: 'chunk'; text: string }
  | { kind: 'thinking'; text: string }
  | { kind: 'done' }
  | { kind: 'error'; message: string }

let pendingTrigger: Promise<void> | null = null
let triggerResolve: (() => void) | null = null
let pendingBuffer: StreamEvent[] = []
let directDispatch: ((evt: StreamEvent) => void) | null = null
let stopCallback: (() => void) | null = null

let runCounter = 0  // 调试：追踪第几轮 run

/**
 * setStopCallback 设置取消时的回调（用于发送 stop 消息到后端）。
 */
export function setStopCallback(cb: () => void): void {
  stopCallback = cb
}

/**
 * triggerStream 触发适配器开始处理流事件。
 * 由 ChatRuntimeProvider 在收到 message_consumed 后调用。
 */
export function triggerStream(): void {
  console.log('[adapter] triggerStream called')
  // 丢弃触发前缓冲的终结性事件（上一轮残留的 done/error：
  // 当前轮在 message_consumed 之前不可能产生本轮的终结事件）
  pendingBuffer = pendingBuffer.filter(e => e.kind !== 'done' && e.kind !== 'error')
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
    console.log('[streamHandler] received type:', msg.type, 'seq:', msg.seq, 'done:', msg.done)
    let event: StreamEvent | null = null
    switch (msg.type) {
      case 'chunk':
        if (msg.content) event = { kind: 'chunk', text: msg.content }
        break
      case 'thinking':
        if (msg.content) event = { kind: 'thinking', text: msg.content }
        break
      case 'done':
        event = { kind: 'done' }
        console.log('[bridge] done event received, directDispatch =', !!directDispatch, 'buffer len =', pendingBuffer.length)
        break
      case 'error':
        event = { kind: 'error', message: msg.message || 'Unknown error' }
        break
    }
    if (!event) return

    if (directDispatch) {
      // 适配器已就绪，直接分发
      directDispatch(event)
    } else {
      // 适配器尚未启动，缓冲事件
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

      let fullText = ''
      let thinkingText = ''
      let done = false

      const push = () => {
        let combined = ''
        if (thinkingText) {
          combined += `⟪think⟫${thinkingText}⟪/think⟫\n\n`
        }
        combined += fullText
        if (combined) {
          queue.push({ content: [{ type: 'text' as const, text: combined }] })
        }
      }

      const queue: ChatModelRunResult[] = []

      const handleEvent = (evt: StreamEvent) => {
        console.log(`[adapter] run #${myRun} handleEvent:`, evt.kind)
        switch (evt.kind) {
          case 'thinking':
            thinkingText += evt.text
            push()
            break
          case 'chunk':
            fullText += evt.text
            push()
            break
          case 'done':
            console.log(`[adapter] run #${myRun} DONE received`)
            done = true
            break
          case 'error':
            fullText += `\n\n❌ ${evt.message}`
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
