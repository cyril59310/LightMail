import { useMemo, useState } from 'react'
import { t } from '../i18n'
import { href, useRoute } from '../router'
import { openCompose, useStore } from '../store'
import type { Account, Folder } from '../types'
import { accountColor, accountLabel, folderDepth, folderLabel } from '../util'
import { Icon } from './Icons'

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
  const folders = useStore((s) => s.folders)
  const counters = useStore((s) => s.counters)
  const outbox = useStore((s) => s.outbox)
  const me = useStore((s) => s.me)
  const [collapsed, setCollapsed] = useState<Record<number, boolean>>({})
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

  return (
    <nav className="sidebar" aria-label="Navigation">
      <div className="brand">
        <span className="brand-mark" aria-hidden="true" />
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

      <div className="nav-section-title">{t('nav.accounts')}</div>
      {accounts.map((a: Account) => {
        const list = byAccount.get(a.id) ?? []
        const open = !collapsed[a.id]
        const problem = a.status === 'error' || a.status === 'auth_error'
        return (
          <div key={a.id} className="nav-account">
            <button className="nav-account-head" onClick={() => setCollapsed((c) => ({ ...c, [a.id]: open }))} aria-expanded={open} title={a.email}>
              <span className="dot" style={{ background: accountColor(a) }} />
              <span className="nav-label">{accountLabel(a)}</span>
              {problem && <Icon name="alert" className="warn" size={15} title={a.lastError || t(('acct.status.' + a.status) as never)} />}
              {a.status === 'syncing' && <Icon name="refresh" className="spin muted" size={14} />}
            </button>
            {open &&
              list
                .filter((f) => f.selectable && (f.syncEnabled || f.role === 'inbox'))
                .map((f) => (
                  <a
                    key={f.id}
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
                ))}
          </div>
        )
      })}
      {accounts.length === 0 && <p className="muted small pad">{t('acct.none')}</p>}

      <div className="nav-footer">
        <NavLink to="add-account" icon="plus" label={t('nav.addAccount')} active={cur === 'add-account'} onNavigate={onNavigate} />
        <NavLink to="settings/general" icon="settings" label={t('nav.settings')} active={route.path[0] === 'settings'} onNavigate={onNavigate} />
        <button className="nav-item" onClick={onLogout}>
          <Icon name="logout" />
          <span className="nav-label">{t('nav.logout')}</span>
        </button>
      </div>
    </nav>
  )
}
