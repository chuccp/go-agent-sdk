import { type ReactNode, useMemo, useRef, useEffect, useState, useCallback, createContext, useContext } from 'react'
import { AssistantRuntimeProvider, useLocalRuntime, useAssistantRuntime, useThreadRuntime, useThread } from '@assistant-ui/react'
import { createSimpleWebSocketAdapter, type QueueEvent } from './WebSocketAdapter'
import { getSessionMessages, type ChatMessage } from '../api/chat'

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

/** 解析 content 字段，返回 block 数组 */
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

/** 判断是否是 tool_result 消息 */
function isToolResult(blocks: ContentBlock[]): boolean {
  return blocks.length > 0 && blocks.every(b => b.type === 'tool_result')
}

/** 从 tool_result block 提取结果文本 */
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

/** 将 block 数组转换为带类型标记的文本 */
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

/**
 * 将原始消息列表转换为可显示的对话记录：
 * - tool_result 消息作为 assistant 显示
 * - thinking 以折叠块显示
 * - 合并相邻的 assistant 消息
 */
function buildDisplayMessages(msgs: ChatMessage[]): { role: 'user' | 'assistant'; content: string }[] {
  const result: { role: 'user' | 'assistant'; content: string }[] = []

  for (const m of msgs) {
    if (m.role !== 'user' && m.role !== 'assistant') continue

    const blocks = parseBlocks(m.content)
    let text: string
    let role: 'user' | 'assistant'

    if (isToolResult(blocks)) {
      // tool_result 作为 assistant 显示
      role = 'assistant'
      const content = toolResultToText(blocks).trim()
      text = content ? `⟪result⟫${content}⟪/result⟫` : ''
    } else {
      role = m.role as 'user' | 'assistant'
      text = blocksToText(blocks)
    }

    if (!text.trim()) continue

    // 相邻 assistant 合并
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
  /** 客户端排队待发送的消息内容 */
  pendingQueue: string[]
  /** 将消息加入客户端队列（AI 忙碌时调用） */
  queueSend: (text: string) => void
  /** 从队列头部出队一条 */
  shiftPending: () => void
  /** 思考等级 */
  thinkingLevel: string
  setThinkingLevel: (level: string) => void
}

const MessageQueueContext = createContext<MessageQueueState>({
  queuedMessages: [],
  queueCount: 0,
  pendingQueue: [],
  queueSend: () => {},
  shiftPending: () => {},
  thinkingLevel: 'off',
  setThinkingLevel: () => {},
})

export function useMessageQueue() {
  return useContext(MessageQueueContext)
}

interface Props {
  children: ReactNode
  sessionId: number
}

export function ChatRuntimeProvider({ children, sessionId }: Props) {
  const wsRef = useRef<WebSocket | null>(null)
  const sessionIdRef = useRef(sessionId)
  sessionIdRef.current = sessionId

  // 历史消息加载状态
  const [initialMessages, setInitialMessages] = useState<{ role: 'user' | 'assistant'; content: string }[] | null>(null)

  // 加载历史聊天记录
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

  // 消息队列状态
  const [queuedMessages, setQueuedMessages] = useState<QueuedMessage[]>([])

  // 客户端排队待发送的消息（AI 忙碌时用户提交的消息）
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

  const handleQueueEvent = useCallback((evt: QueueEvent) => {
    switch (evt.type) {
      case 'message_queued':
        setQueuedMessages(prev => [...prev, { id: evt.message_id, status: 'queued' }])
        break
      case 'message_consumed':
        setQueuedMessages(prev =>
          prev.map(m => m.id === evt.message_id ? { ...m, status: 'consumed' as const } : m)
        )
        // 消费后短暂保留然后移除
        setTimeout(() => {
          setQueuedMessages(prev => prev.filter(m => m.id !== evt.message_id))
        }, 600)
        break
      case 'message_sent':
        // 消息已被立即处理，无需排队显示
        break
    }
  }, [])

  // Connect WebSocket on mount
  useEffect(() => {
    let ws: WebSocket | null = null
    let reconnectTimer: ReturnType<typeof setTimeout>
    let mounted = true

    const connect = () => {
      if (!mounted) return
      const proto = location.protocol === 'https:' ? 'wss' : 'ws'
      const wsUrl = `${proto}://${location.hostname}:19009/ws/chat`
      ws = new WebSocket(wsUrl)
      wsRef.current = ws

      ws.onclose = () => {
        if (mounted) {
          reconnectTimer = setTimeout(connect, 2000)
        }
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

  const getWs = () => wsRef.current
  const getSessionId = () => sessionIdRef.current

  const adapter = useMemo(
    () => createSimpleWebSocketAdapter(getWs, getSessionId, handleQueueEvent, () => thinkingRef.current),
    [handleQueueEvent],
  )

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
  }), [queuedMessages, pendingQueue, queueSend, shiftPending, thinkingLevel])

  // 历史消息未加载完成时显示 loading
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
        <PendingSendWatcher />
        {children}
      </AssistantRuntimeProvider>
    </MessageQueueContext.Provider>
  )
}

/** 空闲时自动发送客户端队列中的下一条消息。 */
function PendingSendWatcher() {
  const threadRuntime = useThreadRuntime()
  const isRunning = useThread(t => t.isRunning)
  const { pendingQueue, shiftPending } = useMessageQueue()

  useEffect(() => {
    if (isRunning || pendingQueue.length === 0) return
    const next = pendingQueue[0]
    shiftPending()
    threadRuntime.composer.setText(next)
    threadRuntime.composer.send()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isRunning, pendingQueue.length, threadRuntime])

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
