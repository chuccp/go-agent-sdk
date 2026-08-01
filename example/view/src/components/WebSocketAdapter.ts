import type { ChatModelAdapter, ChatModelRunResult } from '@assistant-ui/react'

/**
 * WebSocket adapter for the go-agent-sdk chat protocol.
 *
 * Protocol:
 *   Send:    { type: "chat",  message: "user text", session_id: 123 }
 *   Send:    { type: "stop" }
 *   Receive: { type: "chunk", content: "text", done: false, conversation_id: "..." }
 *   Receive: { type: "done",  done: true }
 *   Receive: { type: "error", message: "error text" }
 *   Receive: { type: "message_sent", message_id: N }      — 消息已被立即处理
 *   Receive: { type: "message_queued", message_id: N }    — 消息进入等待队列
 *   Receive: { type: "message_consumed", message_id: N }  — 队列消息已被消费
 */

export interface QueueEvent {
  type: 'message_sent' | 'message_queued' | 'message_consumed'
  message_id: number
}

export type QueueEventListener = (evt: QueueEvent) => void

export function createSimpleWebSocketAdapter(
  getWs: () => WebSocket | null,
  getSessionId: () => number,
  onQueueEvent?: QueueEventListener,
): ChatModelAdapter {
  return {
    async *run({ messages, abortSignal }): AsyncGenerator<ChatModelRunResult> {
      const ws = getWs()
      if (!ws || ws.readyState !== WebSocket.OPEN) {
        yield { content: [{ type: 'text', text: '❌ WebSocket not connected. Is the server running on port 19009?' }] }
        return
      }

      // Extract text from the last user message
      const lastMsg = messages[messages.length - 1]
      if (!lastMsg || lastMsg.role !== 'user') return

      let textContent = ''
      if (Array.isArray(lastMsg.content)) {
        for (const part of lastMsg.content) {
          if (part.type === 'text' && part.text) textContent += part.text
        }
      } else if (typeof lastMsg.content === 'string') {
        textContent = lastMsg.content
      }

      if (!textContent.trim()) return

      // --- Streaming via event queue ---
      const queue: ChatModelRunResult[] = []
      let done = false
      let fullText = ''
      let thinkingText = ''

      const push = () => {
        // 将 thinking 和正文组合，用标记包裹 thinking
        let combined = ''
        if (thinkingText) {
          combined += `⟪think⟫${thinkingText}⟪/think⟫\n\n`
        }
        combined += fullText
        if (combined) {
          queue.push({ content: [{ type: 'text', text: combined }] })
        }
      }

      const handler = (evt: MessageEvent) => {
        try {
          const msg = JSON.parse(evt.data)
          switch (msg.type) {
            case 'thinking':
              if (msg.content) {
                thinkingText += msg.content
                push()
              }
              break
            case 'chunk':
              if (msg.content) {
                fullText += msg.content
                push()
              }
              break
            case 'done':
              done = true
              return
            case 'error':
              fullText += `\n\n❌ ${msg.message || 'Unknown error'}`
              push()
              done = true
              return
            case 'message_sent':
            case 'message_queued':
            case 'message_consumed':
              onQueueEvent?.({ type: msg.type, message_id: msg.message_id })
              break
          }
        } catch { /* ignore parse errors */ }
      }

      ws.addEventListener('message', handler)

      // Handle abort (user clicks stop)
      abortSignal.addEventListener('abort', () => {
        ws.removeEventListener('message', handler)
        if (ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({ type: 'stop' }))
        }
        done = true
      })

      // Send the message with session_id
      const sessionId = getSessionId()
      ws.send(JSON.stringify({ type: 'chat', message: textContent.trim(), session_id: sessionId }))

      // Yield results as they arrive
      while (!done) {
        if (queue.length > 0) {
          yield queue.shift()!
        } else {
          await new Promise(r => setTimeout(r, 50))
        }
      }

      // Drain remaining
      while (queue.length > 0) {
        yield queue.shift()!
      }

      ws.removeEventListener('message', handler)
    },
  }
}
