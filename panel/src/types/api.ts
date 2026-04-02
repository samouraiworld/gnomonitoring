/* ═══════════════════════════════════════════════════════════════════
   TypeScript interfaces — exact match to Go backend structs
   Source: api-admin.go, db_init.go, db_admin.go
   ═══════════════════════════════════════════════════════════════════ */

// ── Status endpoint ──────────────────────────────────────────────
export interface ChainStatus {
  chain_id: string
  height: number
  active_alerts: number
  critical_alerts: number
  goroutine_active: boolean
}

export interface StatusResponse {
  chains: ChainStatus[]
  total_users: number
  total_webhooks: number
  total_telegram_chats: number
}

// ── Chain management ─────────────────────────────────────────────
export interface ChainInfo {
  id: string
  rpc_endpoint: string
  graphql: string
  gnoweb: string
  enabled: boolean
  goroutine_active: boolean
  height: number
}

export interface ChainCreatePayload {
  id: string
  rpc_endpoint: string
  graphql: string
  gnoweb: string
  enabled: boolean
}

export interface ChainUpdatePayload {
  rpc_endpoint?: string
  graphql?: string
  gnoweb?: string
  enabled?: boolean
}

// ── Alert logs ───────────────────────────────────────────────────
export interface AlertLog {
  ID: number
  chain_id: string
  addr: string
  moniker: string
  level: 'WARNING' | 'CRITICAL' | 'RESOLVED' | 'MUTED' | string
  start_height: number
  end_height: number
  skipped: boolean
  msg: string
  sent_at: string
}

// ── Moniker ──────────────────────────────────────────────────────
export interface AddrMoniker {
  chain_id: string
  addr: string
  moniker: string
  first_active_block: number
}

// ── User ─────────────────────────────────────────────────────────
export interface User {
  ID: number
  user_id: string
  name: string
  email: string
  created_at: string
}

// ── Webhooks ─────────────────────────────────────────────────────
export interface WebhookAdmin {
  id: number
  user_id: string
  url: string
  type: string
  description: string
  chain_id: string | null
  kind: 'validator' | 'govdao'
}

// ── Telegram ─────────────────────────────────────────────────────
export interface TelegramChat {
  ID: number
  chat_id: number
  chain_id: string
  chat_type: string
  chat_title: string
}

export interface TelegramSub {
  ID: number
  chat_id: number
  chain_id: string
  addr: string
  moniker: string
  activate: boolean
}

export interface TelegramSchedule {
  ID: number
  chat_id: number
  chain_id: string
  daily_report_hour: number
  daily_report_minute: number
  timezone: string
  activate: boolean
}

// ── Hour Reports (web) ───────────────────────────────────────────
export interface HourReport {
  ID: number
  user_id: string
  daily_report_hour: number
  daily_report_minute: number
  timezone: string
}

// ── GovDAO proposals ─────────────────────────────────────────────
export interface GovdaoProposal {
  Id: number
  ChainID: string
  Url: string
  Title: string
  Tx: string
  Status: string
}

// ── Generic API response ─────────────────────────────────────────
export interface ApiStatus {
  status: string
  id?: string
  user_id?: string
  chain?: string
}
