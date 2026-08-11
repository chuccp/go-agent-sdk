import type { ChatSession } from '../api/chat'

interface Props {
  sessions: ChatSession[]
  activeId: number | null
  onSelect: (session: ChatSession) => void
  onNew: () => void
  onDelete: (id: number) => void
  onSettings: () => void
}

export function Sidebar({ sessions, activeId, onSelect, onNew, onDelete, onSettings }: Props) {
  return (
    <aside className="sidebar">
      {/* ── Header ── */}
      <div className="sidebar-header">
        <button className="new-chat-btn" onClick={onNew}>
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round">
            <line x1="12" y1="5" x2="12" y2="19" />
            <line x1="5" y1="12" x2="19" y2="12" />
          </svg>
          New Chat
        </button>
      </div>

      {/* ── Session List ── */}
      <div className="session-list">
        {sessions.length === 0 ? (
          <div className="session-empty">
            <div className="session-empty-icon">💬</div>
            <div className="session-empty-text">No conversations yet</div>
            <div className="session-empty-hint">Click "New Chat" to start</div>
          </div>
        ) : (
          sessions.map((s) => (
            <div
              key={s.id}
              className={`session-item${s.id === activeId ? ' active' : ''}`}
              onClick={() => onSelect(s)}
            >
              <div className="session-item-main">
                <div className="session-item-title">{s.title}</div>
                <div className="session-item-time">{formatTime(s.updated_at)}</div>
              </div>
              <button
                className="session-item-delete"
                onClick={(e) => {
                  e.stopPropagation()
                  onDelete(s.id)
                }}
                title="Delete"
              >
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
                  <polyline points="3,6 5,6 21,6" />
                  <path d="M19,6v14a2,2,0,0,1-2,2H7a2,2,0,0,1-2-2V6M8,6V4a2,2,0,0,1,2-2h4a2,2,0,0,1,2,2V6" />
                </svg>
              </button>
            </div>
          ))
        )}
      </div>

      {/* ── Footer ── */}
      <div className="sidebar-footer">
        <button className="settings-btn" onClick={onSettings}>
          <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
            <circle cx="12" cy="12" r="3" />
            <path d="M19.4,15a1.65,1.65,0,0,0,.33,1.82l.06.06a2,2,0,0,1,0,2.83,2,2,0,0,1-2.83,0l-.06-.06a1.65,1.65,0,0,0-1.82-.33,1.65,1.65,0,0,0-1,1.51V21a2,2,0,0,1-2,2,2,2,0,0,1-2-2v-.09A1.65,1.65,0,0,0,9,19.4a1.65,1.65,0,0,0-1.82.33l-.06.06a2,2,0,0,1-2.83,0,2,2,0,0,1,0-2.83l.06-.06a1.65,1.65,0,0,0,.33-1.82,1.65,1.65,0,0,0-1.51-1H3a2,2,0,0,1-2-2,2,2,0,0,1,2-2h.09A1.65,1.65,0,0,0,4.6,9a1.65,1.65,0,0,0-.33-1.82l-.06-.06a2,2,0,0,1,0-2.83,2,2,0,0,1,2.83,0l.06.06a1.65,1.65,0,0,0,1.82.33H9a1.65,1.65,0,0,0,1-1.51V3a2,2,0,0,1,2-2,2,2,0,0,1,2,2v.09a1.65,1.65,0,0,0,1,1.51,1.65,1.65,0,0,0,1.82-.33l.06-.06a2,2,0,0,1,2.83,0,2,2,0,0,1,0,2.83l-.06.06a1.65,1.65,0,0,0-.33,1.82V9a1.65,1.65,0,0,0,1.51,1H21a2,2,0,0,1,2,2,2,2,0,0,1-2,2h-.09A1.65,1.65,0,0,0,19.4,15Z" />
          </svg>
          Settings
        </button>
        <span className="sidebar-footer-text">go-agent-sdk debug</span>
      </div>
    </aside>
  )
}

function formatTime(dateStr: string): string {
  const d = new Date(dateStr)
  const now = new Date()
  const diff = now.getTime() - d.getTime()
  const mins = Math.floor(diff / 60000)
  const hours = Math.floor(diff / 3600000)
  const days = Math.floor(diff / 86400000)

  if (mins < 1) return 'Just now'
  if (mins < 60) return `${mins}m ago`
  if (hours < 24) return `${hours}h ago`
  if (days < 7) return `${days}d ago`
  return d.toLocaleDateString()
}
