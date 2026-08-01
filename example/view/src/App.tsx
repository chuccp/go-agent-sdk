import { useState, useEffect, useCallback } from 'react'
import { ChatRuntimeProvider } from './components/ChatRuntimeProvider'
import { Thread } from './components/Thread'
import { Sidebar } from './components/Sidebar'
import type { ChatSession } from './api/chat'
import { listSessions, createSession, deleteSession } from './api/chat'

export default function App() {
  const [sessions, setSessions] = useState<ChatSession[]>([])
  const [activeId, setActiveId] = useState<number | null>(null)

  // Load sessions on mount
  const refresh = useCallback(async () => {
    try {
      const list = await listSessions()
      setSessions(list)
    } catch {
      // Server may not be running yet — silently ignore
    }
  }, [])

  useEffect(() => {
    refresh()
  }, [refresh])

  // Auto-select first session if none selected
  useEffect(() => {
    if (activeId === null && sessions.length > 0) {
      setActiveId(sessions[0].id)
    }
  }, [sessions, activeId])

  const handleNew = useCallback(async () => {
    try {
      const session = await createSession()
      setSessions((prev) => [session, ...prev])
      setActiveId(session.id)
    } catch (err) {
      console.error('Failed to create session:', err)
    }
  }, [])

  const handleSelect = useCallback((session: ChatSession) => {
    setActiveId(session.id)
  }, [])

  const handleDelete = useCallback(async (id: number) => {
    try {
      await deleteSession(id)
      setSessions((prev) => prev.filter((s) => s.id !== id))
      if (activeId === id) {
        setActiveId(null)
      }
    } catch (err) {
      console.error('Failed to delete session:', err)
    }
  }, [activeId])

  return (
    <div className="app-layout">
      <Sidebar
        sessions={sessions}
        activeId={activeId}
        onSelect={handleSelect}
        onNew={handleNew}
        onDelete={handleDelete}
      />
      <main className="main-area">
        {activeId !== null ? (
          <ChatRuntimeProvider key={activeId} sessionId={activeId}>
            <Thread />
          </ChatRuntimeProvider>
        ) : (
          <div className="no-session">
            <div className="no-session-icon">🐛</div>
            <div className="no-session-title">Agent Debug Chat</div>
            <div className="no-session-hint">Create a new chat or select one from the sidebar</div>
            <button className="no-session-btn" onClick={handleNew}>
              New Chat
            </button>
          </div>
        )}
      </main>
    </div>
  )
}
