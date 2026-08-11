import { request } from './chat'

export interface FlowInfo {
  id: string
  name: string
}

export async function listFlows(): Promise<FlowInfo[]> {
  return request<FlowInfo[]>('/api/flows')
}
