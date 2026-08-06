import {
  ThreadPrimitive,
  MessagePrimitive,
  ComposerPrimitive,
  useThread,
  useComposerRuntime,
  useComposer,
  useMessage,
} from '@assistant-ui/react'
import Markdown from 'react-markdown'
import { useMessageQueue } from './ChatRuntimeProvider'

export function Thread() {
  return (
    <ThreadPrimitive.Root style={{
      display: 'flex', flexDirection: 'column', height: '100%', background: '#fff',
    }}>
      {/* ── Header ── */}
      <div style={{
        padding: '12px 24px', borderBottom: '1px solid #e8eaed',
        display: 'flex', alignItems: 'center', gap: 8,
        background: '#fafbfc', flexShrink: 0,
      }}>
        <div style={{
          width: 28, height: 28, borderRadius: '50%',
          background: 'linear-gradient(135deg, #4285f4, #34a853)',
          display: 'flex', alignItems: 'center', justifyContent: 'center',
          color: '#fff', fontSize: 14, fontWeight: 700,
        }}>AI</div>
        <span style={{ fontWeight: 600, fontSize: 15, color: '#202124' }}>Agent Debug Chat</span>
        <span style={{ fontSize: 11, color: '#80868b', marginLeft: 4 }}>
          go-agent-sdk · {location.hostname}:19009
        </span>
      </div>

      {/* ── Messages ── */}
      <ThreadPrimitive.Viewport style={{ flex: 1, overflowY: 'auto', padding: '24px 0' }}>
        <div style={{ maxWidth: 820, margin: '0 auto', padding: '0 24px' }}>
          <ThreadPrimitive.Empty>
            <div style={{
              display: 'flex', flexDirection: 'column', alignItems: 'center',
              justifyContent: 'center', minHeight: '40vh', gap: 12,
            }}>
              <div style={{ fontSize: 32, fontWeight: 700, color: '#1a73e8' }}>🐛 Agent Debug</div>
              <div style={{ fontSize: 14, color: '#5f6368' }}>
                Send a message to test the agent — WebSocket at /ws/chat
              </div>
              <div style={{ fontSize: 12, color: '#80868b', fontFamily: 'monospace' }}>
                WS protocol: {'{"type":"chat","message":"..."}'}<br />
                Response: {'{"type":"chunk","content":"..."} → {"type":"done"}'}
              </div>
            </div>
          </ThreadPrimitive.Empty>

          <ThreadPrimitive.Messages
            components={{
              UserMessage,
              AssistantMessage,
            }}
          />
        </div>
      </ThreadPrimitive.Viewport>

      {/* ── Message Queue Indicator ── */}
      <MessageQueueBar />

      {/* ── Ask User Question Card ── */}
      <AskUserCard />

      {/* ── Composer ── */}
      <div style={{ padding: '0 24px 24px', flexShrink: 0 }}>
        <div style={{ maxWidth: 820, margin: '0 auto' }}>
          <Composer />
        </div>
      </div>
    </ThreadPrimitive.Root>
  )
}

// ── Message Queue Bar ──

function MessageQueueBar() {
  const { queuedMessages, queueCount, pendingQueue } = useMessageQueue()

  if (queuedMessages.length === 0 && pendingQueue.length === 0) return null

  return (
    <div style={{
      padding: '8px 24px', flexShrink: 0,
      background: '#fff8e1', borderTop: '1px solid #ffe082',
    }}>
      <div style={{ maxWidth: 820, margin: '0 auto', display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
        <span style={{
          display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
          minWidth: 20, height: 20, borderRadius: 10, padding: '0 6px',
          background: '#f9a825', color: '#fff', fontSize: 11, fontWeight: 700,
        }}>
          {queueCount + pendingQueue.length}
        </span>
        <span style={{ fontSize: 13, color: '#5f6368' }}>
          {pendingQueue.length > 0
            ? `${pendingQueue.length} 条消息排队等待发送…`
            : `${queueCount} 条消息排队等待处理…`}
        </span>
        <div style={{ display: 'flex', gap: 6, marginLeft: 'auto', flexWrap: 'wrap' }}>
          {pendingQueue.map((text, i) => (
            <span key={`p-${i}`} style={{
              display: 'inline-block', padding: '2px 10px', maxWidth: 220,
              borderRadius: 10, fontSize: 11,
              background: '#fff3e0', color: '#e65100', border: '1px solid #ffcc80',
              overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
            }}>
              ⏳ {text}
            </span>
          ))}
          {queuedMessages.map(m => (
            <span key={m.id} style={{
              display: 'inline-block', padding: '2px 8px',
              borderRadius: 10, fontSize: 11, fontFamily: 'monospace',
              background: m.status === 'queued' ? '#fff3e0' : '#e8f5e9',
              color: m.status === 'queued' ? '#e65100' : '#2e7d32',
              border: `1px solid ${m.status === 'queued' ? '#ffcc80' : '#a5d6a7'}`,
              transition: 'all 0.3s ease',
            }}>
              #{m.id} {m.status === 'queued' ? '⏳' : '✓'}
            </span>
          ))}
        </div>
      </div>
    </div>
  )
}

// ── Ask User Question Card（ask_user 工具的问题卡片）──

function AskUserCard() {
  const { pendingQuestion, submitAnswer } = useMessageQueue()
  if (!pendingQuestion || pendingQuestion.length === 0) return null

  return (
    <div style={{
      padding: '14px 24px', flexShrink: 0,
      background: '#e8f0fe', borderTop: '1px solid #aecbfa',
    }}>
      <div style={{ maxWidth: 820, margin: '0 auto', display: 'flex', flexDirection: 'column', gap: 14 }}>
        {pendingQuestion.map((q, qi) => (
          <div key={qi}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
              {q.header && (
                <span style={{
                  padding: '2px 8px', borderRadius: 4, background: '#1a73e8',
                  color: '#fff', fontSize: 11, fontWeight: 600,
                }}>{q.header}</span>
              )}
              <span style={{ fontSize: 14, fontWeight: 500, color: '#202124' }}>{q.question}</span>
            </div>
            <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
              {q.options.map((opt, oi) => (
                <button
                  key={oi}
                  onClick={() => submitAnswer(q.header ? `${q.header}: ${opt.label}` : opt.label)}
                  title={opt.description}
                  style={{
                    padding: '8px 14px', borderRadius: 8,
                    border: '1px solid #1a73e8', background: '#fff', color: '#1a73e8',
                    fontSize: 13, cursor: 'pointer',
                  }}
                >
                  {opt.label}
                </button>
              ))}
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}

// ── User Message Bubble ──

function UserMessage() {
  return (
    <MessagePrimitive.Root style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 24 }}>
      <div style={{ maxWidth: '70%', display: 'flex', alignItems: 'flex-start', gap: 12 }}>
        <div style={{
          padding: '12px 18px', borderRadius: '20px 20px 4px 20px',
          background: '#1a73e8', color: '#fff', fontSize: 14, lineHeight: 1.7,
          wordBreak: 'break-word',
        }}>
          <MessagePrimitive.Content />
        </div>
        <div style={{
          width: 34, height: 34, borderRadius: '50%', background: '#e8f0fe',
          display: 'flex', alignItems: 'center', justifyContent: 'center',
          color: '#1a73e8', fontSize: 13, fontWeight: 600, flexShrink: 0,
        }}>U</div>
      </div>
    </MessagePrimitive.Root>
  )
}

// ── Assistant Message (agent-style, full width, no bubble) ──

interface Segment {
  type: 'think' | 'tool' | 'result' | 'text'
  content: string
}

/** 解析内容中的类型标记，拆分为不同类型的片段 */
function parseSegments(raw: string): Segment[] {
  const segments: Segment[] = []
  const regex = /⟪(think|tool|result)⟫([\s\S]*?)⟪\/\1⟫/g
  let lastIndex = 0
  let match: RegExpExecArray | null

  while ((match = regex.exec(raw)) !== null) {
    // 标记前的普通文本
    if (match.index > lastIndex) {
      const text = raw.slice(lastIndex, match.index).trim()
      if (text) segments.push({ type: 'text', content: text })
    }
    segments.push({ type: match[1] as Segment['type'], content: match[2].trim() })
    lastIndex = regex.lastIndex
  }
  // 剩余文本
  if (lastIndex < raw.length) {
    const text = raw.slice(lastIndex).trim()
    if (text) segments.push({ type: 'text', content: text })
  }
  return segments
}

const segmentStyles: Record<string, React.CSSProperties> = {
  think: {
    margin: '8px 0', padding: '8px 14px',
    borderLeft: '2px solid #d1c4e9', borderRadius: 4,
    background: '#faf5ff', fontSize: 12.5, lineHeight: 1.6,
    color: '#8b5cf6', fontStyle: 'italic',
    maxHeight: 100, overflowY: 'auto',
  },
  tool: {
    margin: '6px 0', padding: '6px 12px',
    borderLeft: '3px solid #42a5f5', borderRadius: 4,
    background: '#e3f2fd', fontSize: 12.5,
    fontFamily: "'SF Mono', 'Fira Code', monospace",
    color: '#1565c0',
  },
  result: {
    margin: '6px 0', padding: '6px 12px',
    borderLeft: '3px solid #66bb6a', borderRadius: 4,
    background: '#e8f5e9', fontSize: 12.5,
    fontFamily: "'SF Mono', 'Fira Code', monospace",
    color: '#2e7d32', whiteSpace: 'pre-wrap',
  },
}

function AssistantMessage() {
  const content = useMessage((m) => m.content)

  // 提取纯文本
  const raw = content
    .map((part) => (part.type === 'text' ? part.text : ''))
    .join('')

  const segments = parseSegments(raw)
  const hasMarkers = segments.some(s => s.type !== 'text')

  return (
    <MessagePrimitive.Root style={{ display: 'flex', justifyContent: 'flex-start', marginBottom: 32 }}>
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: 14, width: '100%' }}>
        <div style={{
          width: 30, height: 30, borderRadius: '50%',
          background: 'linear-gradient(135deg, #4285f4, #34a853)',
          display: 'flex', alignItems: 'center', justifyContent: 'center',
          color: '#fff', fontSize: 14, fontWeight: 700, flexShrink: 0,
          marginTop: 2,
        }}>AI</div>
        <div className="aui-assistant-message" style={{
          fontSize: 14.5, lineHeight: 1.8, color: '#1f2937',
          wordBreak: 'break-word', flex: 1, minWidth: 0,
        }}>
          {hasMarkers ? (
            segments.map((seg, i) =>
              seg.type === 'text' ? (
                <Markdown key={i}>{seg.content}</Markdown>
              ) : (
                <div key={i} style={segmentStyles[seg.type]}>
                  {seg.type === 'think' ? '💭 ' : seg.type === 'tool' ? '🔧 ' : '↳ '}
                  {seg.content}
                </div>
              )
            )
          ) : (
            <MessagePrimitive.Content />
          )}
        </div>
      </div>
    </MessagePrimitive.Root>
  )
}

// ── Composer (input bar) ──

function Composer() {
  const isRunning = useThread((state) => state.isRunning)
  const composerRuntime = useComposerRuntime()
  const text = useComposer((c) => c.text)
  const { queueSend, thinkingLevel, setThinkingLevel, sendDirect } = useMessageQueue()

  const hasText = text.trim().length > 0
  // AI 输出中且已输入文字 → 显示发送键（加入队列）；AI 输出中但未输入 → 停止键
  const showQueueSend = isRunning && hasText

  const handleDirectSend = () => {
    const t = text.trim()
    if (!t) return
    sendDirect(t)
    composerRuntime.setText('')
  }

  const handleQueueSend = () => {
    const t = text.trim()
    if (!t) return
    queueSend(t)
    composerRuntime.setText('')
  }

  return (
    <div>
      <ComposerPrimitive.Root style={{
        display: 'flex', alignItems: 'flex-end', borderRadius: 24,
        border: '1px solid #dadce0', background: '#fff',
        padding: '8px 8px 8px 16px',
        transition: 'border-color 0.15s, box-shadow 0.15s',
        boxShadow: '0 2px 6px rgba(0,0,0,0.06)',
      }}>
        <ComposerPrimitive.Input
          style={{
            flex: 1, border: 'none', outline: 'none', fontSize: 15,
            lineHeight: '24px', resize: 'none', fontFamily: 'inherit',
            padding: '6px 8px', background: 'transparent', color: '#202124',
          }}
          placeholder="Type a message... (Enter to send)"
          rows={1}
          onKeyDown={(e: React.KeyboardEvent<HTMLTextAreaElement>) => {
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault()
              e.stopPropagation()
              if (isRunning) {
                handleQueueSend()
              } else {
                handleDirectSend()
              }
            }
          }}
        />

        {isRunning ? (
          showQueueSend ? (
            <button
              onClick={handleQueueSend}
              style={{
                width: 36, height: 36, borderRadius: '50%', border: 'none',
                background: '#f9a825', color: '#fff', cursor: 'pointer',
                display: 'flex', alignItems: 'center', justifyContent: 'center',
                flexShrink: 0,
              }}
              title="加入队列发送"
            >
              <svg width="18" height="18" viewBox="0 0 24 24" fill="none">
                <path d="M22 2L11 13" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"/>
                <path d="M22 2L15 22L11 13L2 9L22 2Z" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"/>
              </svg>
            </button>
          ) : (
            <button
              onClick={() => composerRuntime.cancel()}
              style={{
                width: 36, height: 36, borderRadius: '50%', border: 'none',
                background: '#ea4335', color: '#fff', cursor: 'pointer',
                display: 'flex', alignItems: 'center', justifyContent: 'center',
                flexShrink: 0,
              }}
              title="Stop"
            >
              <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor">
                <rect x="6" y="6" width="12" height="12" rx="2" />
              </svg>
            </button>
          )
        ) : (
          <button
            onClick={handleDirectSend}
            style={{
              width: 36, height: 36, borderRadius: '50%', border: 'none',
              background: '#1a73e8', color: '#fff', cursor: 'pointer',
              display: 'flex', alignItems: 'center', justifyContent: 'center',
              flexShrink: 0,
            }}
            title="发送"
          >
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none">
              <path d="M22 2L11 13" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"/>
              <path d="M22 2L15 22L11 13L2 9L22 2Z" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"/>
            </svg>
          </button>
        )}
      </ComposerPrimitive.Root>

      <div style={{
        display: 'flex', alignItems: 'center', marginTop: 6, paddingLeft: 4, gap: 10,
      }}>
        <ThinkingSelector value={thinkingLevel} onChange={setThinkingLevel} />
        <span style={{ fontSize: 11, color: '#80868b' }}>Enter 发送 · AI 回复中可继续输入排队</span>
        <span style={{ marginLeft: 'auto', fontSize: 10, color: '#dadce0', fontFamily: 'monospace' }}>
          /ws/chat
        </span>
      </div>
    </div>
  )
}

// ── Thinking Level Selector ──

const THINKING_LEVELS = [
  { value: 'off', label: '关闭' },
  { value: 'low', label: '低' },
  { value: 'medium', label: '中' },
  { value: 'high', label: '高' },
]

function ThinkingSelector({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
      <span style={{ fontSize: 11, color: '#80868b' }}>💭 思考</span>
      <div style={{
        display: 'inline-flex', borderRadius: 12, overflow: 'hidden',
        border: '1px solid #dadce0', background: '#fff',
      }}>
        {THINKING_LEVELS.map(l => (
          <button
            key={l.value}
            onClick={() => onChange(l.value)}
            style={{
              border: 'none', cursor: 'pointer', padding: '2px 10px',
              fontSize: 11, lineHeight: '18px', fontFamily: 'inherit',
              background: value === l.value ? '#8b5cf6' : 'transparent',
              color: value === l.value ? '#fff' : '#5f6368',
              transition: 'all 0.15s',
            }}
          >
            {l.label}
          </button>
        ))}
      </div>
    </div>
  )
}
