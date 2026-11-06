import { getLanguage, t } from './i18n'
import { getState } from './store'
import type { Account, AccountLayout, Address, Endpoint, Folder, ServerPreset } from './types'

function tz(): string | undefined {
  return getState().settings.timezone || undefined
}

function locale() {
  return getLanguage() === 'fr' ? 'fr-FR' : 'en-GB'
}

/** Compact date for lists: time today, "Yesterday", day/month this year, full date otherwise. */
export function shortDate(unix: number): string {
  const d = new Date(unix * 1000)
  const now = new Date()
  const opts: Intl.DateTimeFormatOptions = { timeZone: tz() }
  const day = (x: Date) => x.toLocaleDateString('en-CA', opts)
  if (day(d) === day(now)) return d.toLocaleTimeString(locale(), { ...opts, hour: '2-digit', minute: '2-digit' })
  const y = new Date(now.getTime() - 86400000)
  if (day(d) === day(y)) return t('time.yesterday')
  if (d.getFullYear() === now.getFullYear()) return d.toLocaleDateString(locale(), { ...opts, day: 'numeric', month: 'short' })
  return d.toLocaleDateString(locale(), { ...opts, day: '2-digit', month: '2-digit', year: 'numeric' })
}

export function fullDate(unix: number): string {
  return new Date(unix * 1000).toLocaleString(locale(), { timeZone: tz(), dateStyle: 'full', timeStyle: 'short' })
}

export function relativeTime(unix?: number): string {
  if (!unix) return t('acct.never')
  const diff = Math.round(Date.now() / 1000 - unix)
  const rtf = new Intl.RelativeTimeFormat(locale(), { numeric: 'auto' })
  if (diff < 60) return rtf.format(-diff, 'second')
  if (diff < 3600) return rtf.format(-Math.round(diff / 60), 'minute')
  if (diff < 86400) return rtf.format(-Math.round(diff / 3600), 'hour')
  return rtf.format(-Math.round(diff / 86400), 'day')
}

export function formatSize(n: number): string {
  if (n < 1024) return `${n} ${t('size.b')}`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} ${t('size.kb')}`
  if (n < 1024 ** 3) return `${(n / 1024 / 1024).toFixed(1)} ${t('size.mb')}`
  return `${(n / 1024 ** 3).toFixed(1)} ${t('size.gb')}`
}

export function displayName(a: Address | undefined): string {
  if (!a) return ''
  return a.name?.trim() || a.addr
}

export function formatAddress(a: Address): string {
  return a.name ? `${a.name} <${a.addr}>` : a.addr
}

const emailRe = /^[^\s@<>(),;:"]+@[^\s@<>(),;:"]+\.[^\s@<>(),;:"]+$/

export function isEmail(s: string): boolean {
  return emailRe.test(s.trim())
}

/** Parses "Name <a@b>, c@d" into addresses; invalid entries are returned separately. */
export function parseAddresses(input: string): { valid: Address[]; invalid: string[] } {
  const valid: Address[] = []
  const invalid: string[] = []
  // split on commas/semicolons outside quotes and angle brackets
  const parts: string[] = []
  let cur = ''
  let quote = false
  let angle = false
  for (const ch of input) {
    if (ch === '"') quote = !quote
    if (ch === '<') angle = true
    if (ch === '>') angle = false
    if ((ch === ',' || ch === ';' || ch === '\n') && !quote && !angle) {
      parts.push(cur)
      cur = ''
    } else cur += ch
  }
  parts.push(cur)
  for (const raw of parts) {
    const p = raw.trim()
    if (!p) continue
    const m = p.match(/^(.*)<([^<>]+)>$/)
    if (m) {
      const addr = m[2].trim()
      const name = m[1].trim().replace(/^"|"$/g, '')
      if (isEmail(addr)) valid.push({ name: name || undefined, addr })
      else invalid.push(p)
    } else if (isEmail(p)) valid.push({ addr: p })
    else invalid.push(p)
  }
  return { valid, invalid }
}

export function accountLabel(a?: Account): string {
  if (!a) return ''
  return a.displayName && a.displayName !== a.email ? a.displayName : a.email
}

const palette = ['#2563eb', '#16a34a', '#d97706', '#db2777', '#7c3aed', '#0891b2', '#dc2626', '#4d7c0f']

export function accountColor(a?: Account): string {
  if (!a) return '#888'
  if (a.color && /^#[0-9a-fA-F]{3,8}$/.test(a.color)) return a.color
  return palette[a.id % palette.length]
}

/** Reads the accountLayout setting: unknown ids are dropped, accounts not placed yet come last without a category. */
export function accountLayout(raw: string | undefined, accounts: Account[]): AccountLayout {
  let stored: Partial<AccountLayout> | null = null
  try {
    stored = JSON.parse(raw || '{}')
  } catch {
    // unreadable value: same as no layout
  }
  const left = new Set(accounts.map((a) => a.id))
  const take = (ids: unknown) => (Array.isArray(ids) ? ids.filter((id: number) => left.delete(id)) : [])
  const ungrouped = take(stored?.ungrouped)
  const groups = (Array.isArray(stored?.groups) ? stored.groups : []).map((g) => ({ ...g, accounts: take(g.accounts) }))
  const ids = new Set(accounts.map((a) => a.id))
  const collapsedAccounts = Array.isArray(stored?.collapsedAccounts) ? stored.collapsedAccounts.filter((id) => ids.has(id)) : []
  return { ungrouped: [...ungrouped, ...accounts.filter((a) => left.has(a.id)).map((a) => a.id)], groups, ...(collapsedAccounts.length ? { collapsedAccounts } : {}) }
}

/** Accounts in display order, ignoring the categories. */
export function orderedAccounts(layout: AccountLayout, accounts: Account[]): Account[] {
  const byId = new Map(accounts.map((a) => [a.id, a]))
  return [...layout.ungrouped, ...layout.groups.flatMap((g) => g.accounts)].map((id) => byId.get(id)!)
}

/** Moves an account into a category (null = none), before another account or at the end. */
export function moveAccount(layout: AccountLayout, id: number, groupId: string | null, beforeId?: number): AccountLayout {
  const place = (list: number[], gid: string | null) => {
    const next = list.filter((x) => x !== id)
    if (gid === groupId) {
      const at = beforeId === undefined ? -1 : next.indexOf(beforeId)
      next.splice(at < 0 ? next.length : at, 0, id)
    }
    return next
  }
  return { ...layout, ungrouped: place(layout.ungrouped, null), groups: layout.groups.map((g) => ({ ...g, accounts: place(g.accounts, g.id) })) }
}

/** Reads the serverPresets setting; an unreadable value means no preset. */
export function serverPresets(raw: string | undefined): ServerPreset[] {
  try {
    const list = JSON.parse(raw || '[]')
    return Array.isArray(list) ? list : []
  } catch {
    return []
  }
}

/** Whether a preset already holds these servers. */
export function hasServerPreset(presets: ServerPreset[], imap: Endpoint, smtp: Endpoint): boolean {
  const same = (a: Endpoint, b: Endpoint) => a.host.toLowerCase() === b.host.toLowerCase() && a.port === b.port && a.security === b.security
  return presets.some((p) => same(p.imap, imap) && same(p.smtp, smtp))
}

export function folderLabel(f: Folder): string {
  if (f.role && f.role !== 'inbox') {
    const key = ('role.' + f.role) as Parameters<typeof t>[0]
    const translated = t(key)
    if (translated !== key) return translated
  }
  if (f.role === 'inbox') return t('role.inbox')
  const leaf = f.delimiter ? f.name.split(f.delimiter).pop() : f.name
  return leaf || f.name
}

export function folderDepth(f: Folder): number {
  if (!f.delimiter || f.role) return 0
  return Math.min(4, f.name.split(f.delimiter).length - 1)
}

export function escapeHTML(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!)
}

export function textToHTML(s: string): string {
  return escapeHTML(s).replace(/\n/g, '<br>')
}

export function htmlToText(html: string): string {
  const doc = new DOMParser().parseFromString(html, 'text/html')
  doc.querySelectorAll('br').forEach((b) => b.replaceWith('\n'))
  doc.querySelectorAll('p,div,li,tr,h1,h2,h3,blockquote').forEach((el) => el.append('\n'))
  return (doc.body.textContent || '').replace(/\n{3,}/g, '\n\n').trim()
}

/** Strips elements that must never be inserted in the composer. */
export function cleanFragment(html: string): string {
  const doc = new DOMParser().parseFromString(html, 'text/html')
  doc.querySelectorAll('style,script,link,meta,iframe,object,embed,form,base').forEach((el) => el.remove())
  doc.querySelectorAll('*').forEach((el) => {
    for (const attr of Array.from(el.attributes)) {
      if (attr.name.startsWith('on')) el.removeAttribute(attr.name)
    }
  })
  return doc.body.innerHTML
}

export function mentionsAttachment(text: string): boolean {
  return /\b(pi[eè]ce[s]? jointe[s]?|ci-joint|en pj|attached|attachment|enclosed)\b/i.test(text)
}

export function debounce<A extends unknown[]>(fn: (...a: A) => void, ms: number) {
  let h: ReturnType<typeof setTimeout> | undefined
  return (...a: A) => {
    if (h) clearTimeout(h)
    h = setTimeout(() => fn(...a), ms)
  }
}
