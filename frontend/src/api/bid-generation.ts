import { get, post } from '@/utils/request'
import type { SessionLastRequestStatePayload } from '@/stores/settings'

export type BidGenerationStatus = 'planning' | 'awaiting_outline' | 'running' | 'awaiting_input' | 'paused' | 'failed' | 'completed' | 'cancelled'
export type BidGenerationTask = {
  id: string
  session_id: string
  status: BidGenerationStatus
  phase: 'planning' | 'writing' | 'exporting' | 'waiting'
  revision: number
  title: string
  sections: { id: string; title: string; status: 'pending' | 'writing' | 'completed'; word_count: number }[]
  current_section: number
  total_words: number
  pending_message_id?: string
  error?: string
  artifact_message_id?: string
  artifact_index?: number
  artifact_file_name?: string
}
type Response<T> = { success: boolean; data: T }
const path = (sessionId: string) => `/api/v1/sessions/${encodeURIComponent(sessionId)}/bid-generation`

export async function startBidGeneration(sessionId: string, body: {
  query: string; request_state?: SessionLastRequestStatePayload; attachment_ids?: string[]
}): Promise<Response<BidGenerationTask>> {
  return post(path(sessionId), body)
}
export async function getBidGeneration(sessionId: string): Promise<Response<BidGenerationTask | null>> {
  return get(path(sessionId))
}
export async function respondBidGeneration(sessionId: string, taskId: string, body: {
  query: string; confirm_outline?: boolean; attachment_ids?: string[]; expected_revision: number
}): Promise<Response<BidGenerationTask>> {
  return post(`${path(sessionId)}/${encodeURIComponent(taskId)}/respond`, body)
}
export async function controlBidGeneration(sessionId: string, taskId: string, body: {
  action: 'pause' | 'resume' | 'cancel'; expected_revision: number
}): Promise<Response<BidGenerationTask>> {
  return post(`${path(sessionId)}/${encodeURIComponent(taskId)}/control`, body)
}
