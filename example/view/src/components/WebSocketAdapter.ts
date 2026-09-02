import type { ChatModelAdapter, ChatModelRunResult } from '@assistant-ui/react'

/**
 * WebSocket streaming adapter for the go-agent-sdk chat protocol.
 *
 * Architecture: send/display separation
 *   - Sending is handled externally via sendDirect() (direct WebSocket write).
 *   - This adapter only consumes streaming events (chunk / thinking / done / error)
 *     and yields them as assistant content.
 *   - The trigger mechanism ensures the adapter starts processing only after
 *     the backend has consumed the user message (User block with block_user_type="consume").
 *
 * Protocol (receive only) — 后端事件统一为 {no, start, offset, blocks: [...]}：
 *   { no, start, offset, blocks: [{ type: "start",          block: { type, ... } }] }  // 流式块开始
 *   { no, start, offset, blocks: [{ type: "delta",          content: "..." }] }        // 流式增量
 *   { no, start, offset, blocks: [{ type: "text",           text: "..." }] }           // 完整文本块
 *   { no, start, offset, blocks: [{ type: "thinking",       thinking: "..." }] }       // 思考块
 *   { no, start, offset, blocks: [{ type: "tool_execution", tool_name, args, output }] }
 *   { no, start, offset, blocks: [{ type: "User",           block_user_type, ... }] }  // 用户消息状态
 *   { no, start, offset, blocks: [{ type: "done" }] }                                  // 本轮结束
 *   { no, start, offset, blocks: [{ type: "error",          text: "..." }] }           // 错误
 *   { no, start, offset, blocks: [{ type: "usage" }] }                                 // 元数据，不产生事件
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
// 插话模式：append 触发框架 abort 时不向后端发 stop
let skipNextStop = false

// 当前流式块类型（由 StartBlock 设置，Delta 据此路由）
let currentStreamBlockType: string | null = null
// 当前 tool_use 的工具名与 id（识别 execute_command，并按 id 关联输出）
let currentToolName: string | null = null
let currentToolUseId: string | null = null
// 当前 tool_use 入参 JSON 累积（用于解析 execute_command 的 command）
let toolInputJson = ''
// tool_use_id → command 映射：工具输出的 start 块携带 tool_use_id，据此取回命令
let commandByToolUseId = new Map<string, string>()
// 当前文本输出对应的命令（非空表示后续 text delta 是该命令的输出）
let activeCommand: string | null = null

// parseCommand 从 tool_use 入参 JSON 中解析 command；解析失败退回原始入参。
function parseCommand(json: string): string {
  try {
    const obj = JSON.parse(json)
    if (typeof obj?.command === 'string' && obj.command) return obj.command
  } catch { /* ignore */ }
  return json.trim()
}

let runCounter = 0  // 调试：追踪第几轮 run

// ── Token 用量跟踪 ──

export interface UsageInfo {
  inputTokens: number
  outputTokens: number
  cacheInputTokens: number
}

let latestUsage: UsageInfo | null = null
let usageListeners: Array<(u: UsageInfo | null) => void> = []

export function setLatestUsage(u: UsageInfo): void {
  // 后端在流开始（output=0）和流结束各发一次 Usage。
  // 非零字段才更新，避免中间态（output=0）覆盖已有完整数据。
  const prev = latestUsage
  latestUsage = {
    inputTokens:      u.inputTokens      || prev?.inputTokens      || 0,
    outputTokens:     u.outputTokens     || prev?.outputTokens     || 0,
    cacheInputTokens: u.cacheInputTokens || prev?.cacheInputTokens || 0,
  }
  for (const cb of usageListeners) cb(latestUsage)
}

/** resetUsage 清除用量数据（新一轮开始时调用）。 */
export function resetUsage(): void {
  latestUsage = null
  for (const cb of usageListeners) cb(null)
}

export function getLatestUsage(): UsageInfo | null {
  return latestUsage
}

export function subscribeUsage(cb: (u: UsageInfo | null) => void): () => void {
  usageListeners.push(cb)
  return () => { usageListeners = usageListeners.filter(l => l !== cb) }
}

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
 * setSkipNextStop 标记下一次 abort 不发送 stop 消息到后端。
 * 用于插话场景：append 触发框架 abort，但不应中断后端当前请求。
 */
export function setSkipNextStop(): void {
  skipNextStop = true
}

/**
 * triggerStream 触发适配器开始处理流事件。
 * 由 ChatRuntimeProvider 在收到 User block (sent/consume) 后调用。
 * 立即安装桥接分发，保证后续事件进入 pendingBuffer（由 adapter run 排空）。
 */
export function triggerStream(): void {
  console.log('[adapter] triggerStream called')
  // 新一轮开始，清除上一轮的 token 用量（本轮 LLM 返回后会重新填充）
  resetUsage()
  // 丢弃触发前缓冲的终结性事件（上一轮残留的 done/error：
  // 当前轮在 message_consumed 之前不可能产生本轮的终结事件）
  pendingBuffer = pendingBuffer.filter(e => e.kind !== 'done' && e.kind !== 'error')
  // 立即安装桥接分发：后续事件进入 pendingBuffer，由 adapter run 排空并接管
  if (!directDispatch) {
    directDispatch = (evt: StreamEvent) => {
      pendingBuffer.push(evt)
    }
  }
  // 若上一轮已消费掉 trigger（triggerResolve 为 null），为本轮重建，
  // 否则本轮的 adapter run 拿不到 trigger，会越过等待直接进入空循环而卡死。
  if (!triggerResolve) {
    pendingTrigger = new Promise<void>(resolve => { triggerResolve = resolve })
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
  resetStreamBlockState()
  console.log('[bridge] setupStreamBridge: trigger created, buffer cleared')

  ws.addEventListener('message', streamHandler)
}

// resetStreamBlockState 重置流式块追踪状态（连接建立 / 一轮结束时调用）。
function resetStreamBlockState(): void {
  currentStreamBlockType = null
  currentToolName = null
  currentToolUseId = null
  toolInputJson = ''
  commandByToolUseId = new Map()
  activeCommand = null
}

function streamHandler(evt: MessageEvent): void {
  try {
    const msg = JSON.parse(evt.data)
    // 后端发送 {no, start, offset, blocks: [{type, ...}, ...]} 格式
    const blocks = msg.blocks
    if (!Array.isArray(blocks)) return
    for (const block of blocks) {
      processBlock(block, msg)
    }
  } catch { /* ignore parse errors */ }
}

function processBlock(block: Record<string, unknown>, msg: Record<string, unknown>): void {
    const blockType = block.type as string
    console.log('[streamHandler] block type:', blockType, 'seq:', msg.start)

    let event: StreamEvent | null = null
    switch (blockType) {
      case 'start': {
        // 流式块开始标记：记录类型，后续 delta 据此路由（thinking vs text）
        const inner = block.block as Record<string, unknown> | undefined
        const innerType = (inner?.type as string) || null
        const innerName = (inner?.name as string) || null
        const innerId = (inner?.id as string) || null
        const toolUseId = (inner?.tool_use_id as string) || null
        const wasToolUse = currentStreamBlockType === 'tool_use'

        // 上一个 execute_command 的入参已收齐：解析命令并按 id 记录
        if (wasToolUse && currentToolName === 'execute_command' && currentToolUseId) {
          const cmd = parseCommand(toolInputJson)
          if (cmd) commandByToolUseId.set(currentToolUseId, cmd)
        }

        if (innerType === 'tool_use') {
          // 新 tool_use 开始：记录 id/name，开始累积入参
          activeCommand = null
          currentToolUseId = innerId
          currentToolName = innerName
          toolInputJson = ''
        } else {
          // 文本/思考块：工具输出携带 tool_use_id，按 id 取命令（无 id 的普通文本则清空）
          activeCommand = toolUseId ? (commandByToolUseId.get(toolUseId) ?? null) : null
          currentToolUseId = null
          currentToolName = null
          toolInputJson = ''
        }
        currentStreamBlockType = innerType
        console.log('[streamHandler] start block, inner type:', innerType, 'name:', innerName)
        return
      }
      case 'delta':
        // 流式增量：按当前块类型路由（tool_use 入参 / thinking / 命令输出 / 文本）
        if (block.content) {
          if (currentStreamBlockType === 'tool_use') {
            // tool_use 入参 JSON：execute_command 累积解析命令，其他工具按文本回显
            if (currentToolName === 'execute_command') {
              toolInputJson += block.content as string
            } else {
              event = { kind: 'chunk', text: block.content as string }
            }
          } else if (currentStreamBlockType === 'thinking') {
            event = { kind: 'thinking', text: block.content as string }
          } else if (activeCommand !== null) {
            // 命令输出阶段的文本增量 → command 事件（前端终端样式渲染）
            event = { kind: 'command', command: activeCommand, output: block.content as string }
          } else {
            event = { kind: 'chunk', text: block.content as string }
          }
        }
        break
      case 'text':
        // 完整文本块（工具输出/错误补充等）：命令输出阶段并入 command，否则 chunk
        if (block.text) {
          if (activeCommand !== null) {
            event = { kind: 'command', command: activeCommand, output: block.text as string }
          } else {
            event = { kind: 'chunk', text: block.text as string }
          }
        }
        break
      case 'thinking':
        // 完整 thinking 块 → thinking
        if (block.thinking) event = { kind: 'thinking', text: block.thinking as string }
        break
      case 'tool_execution':
        // 工具执行事件：tool_name 为工具名，args 为入参，output 为输出
        if (block.tool_name || block.output) {
          event = {
            kind: 'tool_execution',
            name: (block.tool_name as string) || '',
            input: (block.args as string) || '',
            output: (block.output as string) || '',
          }
        }
        break
      case 'done':
        // 不设 done=true：多轮历史事件中每个 DoneBlock 后还有下一轮事件
        resetStreamBlockState()
        console.log('[bridge] done block received')
        return
      case 'error':
        event = { kind: 'error', message: (block.text as string) || (block.message as string) || 'Unknown error' }
        break
      case 'custom_text':
        // CustomTextBlock：按 text_type 路由语义。ask_user = 提问卡片（非流事件）
        if (block.text_type === 'ask_user') {
          console.log('[bridge] ask_user (custom_text) block received')
          if (askUserHandler && block.text) askUserHandler(block.text as string)
          return
        }
        // 其他自定义文本（resource_card / plan_card 等）由业务自行处理
        console.log('[streamHandler] custom_text block:', block.text_type)
        return
      case 'ask_user':
        // 兼容旧格式：ask_user 块（已迁移到 custom_text + text_type=ask_user）
        console.log('[bridge] legacy ask_user block received')
        if (askUserHandler && block.text) askUserHandler(block.text as string)
        return
      case 'message_start':
      case 'message_delta': {
        // token 用量元数据，不产生流事件，但提取用量信息供 UI 展示
        const usage = block.Usage as Record<string, number> | undefined
        if (usage) {
          setLatestUsage({
            inputTokens: usage.input_tokens ?? 0,
            outputTokens: usage.output_tokens ?? 0,
            cacheInputTokens: usage.cache_input_tokens ?? 0,
          })
        }
        return
      }
      case 'User':
        // User block 由 ChatRuntimeProvider 处理，此处跳过
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

      // 结果队列：每轮对话内容独立入队，不会互相覆盖
      const resultQueue: ChatModelRunResult[] = []

      const push = () => {
        const combined = serializeSegments(segments)
        if (combined) {
          resultQueue.push({ content: [{ type: 'text' as const, text: combined }] })
        }
        segments.length = 0
      }

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
        console.log(`[adapter] run #${myRun} abort signal, skipNextStop =`, skipNextStop)
        if (skipNextStop) {
          skipNextStop = false
          // 插话：不发 stop 到后端，但结束当前 adapter run
          directDispatch = null
          done = true
          return
        }
        directDispatch = null
        done = true
        stopCallback?.()
      }
      abortSignal?.addEventListener('abort', onAbort)

      // 4. 流式输出：从队列逐条 yield，done 后排空队列再退出
      while (!done || resultQueue.length > 0) {
        if (resultQueue.length > 0) {
          yield resultQueue.shift()!
        } else {
          await new Promise(r => setTimeout(r, 50))
        }
      }

      // 5. 自然结束时移除 abort 监听，防止框架取消上一轮时误发 stop
      abortSignal?.removeEventListener('abort', onAbort)

      // 6. 排空剩余（确保最终状态被 yield）
      while (resultQueue.length > 0) {
        yield resultQueue.shift()!
      }

      directDispatch = null
      console.log(`[adapter] run #${myRun} finished`)
    },
  }
}
