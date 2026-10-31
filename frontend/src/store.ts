// Tiny global store (no external state library).
import { useSyncExternalStore } from 'react'
import { ApiError, api } from './api'
import { t } from './i18n'
import type { Account, AccountLayout, Counters, Folder, Me, Notification, OutboxItem, Settings } from './types'

export interface Toast {
  id: number
  kind: 'info' | 'success' | 'error'
  text: string
  action?: { label: string; run: () => void }
  ttl?: number
}

export interface ComposeRequest {
  key: number
  mode: 'new' | 'reply' | 'replyall' | 'forward'
  messageId?: number
  draftId?: number
  to?: string
}

export interface State {
  me: Me | null
  accounts: Account[]
  folders: Folder[]
  settings: Settings
  counters: Counters
  notifications: Notification[]
  outbox: OutboxItem[]
  toasts: Toast[]
  compose: ComposeRequest | null
  /** incremented when message lists must refresh */
  listVersion: number
}

let state: State = {
  me: null,
  accounts: [],
  folders: [],
  settings: {},
  counters: { unifiedUnread: 0, starred: 0, accounts: {} },
  notifications: [],
  outbox: [],
  toasts: [],
  compose: null,
  listVersion: 0,
}

const listeners = new Set<() => void>()

export function getState() {
  return state
}

export function setState(patch: Partial<State> | ((s: State) => Partial<State>)) {
  const p = typeof patch === 'function' ? patch(state) : patch
  state = { ...state, ...p }
  listeners.forEach((l) => l())
}

export function useStore<T>(sel: (s: State) => T): T {
  return useSyncExternalStore(
    (cb) => {
      listeners.add(cb)
      return () => listeners.delete(cb)
    },
    () => sel(state),
  )
}

let toastSeq = 0
export function toast(text: string, kind: Toast['kind'] = 'info', opts: Partial<Toast> = {}) {
  const id = opts.id ?? ++toastSeq // passing an id replaces that toast
  const ttl = opts.ttl ?? (kind === 'error' ? 8000 : 4000)
  setState((s) => ({ toasts: [...s.toasts.filter((x) => x.id !== id), { ...opts, id, kind, text, ttl }] }))
  if (ttl > 0) setTimeout(() => dismissToast(id), ttl)
  return id
}

export function dismissToast(id: number) {
  setState((s) => ({ toasts: s.toasts.filter((x) => x.id !== id) }))
}

let composeSeq = 0
export function openCompose(req: Omit<ComposeRequest, 'key'>) {
  setState({ compose: { ...req, key: ++composeSeq } })
}

export function bumpLists() {
  setState((s) => ({ listVersion: s.listVersion + 1 }))
}

// ---- loaders ---------------------------------------------------------

export async function loadAccounts() {
  const r = await api.get<{ items: Account[] }>('/accounts')
  setState({ accounts: r.items })
}

export async function loadFolders() {
  const r = await api.get<{ items: Folder[] }>('/folders')
  setState({ folders: r.items })
}

export async function loadCounters() {
  const c = await api.get<Counters>('/counters')
  setState({ counters: c })
}

export async function loadSettings() {
  const s = await api.get<Settings>('/settings')
  setState({ settings: s })
}

/** Applied immediately so that successive changes build on each other, restored if the server refuses. */
export async function saveAccountLayout(layout: AccountLayout) {
  const previous = state.settings.accountLayout
  const value = JSON.stringify(layout)
  setState((s) => ({ settings: { ...s.settings, accountLayout: value } }))
  try {
    await saveSettings({ accountLayout: value })
  } catch (e) {
    setState((s) => ({ settings: { ...s.settings, accountLayout: previous } }))
    toast(e instanceof ApiError ? e.message : t('err.network'), 'error')
  }
}

export async function loadNotifications() {
  const r = await api.get<{ items: Notification[] }>('/notifications')
  setState({ notifications: r.items })
}

export async function loadOutbox() {
  const r = await api.get<{ items: OutboxItem[] }>('/outbox')
  setState({ outbox: r.items })
}

export async function loadAll() {
  await Promise.allSettled([loadAccounts(), loadFolders(), loadCounters(), loadSettings(), loadNotifications(), loadOutbox()])
}

export async function saveSettings(patch: Settings) {
  const s = await api.put<Settings>('/settings', patch)
  setState({ settings: s })
}
