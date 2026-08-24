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
    // 跳过元数据块：token 统计、User 消息状态、Start/Delta 流式标记
    if (b.type === 'message_start' || b.type === 'message_delta' || b.type === 'User' || b.type === 'start' || b.type === 'delta' || b.type === 'done') continue
    if (b.type === 'thinking' && b.thinking) {
      parts.push(`⟪think⟫${b.thinking}⟪/think⟫`)
    } else if (b.type === 'text' && b.text) {
      parts.push(b.text)
    } else if (b.type === 'tool_use' && b.name) {
      // execute_command 由 buildDisplayMessages 统一产出 command 标记（与 tool_result 合并）
      if (b.name === 'execute_command') continue
      const input = b.input as Record<string, unknown> | undefined
      const cmd = input?.command ? String(input.command) : JSON.stringify(input)
      parts.push(`⟪tool⟫${cmd}⟪/tool⟫`)
    }
  }
  return parts.join('\n\n')
}

/** extractExecCommand 提取消息中最后一个 execute_command 调用的命令（无则返回 null）。 */
function extractExecCommand(blocks: ContentBlock[]): string | null {
  const lastToolUse = [...blocks].reverse().find(b => b.type === 'tool_use')
  if (lastToolUse?.name !== 'execute_command') return null
  const input = lastToolUse.input as Record<string, unknown> | undefined
  return input?.command ? String(input.command) : ''
}

function buildDisplayMessages(msgs: ChatMessage[]): { role: 'user' | 'assistant'; content: string }[] {
  const result: { role: 'user' | 'assistant'; content: string }[] = []
  // 上一个 execute_command 的命令：将其 tool_result 输出折入同一终端块（与实时流展示一致）
  let pendingCommand: string | null = null
  for (const m of msgs) {
    if (m.role !== 'user' && m.role !== 'assistant') continue
    const blocks = parseBlocks(m.content)
    let text: string
    let role: 'user' | 'assistant'
    if (isToolResult(blocks)) {
      role = 'assistant'
      const content = toolResultToText(blocks).trim()
      if (pendingCommand !== null) {
        // execute_command 的结果：命令 + 输出合并为一个 command 标记
        text = content ? `⟪command⟫${pendingCommand}\n${content}⟪/command⟫` : ''
        pendingCommand = null
      } else {
        text = content ? `⟪result⟫${content}⟪/result⟫` : ''
      }
    } else {
      role = m.role as 'user' | 'assistant'
      text = blocksToText(blocks)
      // 记录最后一个 execute_command 调用，等待其 tool_result；
      // 若本轮没有结果到达（执行中/历史截断），先补一个仅含命令的终端块
      const cmd = extractExecCommand(blocks)
      if (cmd !== null) {
        if (pendingCommand !== null) {
          text = text ? `${text}\n\n⟪command⟫${pendingCommand}\n⟪/command⟫` : `⟪command⟫${pendingCommand}\n⟪/command⟫`
        }
        pendingCommand = cmd
      }
    }
    if (!text.trim()) continue
    const last = result[result.length - 1]
    if (last && last.role === 'assistant' && role === 'assistant') {
      last.content += '\n\n' + text
    } else {
      result.push({ role, content: text })
    }
  }
  // 末尾兜底：最后一个 execute_command 的结果未到达（执行中），补仅含命令的终端块
  if (pendingCommand !== null) {
    const marker = `⟪command⟫${pendingCommand}\n⟪/command⟫`
    const last = result[result.length - 1]
    if (last && last.role === 'assistant') {
      last.content += '\n\n' + marker
    } else {
      result.push({ role: 'assistant', content: marker })
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
  pendingQueue: string[]
  /** 将消息加入客户端队列（AI 忙碌时调用） */
  queueSend: (text: string) => void
  shiftPending: () => void
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
  pendingQueue: [],
  queueSend: () => {},
  shiftPending: () => {},
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
    // 切换会话时重置，等待新历史加载后再重新发送 create
    startRef.current = null
    createdRef.current = false
    pendingChatRef.current = []
    setPendingQuestion(null)
    setInitialMessages(null) // 回到加载态，运行时将随新历史重建
    getSessionMessages(sessionId)
      .then(msgs => {
        if (cancelled) return
        // 事件流从最后一条历史消息之后的位置继续；无历史则从 0 开始
        const last = msgs[msgs.length - 1]
        startRef.current = last ? last.start + last.offset : 0
        setInitialMessages(buildDisplayMessages(msgs))
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

  // 消息队列
  const [queuedMessages, setQueuedMessages] = useState<QueuedMessage[]>([])
  const [pendingQueue, setPendingQueue] = useState<string[]>([])
  const queueSend = useCallback((text: string) => {
    setPendingQueue(prev => [...prev, text])
  }, [])
  const shiftPending = useCallback(() => {
    setPendingQueue(prev => prev.slice(1))
  }, [])

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
    pendingQueue,
    queueSend,
    shiftPending,
    thinkingLevel,
    setThinkingLevel,
    sendDirect,
    pendingQuestion,
    submitAnswer,
  }), [queuedMessages, pendingQueue, queueSend, shiftPending, thinkingLevel, sendDirect, pendingQuestion, submitAnswer])

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

  return (
    <AssistantRuntimeProvider runtime={runtime}>
      <SessionResetter sessionId={sessionId} />
      <MessageConsumedHandler consumeMessageRef={consumeMessageRef} deferredBuffer={deferredBuffer} />
      <DeferredFlusher deferredBuffer={deferredBuffer} />
      <PendingSendWatcher />
      {children}
    </AssistantRuntimeProvider>
  )
}

/**
 * 注册 consumeMessage 回调：将用户消息追加到线程并启动 adapter 流。
 * 必须在 AssistantRuntimeProvider 内部使用（需要 useThreadRuntime）。
 * ask_user 场景：回答的 message_consumed 到达时当前 run 仍在进行（工具轮未结束），
 * 此时不能调用 append（assistant-ui 会中止当前 run 触发 abort → 误发 stop），
 * 先缓冲到 deferredBuffer，由 DeferredFlusher 在 run 结束后补显示。
 */
function MessageConsumedHandler({ consumeMessageRef, deferredBuffer }: {
  consumeMessageRef: React.MutableRefObject<(text: string) => void>
  deferredBuffer: React.MutableRefObject<string[]>
}) {
  const threadRuntime = useThreadRuntime()
  const isRunning = useThread(t => t.isRunning)
  const isRunningRef = useRef(isRunning)
  isRunningRef.current = isRunning

  useEffect(() => {
    consumeMessageRef.current = (text: string) => {
      console.log('[consumeMessage] appending user message, text:', text.substring(0, 30), 'isRunning:', isRunningRef.current)
      if (isRunningRef.current) {
        // ask_user 回答：run 进行中不可 append（会中止当前 run），缓冲待 run 结束后补显示
        deferredBuffer.current.push(text)
        return
      }
      triggerStream()
      // 使用 append 的 startRun 选项，一步完成：追加消息 + 启动 adapter
      threadRuntime.append({
        role: 'user',
        content: [{ type: 'text', text }],
        startRun: true,
      } as any)
    }
  }, [threadRuntime, consumeMessageRef, deferredBuffer])

  return null
}

/**
 * run 结束后逐条补显示缓冲的消息（ask_user 回答等）。
 * 仅追加显示，不启动新 run：后端该轮已结束，无新流事件，
 * 启动 run 会永久悬挂（isRunning 永真）。
 */
function DeferredFlusher({ deferredBuffer }: { deferredBuffer: React.MutableRefObject<string[]> }) {
  const threadRuntime = useThreadRuntime()
  const isRunning = useThread(t => t.isRunning)

  useEffect(() => {
    if (isRunning || deferredBuffer.current.length === 0) return
    const next = deferredBuffer.current.shift()!
    console.log('[DeferredFlusher] flushing deferred message:', next.substring(0, 30))
    threadRuntime.append({
      role: 'user',
      content: [{ type: 'text', text: next }],
    } as any)
  }, [isRunning, deferredBuffer, threadRuntime])

  return null
}

/**
 * 空闲时自动发送客户端队列中的下一条消息。
 * ask_user 等待回答期间（pendingQuestion 非空）也允许发送，
 * 排队消息可作为回答投递，避免死锁。
 */
function PendingSendWatcher() {
  const isRunning = useThread(t => t.isRunning)
  const { pendingQueue, shiftPending, sendDirect, pendingQuestion } = useMessageQueue()

  // 使用 ref 确保 effect 内始终能访问最新的函数
  const shiftRef = useRef(shiftPending)
  shiftRef.current = shiftPending
  const sendRef = useRef(sendDirect)
  sendRef.current = sendDirect
  const queueRef = useRef(pendingQueue)
  queueRef.current = pendingQueue
  const questionRef = useRef(pendingQuestion)
  questionRef.current = pendingQuestion

  useEffect(() => {
    console.log('[PendingSendWatcher] isRunning:', isRunning, 'queueLen:', queueRef.current.length, 'pendingQuestion:', !!questionRef.current)
    if (queueRef.current.length === 0) return
    if (isRunning && !questionRef.current) return
    const next = queueRef.current[0]
    console.log('[PendingSendWatcher] auto-sending:', next.substring(0, 30))
    shiftRef.current()
    sendRef.current(next)
  }, [isRunning, pendingQuestion])

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
