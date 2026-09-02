export interface ChatSession {
  id: number
  title: string
  created_at: string
  updated_at: string
}

// Event 与 WebSocket 推送格式一致：{no, start, offset, blocks: [...]}
export interface ChatEvent {
  no: number
  start: number
  offset: number
  blocks: Record<string, unknown>[]
}

const API_BASE = ''

export async function request<T>(url: string, options?: RequestInit): Promise<T> {
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

export async function getSessionEvents(id: number, since?: number): Promise<ChatEvent[]> {
  const params = new URLSearchParams()
  if (since !== undefined) params.set('since', String(since))
  const qs = params.toString()
  return request<ChatEvent[]>(`/api/chat/sessions/${id}/messages${qs ? '?' + qs : ''}`)
}
