import { useMemo, useState } from 'react'
import { createPortal } from 'react-dom'
import { t } from '../i18n'
import { href, useRoute } from '../router'
import { openCompose, saveAccountLayout, useStore } from '../store'
import type { Account, Folder } from '../types'
import { accountColor, accountLabel, accountLayout, folderDepth, folderLabel } from '../util'
import { deleteFolder, NewFolderDialog } from './FolderDialog'
import { BrandMark, Icon } from './Icons'

const roleIcon: Record<string, string> = {
  inbox: 'inbox', sent: 'send', drafts: 'file', archive: 'archive', trash: 'trash', junk: 'spam', all: 'mail', flagged: 'star', important: 'alert',
}

const roleOrder = ['inbox', 'drafts', 'sent', 'archive', 'junk', 'trash']

function NavLink({ to, icon, label, count, active, accent, onNavigate }: { to: string; icon: string; label: string; count?: number; active: boolean; accent?: string; onNavigate?: () => void }) {
  return (
    <a href={href(to)} className={'nav-item' + (active ? ' active' : '')} onClick={onNavigate} aria-current={active ? 'page' : undefined}>
      <Icon name={icon} />
      <span className="nav-label">{label}</span>
      {accent && <span className="dot" style={{ background: accent }} />}
      {!!count && <span className="badge">{count > 999 ? '999+' : count}</span>}
    </a>
  )
}

export function Sidebar({ onNavigate, onLogout }: { onNavigate?: () => void; onLogout: () => void }) {
  const route = useRoute()
  const accounts = useStore((s) => s.accounts)
  const rawLayout = useStore((s) => s.settings.accountLayout)
  const folders = useStore((s) => s.folders)
  const counters = useStore((s) => s.counters)
  const outbox = useStore((s) => s.outbox)
  const me = useStore((s) => s.me)
  const [newFolder, setNewFolder] = useState<{ account: Account; parentId?: number } | null>(null)
  const cur = route.path.join('/')
  const pendingOut = outbox.filter((o) => o.status !== 'sent' && o.status !== 'canceled').length
  const hasJunk = folders.some((f) => f.role === 'junk')

  const byAccount = useMemo(() => {
    const m = new Map<number, Folder[]>()
    for (const f of folders) {
      if (!m.has(f.accountId)) m.set(f.accountId, [])
      m.get(f.accountId)!.push(f)
    }
    for (const list of m.values()) {
      list.sort((a, b) => {
        const ra = roleOrder.indexOf(a.role)
        const rb = roleOrder.indexOf(b.role)
        if (ra !== rb) return (ra < 0 ? 99 : ra) - (rb < 0 ? 99 : rb)
        return a.name.localeCompare(b.name)
      })
    }
    return m
  }, [folders])

  const layout = useMemo(() => accountLayout(rawLayout, accounts), [rawLayout, accounts])
  const byId = useMemo(() => new Map(accounts.map((a) => [a.id, a])), [accounts])

  const toggleAccount = (id: number, fold: boolean) => {
    const rest = (layout.collapsedAccounts ?? []).filter((x) => x !== id)
    return saveAccountLayout({ ...layout, collapsedAccounts: fold ? [...rest, id] : rest })
  }

  const renderAccount = (id: number) => {
    const a: Account = byId.get(id)!
    const list = byAccount.get(a.id) ?? []
    const open = !layout.collapsedAccounts?.includes(a.id)
    const problem = a.status === 'error' || a.status === 'auth_error'
    return (
      <div key={a.id} className="nav-account">
        <div className="nav-account-row">
          <button className="nav-account-head" onClick={() => void toggleAccount(a.id, open)} aria-expanded={open} title={a.email}>
            <span className="dot" style={{ background: accountColor(a) }} />
            <span className="nav-label">{accountLabel(a)}</span>
            {problem && <Icon name="alert" className="warn" size={15} title={a.lastError || t(('acct.status.' + a.status) as never)} />}
            {a.status === 'syncing' && <Icon name="refresh" className="spin muted" size={14} />}
          </button>
          <button className="icon-btn small nav-hover-btn" onClick={() => setNewFolder({ account: a })} title={t('folder.newIn', { account: accountLabel(a) })} aria-label={t('folder.newIn', { account: accountLabel(a) })}>
            <Icon name="plus" size={14} />
          </button>
        </div>
        {open &&
          list
            .filter((f) => f.selectable && (f.syncEnabled || f.role === 'inbox'))
            .map((f) => (
              <div key={f.id} className="nav-folder-row">
                <a
                  href={href('folder/' + f.id)}
                  className={'nav-item folder' + (cur === 'folder/' + f.id ? ' active' : '')}
                  style={{ paddingLeft: 28 + folderDepth(f) * 12 }}
                  onClick={onNavigate}
                  title={f.name}
                >
                  <Icon name={roleIcon[f.role] ?? 'folder'} size={16} />
                  <span className="nav-label">{folderLabel(f)}</span>
                  {f.unread > 0 && f.role !== 'sent' && f.role !== 'trash' && f.role !== 'drafts' && <span className="badge subtle">{f.unread}</span>}
                </a>
                {!f.role && (
                  <span className="nav-folder-actions">
                    {f.delimiter && (
                      <button className="icon-btn small" onClick={() => setNewFolder({ account: a, parentId: f.id })} title={t('folder.new')} aria-label={t('folder.new')}>
                        <Icon name="plus" size={14} />
                      </button>
                    )}
                    <button className="icon-btn small" onClick={() => void deleteFolder(f)} title={t('folder.delete')} aria-label={t('folder.delete')}>
                      <Icon name="trash" size={14} />
                    </button>
                  </span>
                )}
              </div>
            ))}
      </div>
    )
  }
  const groups = layout.groups.filter((g) => g.accounts.length > 0)

  return (
    <nav className="sidebar" aria-label="Navigation">
      <div className="brand">
        <BrandMark logoVersion={me?.logoVersion} />
        {me?.appName}
      </div>
      <button
        className="btn primary compose-btn"
        onClick={() => {
          openCompose({ mode: 'new' })
          onNavigate?.()
        }}
        title="c"
      >
        <Icon name="edit" /> {t('nav.compose')}
      </button>

      <div className="nav-group">
        <NavLink to="unified" icon="inbox" label={t('nav.unified')} count={counters.unifiedUnread} active={cur === '' || cur === 'unified' || cur === 'unified/inbox'} onNavigate={onNavigate} />
        <NavLink to="starred" icon="star" label={t('nav.starred')} active={cur === 'starred'} onNavigate={onNavigate} />
        <NavLink to="unified/drafts" icon="file" label={t('nav.drafts')} active={cur === 'unified/drafts'} onNavigate={onNavigate} />
        <NavLink to="unified/sent" icon="send" label={t('nav.sent')} active={cur === 'unified/sent'} onNavigate={onNavigate} />
        <NavLink to="unified/archive" icon="archive" label={t('nav.archive')} active={cur === 'unified/archive'} onNavigate={onNavigate} />
        {hasJunk && <NavLink to="unified/junk" icon="spam" label={t('nav.junk')} active={cur === 'unified/junk'} onNavigate={onNavigate} />}
        <NavLink to="unified/trash" icon="trash" label={t('nav.trash')} active={cur === 'unified/trash'} onNavigate={onNavigate} />
        {(pendingOut > 0 || cur === 'outbox') && <NavLink to="outbox" icon="outbox" label={t('nav.outbox')} count={pendingOut} active={cur === 'outbox'} onNavigate={onNavigate} />}
      </div>

      {(layout.ungrouped.length > 0 || groups.length === 0) && <div className="nav-section-title">{t('nav.accounts')}</div>}
      {layout.ungrouped.map(renderAccount)}
      {groups.map((g) => {
        const open = !g.collapsed
        return (
          <div key={g.id}>
            <button className="nav-section-title nav-category" onClick={() => void saveAccountLayout({ ...layout, groups: layout.groups.map((x) => (x.id === g.id ? { ...x, collapsed: open } : x)) })} aria-expanded={open} title={g.name}>
              <span className="nav-label">{g.name}</span>
              <Icon name="chevron" size={14} className={open ? '' : 'closed'} />
            </button>
            {open && g.accounts.map(renderAccount)}
          </div>
        )
      })}
      {accounts.length === 0 && <p className="muted small pad">{t('acct.none')}</p>}

      <div className="nav-footer">
        <NavLink to="contacts" icon="book" label={t('nav.contacts')} active={cur === 'contacts'} onNavigate={onNavigate} />
        <NavLink to="add-account" icon="plus" label={t('nav.addAccount')} active={cur === 'add-account'} onNavigate={onNavigate} />
        <NavLink to="settings/general" icon="settings" label={t('nav.settings')} active={route.path[0] === 'settings'} onNavigate={onNavigate} />
        <button className="nav-item" onClick={onLogout}>
          <Icon name="logout" />
          <span className="nav-label">{t('nav.logout')}</span>
        </button>
      </div>
      {newFolder &&
        createPortal(<NewFolderDialog account={newFolder.account} parentId={newFolder.parentId} onClose={() => setNewFolder(null)} />, document.body)}
    </nav>
  )
}
