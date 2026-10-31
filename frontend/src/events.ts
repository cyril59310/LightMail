// Server-Sent Events: real-time updates pushed by the backend.
import { t } from './i18n'
import { bumpLists, getState, loadAccounts, loadCounters, loadFolders, loadOutbox, setState, toast } from './store'
import type { Notification, OutboxItem } from './types'
import { debounce } from './util'

let source: EventSource | null = null

const refreshCounters = debounce(() => void loadCounters().catch(() => {}), 500)
const refreshFolders = debounce(() => void loadFolders().catch(() => {}), 500)
const refreshLists = debounce(() => bumpLists(), 400)
const refreshAccounts = debounce(() => void loadAccounts().catch(() => {}), 300)

function browserNotify(title: string, body: string) {
  if (getState().settings.browserNotifications !== 'true') return
  if (!('Notification' in window) || Notification.permission !== 'granted') return
  if (document.visibilityState === 'visible' && document.hasFocus()) return
  try {
    const n = new Notification(title, { body, tag: 'lightmail', icon: '/favicon.svg' })
    n.onclick = () => {
      window.focus()
      n.close()
    }
  } catch {
    /* unsupported */
  }
}

export function connectEvents() {
  if (source) return
  source = new EventSource('/api/v1/events', { withCredentials: true })
  source.addEventListener('messages', () => {
    refreshLists()
    refreshCounters()
    refreshFolders()
  })
  source.addEventListener('folders', () => refreshFolders())
  source.addEventListener('account', (e) => {
    try {
      const d = JSON.parse((e as MessageEvent).data) as { id: number; status: string; lastError?: string; lastSyncOkAt?: number }
      setState((s) => ({
        accounts: s.accounts.map((a) => (a.id === d.id ? { ...a, status: d.status as never, lastError: d.lastError, lastSyncOkAt: d.lastSyncOkAt } : a)),
      }))
    } catch {
      refreshAccounts()
    }
  })
  source.addEventListener('newmail', (e) => {
    const d = JSON.parse((e as MessageEvent).data) as { from: string; subject: string }
    browserNotify(t('notif.newMail', { from: d.from }), d.subject)
  })
  source.addEventListener('notification', (e) => {
    const n = JSON.parse((e as MessageEvent).data) as Notification
    if (n.kind === 'new_mail') return
    setState((s) => ({ notifications: [n, ...s.notifications.filter((x) => x.id !== n.id)] }))
    toast(n.title + (n.body ? ' — ' + n.body : ''), 'error')
    browserNotify(n.title, n.body)
    refreshAccounts()
  })
  source.addEventListener('outbox', (e) => {
    const it = JSON.parse((e as MessageEvent).data) as OutboxItem
    setState((s) => {
      const others = s.outbox.filter((x) => x.id !== it.id)
      return { outbox: [it, ...others].sort((a, b) => b.createdAt - a.createdAt) }
    })
    if (it.status === 'sent') toast(t('compose.sent'), 'success', { id: 1_000_000 + it.id })
    if (it.status === 'failed') toast(t('compose.failed', { err: it.lastError ?? '' }), 'error', { id: 1_000_000 + it.id })
    if (it.status === 'ambiguous') toast(t('compose.ambiguous'), 'error', { id: 1_000_000 + it.id, ttl: 15000 })
  })
  source.onerror = () => {
    // EventSource reconnects automatically; resync state when it does.
  }
  source.onopen = () => {
    refreshCounters()
    refreshFolders()
    refreshAccounts()
    void loadOutbox().catch(() => {})
  }
}

export function disconnectEvents() {
  source?.close()
  source = null
}
