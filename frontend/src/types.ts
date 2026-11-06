export interface Address {
  name?: string
  addr: string
}

export interface Endpoint {
  host: string
  port: number
  security: 'tls' | 'starttls'
}

export type AccountStatus = 'pending' | 'ok' | 'syncing' | 'error' | 'auth_error' | 'paused'

export interface Account {
  id: number
  provider: string
  email: string
  displayName: string
  senderName: string
  color: string
  imap: Endpoint
  smtp: Endpoint
  authType: 'password' | 'oauth2'
  username: string
  hasPassword: boolean
  oauthConnected: boolean
  oauthExpiresAt?: number
  hasCustomCa: boolean
  signatureHtml: string
  isDefault: boolean
  syncEnabled: boolean
  syncIntervalSec: number
  idleEnabled: boolean
  smtpSavesSent: boolean
  status: AccountStatus
  lastError?: string
  lastErrorAt?: number
  lastSyncAt?: number
  lastSyncOkAt?: number
}

export interface AccountGroup {
  id: string
  name: string
  accounts: number[]
  collapsed?: boolean
}

export interface AccountLayout {
  ungrouped: number[]
  groups: AccountGroup[]
  /** Accounts whose folder list is folded in the sidebar. */
  collapsedAccounts?: number[]
}

/** Saved IMAP/SMTP configuration, reused when adding an account (no credentials). */
export interface ServerPreset {
  id: string
  name: string
  imap: Endpoint
  smtp: Endpoint
  tlsCaPem?: string
}

export interface Provider {
  id: string
  name: string
  domains?: string[]
  imap: Endpoint
  smtp: Endpoint
  authMethods: ('password' | 'oauth2')[]
  smtpSavesSent: boolean
  passwordHint?: string
  helpUrl?: string
  oauthConfigured: boolean
}

export interface Folder {
  id: number
  accountId: number
  name: string
  delimiter: string
  role: string
  selectable: boolean
  subscribed: boolean
  syncEnabled: boolean
  total: number
  backfillDone: boolean
  lastSyncAt: number
  unread: number
  local: number
}

export interface Message {
  id: number
  accountId: number
  folderId: number
  uid: number
  messageId: string
  inReplyTo?: string
  threadId: number
  subject: string
  from: Address
  to: Address[]
  cc: Address[]
  bcc?: Address[]
  replyTo?: Address[]
  date: number
  internalDate: number
  size: number
  seen: boolean
  flagged: boolean
  answered: boolean
  draft: boolean
  forwarded: boolean
  hasAttachments: boolean
  priority: number
  snippet: string
  bodyFetched: boolean
}

export interface Attachment {
  id: number
  filename: string
  contentType: string
  size: number
  contentId?: string
  inline: boolean
}

export interface MessageDetail {
  message: Message
  folder: Folder
  attachments: Attachment[]
  text: string
  hasHtml: boolean
  remoteContent: number
  thread: Message[]
  quoteHtml?: string
}

export interface Upload {
  id: string
  filename: string
  contentType: string
  size: number
}

export interface Draft {
  id: number
  accountId: number
  mode: ComposeMode
  relatedMessageId?: number
  to: Address[]
  cc: Address[]
  bcc: Address[]
  subject: string
  bodyHtml: string
  bodyText: string
  isHtml: boolean
  uploads: string[]
  keepOriginalAttachments: boolean
  updatedAt: number
}

export type ComposeMode = 'new' | 'reply' | 'replyall' | 'forward'

export type OutboxStatus = 'scheduled' | 'sending' | 'retry' | 'sent' | 'failed' | 'ambiguous' | 'canceled'

export interface OutboxItem {
  id: number
  accountId: number
  status: OutboxStatus
  messageId: string
  subject: string
  from: string
  recipients: string[]
  attempts: number
  nextAttemptAt: number
  lastError?: string
  sentAt?: number
  createdAt: number
}

export interface Notification {
  id: number
  accountId?: number
  kind: string
  title: string
  body: string
  createdAt: number
  readAt?: number
}

export type Settings = Record<string, string>

export interface Me {
  authenticated: boolean
  appName: string
  version: string
  /** Set when an administrator uploaded a custom logo. */
  logoVersion?: number
  setupRequired?: boolean
  user?: { id: number; username: string; isAdmin: boolean }
  csrfToken?: string
  limits?: { maxAttachmentBytes: number; maxMessageBytes: number; undoSendSeconds: number; maxAccounts: number }
  oauth?: Record<string, boolean>
}

export interface Counters {
  unifiedUnread: number
  starred: number
  accounts: Record<string, number>
}

export interface Page<T> {
  items: T[]
  nextCursor?: string
  warning?: string
  server?: boolean
}
