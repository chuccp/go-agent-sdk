import { type ReactNode, useMemo, useRef, useEffect, useState, useCallback, createContext, useContext } from 'react'
import {
  AssistantRuntimeProvider,
  useLocalRuntime,
  useAssistantRuntime,
  useThreadRuntime,
  useThread,
  type ChatModelAdapter,
} from '@assistant-ui/react'
import {
  createStreamingAdapter,
  setupStreamBridge,
  triggerStream,
  setStopCallback,
  setAskUserHandler,
  setSkipNextStop,
  setLatestUsage,
} from './WebSocketAdapter'
import { getSessionEvents, type ChatEvent } from '../api/chat'

// ── 历史事件转换（与 WebSocket 实时流共用 block type 分发逻辑） ──

interface ContentBlock {
  type: string
  text?: string
  thinking?: string
  tool_use_id?: string
  content?: unknown
  name?: string
  input?: unknown
  id?: string
  block_user_type?: string
  block?: ContentBlock
  Usage?: { input_tokens: number; output_tokens: number; cache_input_tokens: number }
}

/** extractUsageFromEvents 从事件列表中提取 token 用量。 */
function extractUsageFromEvents(events: ChatEvent[]): void {
  for (let i = events.length - 1; i >= 0; i--) {
    for (const b of events[i].blocks) {
      const bt = b as ContentBlock
      if ((bt.type === 'message_delta' || bt.type === 'message_start') && bt.Usage) {
        setLatestUsage({
          inputTokens: bt.Usage.input_tokens ?? 0,
          outputTokens: bt.Usage.output_tokens ?? 0,
          cacheInputTokens: bt.Usage.cache_input_tokens ?? 0,
        })
        return
      }
    }
  }
}

/**
 * buildDisplayMessages 从事件列表构建显示消息。
 * 与 WebSocket 流共用 block type 分发逻辑：
 *   User block (consume) → 用户消息
 *   text/thinking/tool_use/tool_result/custom_text/error → AI 输出
 */
function buildDisplayMessages(events: ChatEvent[]): { role: 'user' | 'assistant'; content: string }[] {
  const result: { role: 'user' | 'assistant'; content: string }[] = []

  let currentStreamType: string | null = null
  let currentToolName: string | null = null
  let currentToolUseId: string | null = null
  let toolInputJson = ''
  const commandByToolUseId = new Map<string, string>()
  let activeCommand: string | null = null

  const parseCommand = (json: string): string => {
    try {
      const obj = JSON.parse(json)
      if (typeof obj?.command === 'string' && obj.command) return obj.command
    } catch { /* ignore */ }
    return json.trim()
  }

  const appendText = (role: 'user' | 'assistant', text: string) => {
    if (!text.trim()) return
    const last = result[result.length - 1]
    if (last && last.role === role) {
      last.content += '\n\n' + text
    } else {
      result.push({ role, content: text })
    }
  }

  for (const evt of events) {
    for (const block of evt.blocks) {
      const b = block as ContentBlock
      switch (b.type) {
        case 'User': {
          if (b.block_user_type === 'consume') {
            const content = b.content as ContentBlock[] | undefined
            const text = content?.map(c => c.text || '').filter(Boolean).join('') || ''
            if (text) appendText('user', text)
          }
          break
        }
        case 'start': {
          const inner = b.block
          const innerType = inner?.type || null
          const innerName = inner?.name || null
          const innerId = inner?.id || null
          const toolUseId = inner?.tool_use_id || null
          if (currentStreamType === 'tool_use' && currentToolName === 'execute_command' && currentToolUseId) {
            const cmd = parseCommand(toolInputJson)
            if (cmd) commandByToolUseId.set(currentToolUseId, cmd)
          }
          if (innerType === 'tool_use') {
            activeCommand = null
            currentToolUseId = innerId
            currentToolName = innerName
            toolInputJson = ''
          } else {
            activeCommand = toolUseId ? (commandByToolUseId.get(toolUseId) ?? null) : null
            currentToolUseId = null
            currentToolName = null
            toolInputJson = ''
          }
          currentStreamType = innerType
          break
        }
        case 'delta': {
          const content = (b as Record<string, unknown>).content as string || b.text || ''
          if (!content) break
          if (currentStreamType === 'tool_use') {
            if (currentToolName === 'execute_command') {
              toolInputJson += content
            } else {
              appendText('assistant', content)
            }
          } else if (currentStreamType === 'thinking') {
            // thinking 在历史中跳过
          } else if (activeCommand !== null) {
            appendText('assistant', `⟪command⟫${activeCommand}\n${content}⟪/command⟫`)
            activeCommand = null
          } else {
            appendText('assistant', content)
          }
          break
        }
        case 'text': {
          const text = b.text || ''
          if (!text) break
          if (activeCommand !== null) {
            appendText('assistant', `⟪command⟫${activeCommand}\n${text}⟪/command⟫`)
            activeCommand = null
          } else {
            appendText('assistant', text)
          }
          break
        }
        case 'thinking': {
          const text = b.thinking || ''
          if (text) appendText('assistant', `⟪think⟫${text}⟪/think⟫`)
          break
        }
        case 'tool_use': {
          const input = b.input as Record<string, unknown> | undefined
          if (b.name === 'execute_command') {
            const cmd = input?.command ? String(input.command) : ''
            if (cmd && b.id) commandByToolUseId.set(b.id, cmd)
          } else {
            const text = input?.command ? String(input.command) : JSON.stringify(input)
            if (text) appendText('assistant', text)
          }
          break
        }
        case 'tool_result': {
          const inner = b.content as ContentBlock[] | undefined
          if (inner) {
            const firstText = inner.find(c => c.type === 'text' && c.text)
            const toolUseId = firstText?.tool_use_id || b.tool_use_id || null
            const cmd = toolUseId ? commandByToolUseId.get(toolUseId) : null
            const text = inner.filter(c => c.type === 'text' && c.text).map(c => c.text).join('\n')
            if (text) {
              if (cmd) {
                // execute_command 每个命令独立一条消息
                result.push({ role: 'assistant', content: `⟪command⟫${cmd}\n${text}⟪/command⟫` })
              } else {
                appendText('assistant', `⟪result⟫${text}⟪/result⟫`)
              }
            }
          }
          break
        }
        case 'custom_text': {
          if (b.text) appendText('assistant', b.text)
          break
        }
        case 'error': {
          const text = (b as Record<string, unknown>).text as string || ''
          if (text) appendText('assistant', `❌ ${text}`)
          break
        }
        case 'done':
          currentStreamType = null
          currentToolName = null
          currentToolUseId = null
          toolInputJson = ''
          activeCommand = null
          break
      }
    }
  }
  return result
}

// ── Message Queue Context ──

export interface QueuedMessage {
  id: string
  status: 'queued' | 'consumed'
}

// ── ask_user 问题结构（对齐后端 tools.Question）──

export interface AskUserOption {
  label: string
  description: string
  preview?: string
}

export interface AskUserQuestion {
  question: string
  header: string
  options: AskUserOption[]
  multi_select?: boolean
}

interface MessageQueueState {
  queuedMessages: QueuedMessage[]
  queueCount: number
  thinkingLevel: string
  setThinkingLevel: (level: string) => void
  /** 直接发送消息到后端（不经过框架），由 message_consumed 事件驱动显示 */
  sendDirect: (text: string) => void
  /** 当前待回答的 ask_user 问题（null 表示无） */
  pendingQuestion: AskUserQuestion[] | null
  /** 提交 ask_user 回答：直发后端并清除问题卡片 */
  submitAnswer: (text: string) => void
}

const MessageQueueContext = createContext<MessageQueueState>({
  queuedMessages: [],
  queueCount: 0,
  thinkingLevel: 'off',
  setThinkingLevel: () => {},
  sendDirect: () => {},
  pendingQuestion: null,
  submitAnswer: () => {},
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

  // 历史消息 + 事件流起始位置（start = 最后一条历史消息的 start + offset）
  const [initialMessages, setInitialMessages] = useState<{ role: 'user' | 'assistant'; content: string }[] | null>(null)
  const startRef = useRef<number | null>(null)

  // 会话就绪状态：收到服务端 created 回执后才允许发送聊天消息
  const createdRef = useRef(false)
  const pendingChatRef = useRef<string[]>([]) // 回执到达前暂存的聊天消息报文

  // ── sendCreate: WS 打开后发送 create 消息，接入事件流（从 start 位置开始） ──
  const sendCreate = useCallback(() => {
    const ws = wsRef.current
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    if (startRef.current === null) return // 历史尚未加载完成
    ws.send(JSON.stringify({ type: 'create', session_id: sessionIdRef.current, start: startRef.current }))
  }, [])

  useEffect(() => {
    let cancelled = false
    startRef.current = null
    createdRef.current = false
    pendingChatRef.current = []
    setPendingQuestion(null)
    getSessionEvents(sessionId)
      .then(events => {
        if (cancelled) return
        extractUsageFromEvents(events)
        const last = events[events.length - 1]
        startRef.current = last ? last.start + last.offset : 0
        setInitialMessages(buildDisplayMessages(events))
        sendCreate()
      })
      .catch(() => {
        if (cancelled) return
        startRef.current = 0
        setInitialMessages([])
        sendCreate()
      })
    return () => { cancelled = true }
  }, [sessionId, sendCreate])

  // 消息队列（仅展示后端返回的排队状态）
  const [queuedMessages, setQueuedMessages] = useState<QueuedMessage[]>([])

  // ask_user 待回答问题
  const [pendingQuestion, setPendingQuestion] = useState<AskUserQuestion[] | null>(null)

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

    // 初始化会话（幂等；历史加载完成后携带事件流起始位置）
    sendCreate()

    // 发送聊天消息：未收到 created 回执时暂存，由回执触发补发
    const chatMsg: Record<string, string> = { type: 'chat', message: text.trim() }
    const thinking = thinkingRef.current
    if (thinking && thinking !== 'off') chatMsg.thinking = thinking
    const payload = JSON.stringify(chatMsg)
    if (createdRef.current) {
      ws.send(payload)
    } else {
      pendingChatRef.current.push(payload)
    }
  }, [sendCreate])

  // ── submitAnswer: 提交 ask_user 回答：直发后端（绕过客户端队列，
  // 避免 isRunning 永真导致的队列死锁）并清除问题卡片 ──
  const submitAnswer = useCallback((text: string) => {
    console.log('[submitAnswer] answering:', text.substring(0, 30))
    setPendingQuestion(null)
    sendDirect(text)
  }, [sendDirect])

  // ── consumeMessage: 后端确认消费后，将用户消息加入对话框并启动流 ──
  const consumeMessageRef = useRef<(text: string) => void>(() => {})
  // 已处理过的 consume 消息 id：防止同一条用户消息被重复消费导致回显两遍
  const consumedIdsRef = useRef<Set<string>>(new Set())

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
        // 设置 ask_user 事件处理：渲染问题卡片
        setAskUserHandler(json => {
          try {
            setPendingQuestion(JSON.parse(json) as AskUserQuestion[])
          } catch { /* ignore parse errors */ }
        })
        // 历史已加载完成则立即接入事件流（重连时恢复）
        sendCreate()
      }

      ws.onmessage = (evt: MessageEvent) => {
        try {
          const msg = JSON.parse(evt.data)
          // created 回执仍是顶层 {type: "created", ...}
          if (msg.type === 'created') {
            // 会话就绪回执：补发暂存的聊天消息，之后可直接发送
            createdRef.current = true
            for (const payload of pendingChatRef.current) {
              ws!.send(payload)
            }
            pendingChatRef.current = []
            return
          }
          // 事件格式：{no, start, offset, blocks: [{type, ...}, ...]}
          const blocks = msg.blocks
          if (!Array.isArray(blocks)) return
          for (const block of blocks) {
            if (block.type === 'User') {
              // 用户消息状态变更（替代旧的 message_sent/queued/consumed）
              const content = block.content?.[0]?.text || ''
              // 稳定消息 ID：sent/queued/consume 三条状态事件共享同一 id（后端 UserBlock.id），
              // 用于队列栏状态迁移与清理；缺失时退回事件 seq
              const msgId = block.id ?? String(msg.start ?? 0)
              if (block.block_user_type === 'queued') {
                // 消息进入等待队列
                setQueuedMessages(prev => [...prev, { id: msgId, status: 'queued' as const }])
              } else if (block.block_user_type === 'consume') {
                // 后端确认消费：同一条消息由 queued 迁移为 consumed，并触发显示 + 启动流
                console.log('[ws] User block (consume):', content.substring(0, 30))
                setQueuedMessages(prev =>
                  prev.map(m => (m.id === msgId ? { ...m, status: 'consumed' as const } : m))
                )
                setTimeout(() => {
                  setQueuedMessages(prev => prev.filter(m => m.id !== msgId))
                }, 600)
                // 同一条用户消息只显示一次：重复的 consume 回执直接忽略
                const consumeKey = String(msgId)
                if (consumedIdsRef.current.has(consumeKey)) {
                  return
                }
                consumedIdsRef.current.add(consumeKey)
                if (content) {
                  consumeMessageRef.current(content)
                }
              } else {
                // sent：消息已被受理，仅更新状态栏，不触发显示
                setQueuedMessages(prev => [...prev, { id: msgId, status: 'consumed' as const }])
                setTimeout(() => {
                  setQueuedMessages(prev => prev.filter(m => m.id !== msgId))
                }, 600)
              }
            }
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
  }, [sendCreate])

  // ── Adapter + Runtime ──
  const adapter = useMemo(() => createStreamingAdapter(), [])

  const queueState = useMemo<MessageQueueState>(() => ({
    queuedMessages,
    queueCount: queuedMessages.filter(m => m.status === 'queued').length,
    thinkingLevel,
    setThinkingLevel,
    sendDirect,
    pendingQuestion,
    submitAnswer,
  }), [queuedMessages, thinkingLevel, sendDirect, pendingQuestion, submitAnswer])

  if (initialMessages === null) {
    return (
      <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', color: '#80868b', fontSize: 14 }}>
        加载聊天记录…
      </div>
    )
  }

  return (
    <MessageQueueContext.Provider value={queueState}>
      <RuntimeGate
        adapter={adapter}
        initialMessages={initialMessages}
        sessionId={sessionId}
        consumeMessageRef={consumeMessageRef}
      >
        {children}
      </RuntimeGate>
    </MessageQueueContext.Provider>
  )
}

/**
 * 历史加载完成后才挂载，在此创建运行时，
 * 保证 useLocalRuntime 的 initialMessages 在创建时即为完整历史
 * （useLocalRuntime 只在创建时读取一次 initialMessages）。
 * 切换会话时父级回到加载态，本组件卸载后随新历史重新挂载。
 */
function RuntimeGate({ adapter, initialMessages, sessionId, consumeMessageRef, children }: {
  adapter: ChatModelAdapter
  initialMessages: { role: 'user' | 'assistant'; content: string }[]
  sessionId: number
  consumeMessageRef: React.MutableRefObject<(text: string) => void>
  children: ReactNode
}) {
  const runtime = useLocalRuntime(adapter, {
    initialMessages,
  })

  // run 进行中到达的用户消息（如 ask_user 回答）先缓冲，run 结束后补显示
  const deferredBuffer = useRef<string[]>([])
  // 记录已在运行中 append 过的消息文本，防止 DeferredFlusher 重复 append
  const appendedDuringRunRef = useRef<Set<string>>(new Set())

  return (
    <AssistantRuntimeProvider runtime={runtime}>
      <SessionResetter sessionId={sessionId} />
      <MessageConsumedHandler consumeMessageRef={consumeMessageRef} deferredBuffer={deferredBuffer} appendedDuringRunRef={appendedDuringRunRef} />
      <DeferredFlusher deferredBuffer={deferredBuffer} appendedDuringRunRef={appendedDuringRunRef} />
      {children}
    </AssistantRuntimeProvider>
  )
}

/**
 * 注册 consumeMessage 回调：将用户消息追加到线程并启动 adapter 流。
 * 必须在 AssistantRuntimeProvider 内部使用（需要 useThreadRuntime）。
 * 插话（AI 运行中）：setSkipNextStop + append → 框架触发 abort 但不向后端发 stop，
 * 插话显示在对话流的正确位置，后端流继续。DeferredFlusher 在 run 结束后启动下一轮。
 */
function MessageConsumedHandler({ consumeMessageRef, deferredBuffer, appendedDuringRunRef }: {
  consumeMessageRef: React.MutableRefObject<(text: string) => void>
  deferredBuffer: React.MutableRefObject<string[]>
  appendedDuringRunRef: React.MutableRefObject<Set<string>>
}) {
  const threadRuntime = useThreadRuntime()
  const isRunning = useThread(t => t.isRunning)
  const isRunningRef = useRef(isRunning)
  isRunningRef.current = isRunning

  useEffect(() => {
    consumeMessageRef.current = (text: string) => {
      console.log('[consumeMessage] appending user message, text:', text.substring(0, 30), 'isRunning:', isRunningRef.current)
      if (isRunningRef.current) {
        // AI 运行中：append 到 thread（正确位置），标记跳过 stop，记录已 append
        setSkipNextStop()
        appendedDuringRunRef.current.add(text)
        threadRuntime.append({
          role: 'user',
          content: [{ type: 'text', text }],
        } as any)
        deferredBuffer.current.push(text)
        return
      }
      // AI 空闲：如果已在运行中 append 过，只启动流不重复 append
      if (appendedDuringRunRef.current.has(text)) {
        appendedDuringRunRef.current.delete(text)
        triggerStream()
        return
      }
      triggerStream()
      threadRuntime.append({
        role: 'user',
        content: [{ type: 'text', text }],
        startRun: true,
      } as any)
    }
  }, [threadRuntime, consumeMessageRef, deferredBuffer, appendedDuringRunRef])

  return null
}

/**
 * run 结束后逐条补显示缓冲的消息（插话 / ask_user 回答）。
 * 已在运行中 append 过的消息（appendedDuringRunRef）只启动流，不重复 append。
 */
function DeferredFlusher({ deferredBuffer, appendedDuringRunRef }: {
  deferredBuffer: React.MutableRefObject<string[]>
  appendedDuringRunRef: React.MutableRefObject<Set<string>>
}) {
  const threadRuntime = useThreadRuntime()
  const isRunning = useThread(t => t.isRunning)

  useEffect(() => {
    if (isRunning || deferredBuffer.current.length === 0) return
    const next = deferredBuffer.current.shift()!
    console.log('[DeferredFlusher] flushing deferred message:', next.substring(0, 30))
    if (appendedDuringRunRef.current.has(next)) {
      // 已在运行中 append 过，只启动流，不重复 append
      appendedDuringRunRef.current.delete(next)
      triggerStream()
      return
    }
    triggerStream()
    threadRuntime.append({
      role: 'user',
      content: [{ type: 'text', text: next }],
      startRun: true,
    } as any)
  }, [isRunning, deferredBuffer, threadRuntime, appendedDuringRunRef])

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
