// Message actions shared by the list and the reader.
import { ApiError, api } from './api'
import { confirmDialog } from './components/Dialogs'
import { t } from './i18n'
import { bumpLists, getState, loadCounters, loadFolders, toast } from './store'
import type { Message } from './types'

export type Action = 'read' | 'unread' | 'star' | 'unstar' | 'archive' | 'trash' | 'delete' | 'move' | 'spam' | 'notspam'

interface Result {
  ok: number[]
  failed?: Record<string, string>
}

/**
 * Resolves the effective action (trash → delete inside the Trash folder) and asks
 * for confirmation when needed. Returns null if the user cancelled.
 */
export async function confirmAction(msgs: Message[], action: Action): Promise<Action | null> {
  const folders = getState().folders
  const inTrash = msgs.every((m) => folders.find((f) => f.id === m.folderId)?.role === 'trash')
  if (action === 'trash' && inTrash) action = 'delete'
  if (action === 'delete') {
    if (!(await confirmDialog(t('act.confirmDelete', { n: msgs.length }), { danger: true, confirmLabel: t('act.deleteForever') }))) return null
  } else if (action === 'trash' && getState().settings.confirmDelete === 'true' && msgs.length > 1) {
    if (!(await confirmDialog(t('act.confirmTrash', { n: msgs.length }), { confirmLabel: t('act.trash') }))) return null
  }
  return action
}

/**
 * Runs an action on the server. Returns the ids that succeeded.
 * Pass `confirmed: true` when confirmAction() was already called by the caller.
 */
export async function runAction(msgs: Message[], action: Action, folderId?: number, opts?: { confirmed?: boolean }): Promise<number[]> {
  if (msgs.length === 0) return []
  if (!opts?.confirmed) {
    const resolved = await confirmAction(msgs, action)
    if (!resolved) return []
    action = resolved
  }
  try {
    const r = await api.post<Result>('/messages/actions', { ids: msgs.map((m) => m.id), action, folderId })
    const failed = Object.values(r.failed ?? {})
    if (failed.length) toast(t('act.failed', { err: failed[0] }), 'error')
    void loadCounters().catch(() => {})
    void loadFolders().catch(() => {})
    if (!['read', 'unread', 'star', 'unstar'].includes(action)) bumpLists()
    return r.ok
  } catch (e) {
    const err = e instanceof ApiError ? e.message : String(e)
    toast(t('act.failed', { err }), 'error')
    return []
  }
}

/** Fields changed locally for an optimistic update. */
export function optimisticPatch(action: Action): Partial<Message> | 'remove' | null {
  switch (action) {
    case 'read':
      return { seen: true }
    case 'unread':
      return { seen: false }
    case 'star':
      return { flagged: true }
    case 'unstar':
      return { flagged: false }
    case 'archive':
    case 'trash':
    case 'delete':
    case 'move':
    case 'spam':
    case 'notspam':
      return 'remove'
  }
}

/** Action on messages that are not part of the visible list (e.g. thread). */
export async function runActionIds(ids: number[], action: Action, folderId?: number) {
  try {
    await api.post<Result>('/messages/actions', { ids, action, folderId })
    bumpLists()
    void loadCounters().catch(() => {})
  } catch (e) {
    toast(t('act.failed', { err: e instanceof ApiError ? e.message : String(e) }), 'error')
  }
}
