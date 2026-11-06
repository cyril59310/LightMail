// Folder management: creation dialog and deletion helper.
import { useMemo, useState } from 'react'
import { ApiError, api } from '../api'
import { t } from '../i18n'
import { navigate } from '../router'
import { loadFolders, toast, useStore } from '../store'
import type { Account, Folder } from '../types'
import { accountLabel, folderLabel } from '../util'
import { confirmDialog, Modal } from './Dialogs'

/** Full path of a folder for display ("Projets / Client A"). */
function folderPath(f: Folder): string {
  if (f.role) return folderLabel(f)
  return f.delimiter ? f.name.split(f.delimiter).join(' / ') : f.name
}

export function NewFolderDialog({ account, parentId, onClose, openAfter = true }: { account: Account; parentId?: number; onClose: () => void; openAfter?: boolean }) {
  const folders = useStore((s) => s.folders)
  const [name, setName] = useState('')
  const [parent, setParent] = useState(parentId ?? 0)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const parents = useMemo(
    () =>
      folders
        .filter((f) => f.accountId === account.id && f.delimiter && !['junk', 'trash', 'drafts', 'sent', 'all', 'flagged', 'important'].includes(f.role))
        .sort((a, b) => (a.role === 'inbox' ? -1 : b.role === 'inbox' ? 1 : a.name.localeCompare(b.name))),
    [folders, account.id],
  )
  const delim = folders.find((f) => f.id === parent)?.delimiter ?? ''
  const trimmed = name.trim()
  const invalid = !trimmed || /[*%\r\n]/.test(trimmed) || (!!delim && trimmed.includes(delim))

  const submit = async () => {
    if (invalid || busy) return
    setBusy(true)
    setError('')
    try {
      const f = await api.post<Folder>('/folders', { accountId: account.id, parentId: parent || undefined, name: trimmed })
      await loadFolders()
      toast(t('folder.created', { name: trimmed }), 'success')
      onClose()
      if (openAfter) navigate('folder/' + f.id)
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('err.network'))
      setBusy(false)
    }
  }

  return (
    <Modal onClose={onClose} title={t('folder.new')} small>
      <form
        className="form"
        onSubmit={(e) => {
          e.preventDefault()
          void submit()
        }}
      >
        <p className="muted small">{accountLabel(account)}</p>
        <label>
          {t('folder.name')}
          <input autoFocus value={name} onChange={(e) => setName(e.target.value)} maxLength={150} required />
        </label>
        <label>
          {t('folder.parent')}
          <select value={parent} onChange={(e) => setParent(Number(e.target.value))}>
            <option value={0}>{t('folder.root')}</option>
            {parents.map((f) => (
              <option key={f.id} value={f.id}>
                {folderPath(f)}
              </option>
            ))}
          </select>
        </label>
        {error && <p className="notice error small">{error}</p>}
        <div className="row end gap">
          <button type="button" className="btn" onClick={onClose}>
            {t('act.cancel')}
          </button>
          <button type="submit" className="btn primary" disabled={invalid || busy}>
            {t('folder.create')}
          </button>
        </div>
      </form>
    </Modal>
  )
}

/** Asks for confirmation, then deletes a user folder on the server. */
export async function deleteFolder(f: Folder) {
  if (f.role) return
  if (!(await confirmDialog(t('folder.deleteConfirm', { name: folderPath(f) }), { danger: true, confirmLabel: t('act.delete') }))) return
  try {
    await api.del(`/folders/${f.id}`)
    await loadFolders()
    toast(t('folder.deleted'), 'success')
    if (location.hash.replace(/^#\/?/, '').split('?')[0] === 'folder/' + f.id) navigate('unified')
  } catch (e) {
    toast(e instanceof ApiError ? e.message : t('err.network'), 'error')
  }
}
