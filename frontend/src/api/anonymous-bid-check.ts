import { postUpload } from '@/utils/request'
import { uploadTimeoutMs } from '@/utils/requestTimeouts'

export type AnonymousBidCheckStatus = 'fail' | 'review' | 'pass'
export type AnonymousBidCheckScope = 'technical' | 'business' | 'all'

export interface AnonymousBidCheckItem {
  id: string
  category: string
  title: string
  status: AnonymousBidCheckStatus
  message: string
  requirement: string
  source_file: string
  source_page?: number
  location: string
  excerpt: string
  suggestion: string
}

export interface AnonymousBidRule {
  id: string
  category: string
  requirement: string
  source_file: string
  source_page?: number
}

export interface AnonymousBidCheckReport {
  status: AnonymousBidCheckStatus
  tender_file: string
  bid_file: string
  scope: AnonymousBidCheckScope
  checks: AnonymousBidCheckItem[]
  rules: AnonymousBidRule[]
  limitations: string[]
  summary: { failed: number; review: number; passed: number }
  checked_at: string
}

export interface AnonymousBidCheckRequest {
  tenderFile: File
  bidFile: File
  scope: AnonymousBidCheckScope
  identityKeywords: string[]
}

export async function checkAnonymousBid(
  request: AnonymousBidCheckRequest,
  signal?: AbortSignal,
): Promise<AnonymousBidCheckReport> {
  const form = new FormData()
  form.append('tender_file', request.tenderFile)
  form.append('bid_file', request.bidFile)
  form.append('scope', request.scope)
  form.append('identity_keywords', JSON.stringify(request.identityKeywords))
  // Budget for transferring both large files plus the server's analysis window.
  const response = await postUpload('/api/v1/anonymous-bid-check', form, undefined, { timeout: uploadTimeoutMs(form) + 150000, signal })
  if (!response.success || !response.data) throw new Error(response.message || '未能完成暗标检查，请重试。')
  return response.data
}
