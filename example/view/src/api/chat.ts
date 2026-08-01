export interface ChatSession {
  id: number
  title: string
  created_at: string
  updated_at: string
}

export interface ChatMessage {
  id: number
  session_id: number
  role: string
  content: string
  start: number
  offset: number
  tool_calls?: string
  tool_results?: string
  created_at: string
}

const API_BASE = ''

async function request<T>(url: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${API_BASE}${url}`, {
    headers: { 'Content-Type': 'application/json' },
    ...options,
  })
  if (!res.ok) {
    throw new Error(`HTTP ${res.status}: ${res.statusText}`)
  }
  const json = await res.json()
  if (json.code !== 200) {
    throw new Error(json.msg || 'API error')
  }
  return json.data as T
}

export async function listSessions(): Promise<ChatSession[]> {
  return request<ChatSession[]>('/api/chat/sessions')
}

export async function createSession(title?: string): Promise<ChatSession> {
  return request<ChatSession>('/api/chat/sessions', {
    method: 'POST',
    body: JSON.stringify({ title: title || 'New Chat' }),
  })
}

export async function deleteSession(id: number): Promise<void> {
  await request<void>(`/api/chat/sessions/${id}`, { method: 'DELETE' })
}

export async function getSessionMessages(id: number): Promise<ChatMessage[]> {
  return request<ChatMessage[]>(`/api/chat/sessions/${id}/messages`)
}
