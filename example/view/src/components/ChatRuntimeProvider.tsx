import { type ReactNode, useMemo, useRef, useEffect, useState, useCallback, createContext, useContext } from 'react'
import {
  AssistantRuntimeProvider,
  useLocalRuntime,
  useAssistantRuntime,
  useThreadRuntime,
  useThread,
} from '@assistant-ui/react'
import {
  createStreamingAdapter,
  setupStreamBridge,
  triggerStream,
  setStopCallback,
} from './WebSocketAdapter'
import { getSessionMessages, type ChatMessage } from '../api/chat'

// ── 历史消息转换 ──

interface ContentBlock {
  type: string
  text?: string
  thinking?: string
  tool_use_id?: string
  content?: unknown
  name?: string
  input?: unknown
  id?: string
}

function parseBlocks(content: string): ContentBlock[] {
  if (!content) return []
  const trimmed = content.trim()
  if (!trimmed.startsWith('[')) {
    return [{ type: 'text', text: content }]
  }
  try {
    return JSON.parse(trimmed) as ContentBlock[]
  } catch {
    return [{ type: 'text', text: content }]
  }
}

function isToolResult(blocks: ContentBlock[]): boolean {
  return blocks.length > 0 && blocks.every(b => b.type === 'tool_result')
}

function toolResultToText(blocks: ContentBlock[]): string {
  const parts: string[] = []
  for (const b of blocks) {
    if (b.type !== 'tool_result') continue
    if (typeof b.content === 'string') {
      parts.push(b.content)
    } else if (Array.isArray(b.content)) {
      for (const sub of b.content as ContentBlock[]) {
        if (sub.type === 'text' && sub.text) parts.push(sub.text)
      }
    }
  }
  return parts.join('\n')
}

function blocksToText(blocks: ContentBlock[]): string {
  const parts: string[] = []
  for (const b of blocks) {
    if (b.type === 'thinking' && b.thinking) {
      parts.push(`⟪think⟫${b.thinking}⟪/think⟫`)
    } else if (b.type === 'text' && b.text) {
      parts.push(b.text)
    } else if (b.type === 'tool_use' && b.name) {
      const input = b.input as Record<string, unknown> | undefined
      const cmd = input?.command ? String(input.command) : JSON.stringify(input)
      parts.push(`⟪tool⟫${cmd}⟪/tool⟫`)
    }
  }
  return parts.join('\n\n')
}

function buildDisplayMessages(msgs: ChatMessage[]): { role: 'user' | 'assistant'; content: string }[] {
  const result: { role: 'user' | 'assistant'; content: string }[] = []
  for (const m of msgs) {
    if (m.role !== 'user' && m.role !== 'assistant') continue
    const blocks = parseBlocks(m.content)
    let text: string
    let role: 'user' | 'assistant'
    if (isToolResult(blocks)) {
      role = 'assistant'
      const content = toolResultToText(blocks).trim()
      text = content ? `⟪result⟫${content}⟪/result⟫` : ''
    } else {
      role = m.role as 'user' | 'assistant'
      text = blocksToText(blocks)
    }
    if (!text.trim()) continue
    const last = result[result.length - 1]
    if (last && last.role === 'assistant' && role === 'assistant') {
      last.content += '\n\n' + text
    } else {
      result.push({ role, content: text })
    }
  }
  return result
}

// ── Message Queue Context ──

export interface QueuedMessage {
  id: number
  status: 'queued' | 'consumed'
}

interface MessageQueueState {
  queuedMessages: QueuedMessage[]
  queueCount: number
  pendingQueue: string[]
  /** 将消息加入客户端队列（AI 忙碌时调用） */
  queueSend: (text: string) => void
  shiftPending: () => void
  thinkingLevel: string
  setThinkingLevel: (level: string) => void
  /** 直接发送消息到后端（不经过框架），由 message_consumed 事件驱动显示 */
  sendDirect: (text: string) => void
}

const MessageQueueContext = createContext<MessageQueueState>({
  queuedMessages: [],
  queueCount: 0,
  pendingQueue: [],
  queueSend: () => {},
  shiftPending: () => {},
  thinkingLevel: 'off',
  setThinkingLevel: () => {},
  sendDirect: () => {},
})

export function useMessageQueue() {
  return useContext(MessageQueueContext)
}

// ── Provider ──

interface Props {
  children: ReactNode
  sessionId: number
}

export function ChatRuntimeProvider({ children, sessionId }: Props) {
  const wsRef = useRef<WebSocket | null>(null)
  const sessionIdRef = useRef(sessionId)
  sessionIdRef.current = sessionId

  // 历史消息
  const [initialMessages, setInitialMessages] = useState<{ role: 'user' | 'assistant'; content: string }[] | null>(null)

  useEffect(() => {
    let cancelled = false
    getSessionMessages(sessionId)
      .then(msgs => {
        if (cancelled) return
        setInitialMessages(buildDisplayMessages(msgs))
      })
      .catch(() => {
        if (!cancelled) setInitialMessages([])
      })
    return () => { cancelled = true }
  }, [sessionId])

  // 消息队列
  const [queuedMessages, setQueuedMessages] = useState<QueuedMessage[]>([])
  const [pendingQueue, setPendingQueue] = useState<string[]>([])
  const queueSend = useCallback((text: string) => {
    setPendingQueue(prev => [...prev, text])
  }, [])
  const shiftPending = useCallback(() => {
    setPendingQueue(prev => prev.slice(1))
  }, [])

  // 思考等级
  const [thinkingLevel, setThinkingLevel] = useState<string>('off')
  const thinkingRef = useRef(thinkingLevel)
  thinkingRef.current = thinkingLevel

  // ── sendDirect: 只负责把消息发给后端，不操作 UI ──
  const sendDirect = useCallback((text: string) => {
    console.log('[sendDirect] sending:', text.substring(0, 30))
    const ws = wsRef.current
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      console.log('[sendDirect] WebSocket not open')
      return
    }

    // 初始化会话（幂等）
    ws.send(JSON.stringify({ type: 'create', session_id: sessionIdRef.current, start: 0 }))

    // 发送聊天消息
    const chatMsg: Record<string, string> = { type: 'chat', message: text.trim() }
    const thinking = thinkingRef.current
    if (thinking && thinking !== 'off') chatMsg.thinking = thinking
    ws.send(JSON.stringify(chatMsg))
  }, [])

  // ── consumeMessage: 后端确认消费后，将用户消息加入对话框并启动流 ──
  const consumeMessageRef = useRef<(text: string) => void>(() => {})

  // ── WebSocket 连接 + 事件处理 ──
  useEffect(() => {
    let ws: WebSocket | null = null
    let reconnectTimer: ReturnType<typeof setTimeout>
    let mounted = true

    const connect = () => {
      if (!mounted) return
      const proto = location.protocol === 'https:' ? 'wss' : 'ws'
      ws = new WebSocket(`${proto}://${location.hostname}:19009/ws/chat`)
      wsRef.current = ws

      ws.onopen = () => {
        // 安装流事件桥接
        setupStreamBridge(ws!)
        // 设置停止回调
        setStopCallback(() => {
          if (ws!.readyState === WebSocket.OPEN) {
            ws!.send(JSON.stringify({ type: 'stop' }))
          }
        })
      }

      ws.onmessage = (evt: MessageEvent) => {
        try {
          const msg = JSON.parse(evt.data)
          switch (msg.type) {
            case 'message_sent':
              // 消息已被立即处理，更新状态栏
              setQueuedMessages(prev => [...prev, { id: msg.message_id, status: 'consumed' as const }])
              setTimeout(() => {
                setQueuedMessages(prev => prev.filter(m => m.id !== msg.message_id))
              }, 600)
              break
            case 'message_queued':
              // 消息进入等待队列
              setQueuedMessages(prev => [...prev, { id: msg.message_id, status: 'queued' as const }])
              break
            case 'message_consumed':
              console.log('[ws] message_consumed:', msg.message_id, 'content:', msg.content?.substring(0, 30))
              // 后端确认消费 → 将用户消息加入对话框 + 触发流
              setQueuedMessages(prev =>
                prev.map(m => m.id === msg.message_id ? { ...m, status: 'consumed' as const } : m)
              )
              setTimeout(() => {
                setQueuedMessages(prev => prev.filter(m => m.id !== msg.message_id))
              }, 600)
              // 核心：用户消息显示 + 启动助手响应
              if (msg.content) {
                consumeMessageRef.current(msg.content)
              }
              break
          }
        } catch { /* ignore */ }
      }

      ws.onclose = () => {
        if (mounted) reconnectTimer = setTimeout(connect, 2000)
      }
      ws.onerror = () => {}
    }

    connect()

    return () => {
      mounted = false
      clearTimeout(reconnectTimer)
      ws?.close()
    }
  }, [])

  // ── Adapter + Runtime ──
  const adapter = useMemo(() => createStreamingAdapter(), [])
  const runtime = useLocalRuntime(adapter, {
    initialMessages: initialMessages ?? [],
  })

  const queueState = useMemo<MessageQueueState>(() => ({
    queuedMessages,
    queueCount: queuedMessages.filter(m => m.status === 'queued').length,
    pendingQueue,
    queueSend,
    shiftPending,
    thinkingLevel,
    setThinkingLevel,
    sendDirect,
  }), [queuedMessages, pendingQueue, queueSend, shiftPending, thinkingLevel, sendDirect])

  if (initialMessages === null) {
    return (
      <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', color: '#80868b', fontSize: 14 }}>
        加载聊天记录…
      </div>
    )
  }

  return (
    <MessageQueueContext.Provider value={queueState}>
      <AssistantRuntimeProvider runtime={runtime}>
        <SessionResetter sessionId={sessionId} />
        <MessageConsumedHandler consumeMessageRef={consumeMessageRef} />
        <PendingSendWatcher />
        {children}
      </AssistantRuntimeProvider>
    </MessageQueueContext.Provider>
  )
}

/**
 * 注册 consumeMessage 回调：将用户消息追加到线程并启动 adapter 流。
 * 必须在 AssistantRuntimeProvider 内部使用（需要 useThreadRuntime）。
 */
function MessageConsumedHandler({ consumeMessageRef }: { consumeMessageRef: React.MutableRefObject<(text: string) => void> }) {
  const threadRuntime = useThreadRuntime()

  useEffect(() => {
    consumeMessageRef.current = (text: string) => {
      console.log('[consumeMessage] appending user message, text:', text.substring(0, 30))
      triggerStream()
      // 使用 append 的 startRun 选项，一步完成：追加消息 + 启动 adapter
      threadRuntime.append({
        role: 'user',
        content: [{ type: 'text', text }],
        startRun: true,
      } as any)
    }
  }, [threadRuntime, consumeMessageRef])

  return null
}

/**
 * 空闲时自动发送客户端队列中的下一条消息。
 */
function PendingSendWatcher() {
  const isRunning = useThread(t => t.isRunning)
  const { pendingQueue, shiftPending, sendDirect } = useMessageQueue()

  // 使用 ref 确保 effect 内始终能访问最新的函数
  const shiftRef = useRef(shiftPending)
  shiftRef.current = shiftPending
  const sendRef = useRef(sendDirect)
  sendRef.current = sendDirect
  const queueRef = useRef(pendingQueue)
  queueRef.current = pendingQueue

  useEffect(() => {
    console.log('[PendingSendWatcher] isRunning:', isRunning, 'queueLen:', queueRef.current.length)
    if (isRunning || queueRef.current.length === 0) return
    const next = queueRef.current[0]
    console.log('[PendingSendWatcher] auto-sending:', next.substring(0, 30))
    shiftRef.current()
    sendRef.current(next)
  }, [isRunning])

  return null
}

/** Resets the thread when sessionId changes. */
function SessionResetter({ sessionId }: { sessionId: number }) {
  const prevRef = useRef(sessionId)
  const runtime = useAssistantRuntime()

  useEffect(() => {
    if (sessionId !== prevRef.current) {
      prevRef.current = sessionId
      runtime.threads.main.reset()
    }
  }, [sessionId, runtime])

  return null
}
