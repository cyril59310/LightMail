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

/** Runs an action on the server. Returns the ids that succeeded. */
export async function runAction(msgs: Message[], action: Action, folderId?: number): Promise<number[]> {
  if (msgs.length === 0) return []
  const folders = getState().folders
  const inTrash = msgs.every((m) => folders.find((f) => f.id === m.folderId)?.role === 'trash')
  if (action === 'trash' && inTrash) action = 'delete'
  if (action === 'delete') {
    if (!(await confirmDialog(t('act.confirmDelete', { n: msgs.length }), { danger: true, confirmLabel: t('act.deleteForever') }))) return []
  } else if (action === 'trash' && getState().settings.confirmDelete === 'true' && msgs.length > 1) {
    if (!(await confirmDialog(t('act.confirmTrash', { n: msgs.length }), { confirmLabel: t('act.trash') }))) return []
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
