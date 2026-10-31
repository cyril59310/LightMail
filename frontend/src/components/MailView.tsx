import { useEffect, useMemo, useRef, useState } from 'react'
import { t } from '../i18n'
import { navigate, useRoute } from '../router'
import { openCompose, useStore } from '../store'
import type { Message } from '../types'
import { accountLabel, folderLabel } from '../util'
import { Icon } from './Icons'
import { runActionIds } from '../actions'
import { MessageList, type ListHandle, type ListSpec } from './MessageList'
import { Reader } from './Reader'

const unifiedTitles: Record<string, string> = {
  inbox: 'nav.unified', sent: 'nav.sent', drafts: 'nav.drafts', archive: 'nav.archive', trash: 'nav.trash', junk: 'nav.junk',
}

export function MailView() {
  const route = useRoute()
  const folders = useStore((s) => s.folders)
  const accounts = useStore((s) => s.accounts)
  const readingPane = useStore((s) => s.settings.readingPane)
  const [selected, setSelected] = useState<number | null>(null)
  const listRef = useRef<ListHandle | null>(null)
  const [searchText, setSearchText] = useState(route.query.get('q') ?? '')
  const searchInput = useRef<HTMLInputElement>(null)

  const { spec, title, showRecipients, currentFolder } = useMemo(() => {
    const [view, param] = route.path
    if (view === 'starred') return { spec: { kind: 'starred' } as ListSpec, title: t('nav.starred'), showRecipients: false, currentFolder: undefined }
    if (view === 'folder') {
      const f = folders.find((x) => x.id === Number(param))
      const a = accounts.find((x) => x.id === f?.accountId)
      return {
        spec: { kind: 'folder', folderId: Number(param) } as ListSpec,
        title: f ? `${folderLabel(f)} — ${accountLabel(a)}` : '…',
        showRecipients: f?.role === 'sent' || f?.role === 'drafts',
        currentFolder: f,
      }
    }
    if (view === 'search') {
      const q = route.query.get('q') ?? ''
      const folderId = Number(route.query.get('folder')) || undefined
      const server = route.query.get('server') === '1'
      return {
        spec: { kind: 'search', q, folderId, server } as ListSpec,
        title: server ? t('search.serverResults') : t('search.results', { q }),
        showRecipients: false,
        currentFolder: folders.find((x) => x.id === folderId),
      }
    }
    const role = view === 'unified' && param ? param : 'inbox'
    return { spec: { kind: 'unified', role } as ListSpec, title: t((unifiedTitles[role] ?? 'nav.unified') as never), showRecipients: role === 'sent' || role === 'drafts', currentFolder: undefined }
  }, [route, folders, accounts])

  // Reset selection when the view changes.
  const specKey = JSON.stringify(spec)
  useEffect(() => setSelected(null), [specKey])
  useEffect(() => setSearchText(route.query.get('q') ?? ''), [route])

  const submitSearch = (server = false) => {
    const q = searchText.trim()
    if (!q) return navigate('unified')
    const folder = currentFolder?.id ?? (spec.kind === 'folder' ? spec.folderId : undefined)
    navigate('search?' + new URLSearchParams({ q, ...(folder ? { folder: String(folder) } : {}), ...(server ? { server: '1' } : {}) }).toString())
  }

  // Keyboard shortcuts
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement
      if (el.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName) || e.ctrlKey || e.metaKey || e.altKey) return
      if (document.querySelector('.modal-backdrop')) return
      const list = listRef.current
      const cur = list?.items.find((m) => m.id === selected)
      switch (e.key) {
        case 'j':
          list?.selectNext(1)
          break
        case 'k':
          list?.selectNext(-1)
          break
        case 'Escape':
          setSelected(null)
          break
        case '/':
          e.preventDefault()
          searchInput.current?.focus()
          break
        case 'c':
          openCompose({ mode: 'new' })
          break
        case 'o':
        case 'Enter':
          if (!selected) list?.selectNext(1)
          break
        default:
          if (!cur || !list) return
          if (e.key === 'e') void list.act('archive', [cur.id])
          else if (e.key === '#' || e.key === 'Delete') void list.act('trash', [cur.id])
          else if (e.key === 's') void list.act(cur.flagged ? 'unstar' : 'star', [cur.id])
          else if (e.key === 'm') void list.act(cur.seen ? 'unread' : 'read', [cur.id])
          else if (e.key === 'r') openCompose({ mode: 'reply', messageId: cur.id })
          else if (e.key === 'a') openCompose({ mode: 'replyall', messageId: cur.id })
          else if (e.key === 'f') openCompose({ mode: 'forward', messageId: cur.id })
          else return
      }
      e.preventDefault()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [selected])

  const fullScreenReader = readingPane === 'off'
  return (
    <div className={'mail-view' + (selected ? ' has-selection' : '') + (fullScreenReader ? ' pane-off' : '')}>
      <div className="list-column">
        <form className="search-bar" role="search" onSubmit={(e) => { e.preventDefault(); submitSearch(false) }}>
          <Icon name="search" />
          <input ref={searchInput} type="search" value={searchText} onChange={(e) => setSearchText(e.target.value)} placeholder={t('search.placeholder')} aria-label={t('search.placeholder')} maxLength={500} />
        </form>
        {spec.kind === 'search' && (
          <div className="search-extra small">
            {spec.folderId ? (
              !spec.server && (
                <button className="link" onClick={() => submitSearch(true)}>
                  <Icon name="search" size={13} /> {t('search.server')}
                </button>
              )
            ) : (
              <span className="muted">{t('search.pickFolder')}</span>
            )}
            <span className="muted block">{t('search.serverHint')}</span>
          </div>
        )}
        {currentFolder && !currentFolder.backfillDone && spec.kind === 'folder' && <div className="muted small pad-x">{t('list.backfill')} ({currentFolder.local}/{currentFolder.total})</div>}
        <MessageList spec={spec} selectedId={selected} onSelect={(m: Message | null) => setSelected(m?.id ?? null)} handleRef={listRef} title={title} showRecipients={showRecipients} />
      </div>
      <div className="reader-column">
        {selected ? (
          <Reader key={selected} messageId={selected} onAct={(a, ids, f) => (listRef.current?.items.some((m) => ids.includes(m.id)) ? listRef.current.act(a, ids, f) : runActionIds(ids, a, f))} onClose={() => setSelected(null)} onOpenMessage={setSelected} />
        ) : (
          <div className="reader empty">
            <Icon name="mailOpen" size={48} className="muted" />
            <p className="muted">{t('read.select')}</p>
            <p className="muted small">{t('kbd.help')}</p>
          </div>
        )}
      </div>
    </div>
  )
}
