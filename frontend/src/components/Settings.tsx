import { useEffect, useRef, useState, type FormEvent } from 'react'
import { ApiError, api } from '../api'
import { setLanguage, t } from '../i18n'
import { href, navigate, useRoute } from '../router'
import { getState, setState, loadAccounts, loadFolders, saveAccountLayout, saveSettings, toast, useStore } from '../store'
import type { Account } from '../types'
import { accountColor, accountLabel, accountLayout, folderLabel, formatSize, moveAccount, orderedAccounts, relativeTime } from '../util'
import { EndpointFields, startOAuth } from './AccountWizard'
import { confirmDialog } from './Dialogs'
import { deleteFolder, NewFolderDialog } from './FolderDialog'
import { RichEditor } from './Editor'
import { TwoFactorSettings } from './TwoFactor'
import { BrandMark, Icon } from './Icons'

const tabs = ['general', 'accounts', 'notifications', 'security', 'users', 'about'] as const
// Instance-wide tabs, reserved to administrators.
const adminTabs: readonly string[] = ['users', 'about']

export function SettingsPage() {
  const route = useRoute()
  const isAdmin = useStore((s) => !!s.me?.user?.isAdmin)
  const visible = tabs.filter((x) => isAdmin || !adminTabs.includes(x))
  const asked = route.path[1] as (typeof tabs)[number]
  const tab = asked && visible.includes(asked) ? asked : 'general'
  return (
    <div className="page settings">
      <h1>{t('set.title')}</h1>
      <div className="tabs" role="tablist">
        {visible.map((x) => (
          <a key={x} role="tab" aria-selected={tab === x} className={'tab' + (tab === x ? ' active' : '')} href={href('settings/' + x)}>
            {t(('set.' + x) as never)}
          </a>
        ))}
      </div>
      {tab === 'general' && <General />}
      {tab === 'accounts' && <Accounts selected={Number(route.query.get('id')) || undefined} />}
      {tab === 'notifications' && <Notifications />}
      {tab === 'security' && <Security />}
      {tab === 'users' && <Users />}
      {tab === 'about' && <About />}
    </div>
  )
}

function Select({ k, label, options }: { k: string; label: string; options: [string, string][] }) {
  const settings = useStore((s) => s.settings)
  return (
    <label className="setting">
      <span>{label}</span>
      <select
        value={settings[k] ?? ''}
        onChange={async (e) => {
          const v = e.target.value
          if (k === 'language') setLanguage(v)
          try {
            await saveSettings({ [k]: v })
          } catch (err) {
            toast(err instanceof ApiError ? err.message : t('err.network'), 'error')
          }
        }}
      >
        {options.map(([v, l]) => (
          <option key={v} value={v}>
            {l}
          </option>
        ))}
      </select>
    </label>
  )
}

function Toggle({ k, label, hint }: { k: string; label: string; hint?: string }) {
  const settings = useStore((s) => s.settings)
  const on = settings[k] === 'true' || settings[k] === 'on'
  const isNotify = k.startsWith('notify.')
  return (
    <label className="setting toggle">
      <span>
        {label}
        {hint && <span className="muted small block">{hint}</span>}
      </span>
      <input
        type="checkbox"
        checked={isNotify ? settings[k] !== 'off' : on}
        onChange={(e) => void saveSettings({ [k]: isNotify ? (e.target.checked ? 'on' : 'off') : String(e.target.checked) }).catch(() => toast(t('err.network'), 'error'))}
      />
    </label>
  )
}

function General() {
  const accounts = useStore((s) => s.accounts)
  const settings = useStore((s) => s.settings)
  const isAdmin = useStore((s) => !!s.me?.user?.isAdmin)
  const browserTZ = Intl.DateTimeFormat().resolvedOptions().timeZone
  const zones: string[] = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.('timeZone') ?? [browserTZ, 'UTC']
  const unified = settings.unifiedAccounts === 'all' || !settings.unifiedAccounts ? null : (JSON.parse(settings.unifiedAccounts) as number[])
  return (
    <div className="card form">
      <Select k="language" label={t('set.language')} options={[['fr', 'Français'], ['en', 'English']]} />
      <Select k="theme" label={t('set.theme')} options={[['system', t('set.theme.system')], ['light', t('set.theme.light')], ['dark', t('set.theme.dark')]]} />
      <Select k="timezone" label={t('set.timezone')} options={[['', t('set.timezoneAuto', { tz: browserTZ })], ...zones.map((z) => [z, z] as [string, string])]} />
      <Select k="pageSize" label={t('set.pageSize')} options={[['25', '25'], ['50', '50'], ['100', '100'], ['200', '200']]} />
      <Select k="density" label={t('set.density')} options={[['comfortable', t('set.density.comfortable')], ['compact', t('set.density.compact')]]} />
      <Select k="readingPane" label={t('set.readingPane')} options={[['right', t('set.readingPane.right')], ['off', t('set.readingPane.off')]]} />
      <Select k="defaultFormat" label={t('set.defaultFormat')} options={[['html', t('compose.rich')], ['text', t('compose.plain')]]} />
      <Select k="remoteImages" label={t('set.remoteImages')} options={[['ask', t('set.remoteImages.ask')], ['always', t('set.remoteImages.always')]]} />
      <Select k="undoSendSeconds" label={t('set.undoSend')} options={['0', '5', '10', '20', '30'].map((v) => [v, v])} />
      <Toggle k="markReadOnOpen" label={t('set.markRead')} />
      <Toggle k="confirmDelete" label={t('set.confirmDelete')} />
      <Toggle k="confirmSendNoSubject" label={t('set.confirmNoSubject')} />
      {accounts.length > 1 && (
        <fieldset className="setting-group">
          <legend>{t('set.unified')}</legend>
          <label className="check">
            <input type="checkbox" checked={!unified} onChange={(e) => void saveSettings({ unifiedAccounts: e.target.checked ? 'all' : JSON.stringify(accounts.map((a) => a.id)) })} /> {t('set.unifiedAll')}
          </label>
          {unified &&
            accounts.map((a) => (
              <label key={a.id} className="check">
                <input
                  type="checkbox"
                  checked={unified.includes(a.id)}
                  onChange={(e) => {
                    const next = e.target.checked ? [...unified, a.id] : unified.filter((x) => x !== a.id)
                    void saveSettings({ unifiedAccounts: JSON.stringify(next) })
                  }}
                />{' '}
                {a.email}
              </label>
            ))}
        </fieldset>
      )}
      {isAdmin && <LogoSetting />}
    </div>
  )
}

function LogoSetting() {
  const logoVersion = useStore((s) => s.me?.logoVersion)
  const [busy, setBusy] = useState(false)
  const input = useRef<HTMLInputElement>(null)
  const setLogo = (v: number | undefined) => setState((s) => ({ me: s.me ? { ...s.me, logoVersion: v } : s.me }))
  const upload = async (file: File | undefined) => {
    if (!file) return
    const form = new FormData()
    form.append('file', file)
    setBusy(true)
    try {
      const r = await api.upload<{ logoVersion: number }>('/branding/logo', form)
      setLogo(r.logoVersion)
      toast(t('set.logo.saved'), 'success')
    } catch (e) {
      toast(e instanceof ApiError ? e.message : t('err.network'), 'error')
    } finally {
      setBusy(false)
    }
  }
  const reset = async () => {
    setBusy(true)
    try {
      await api.del('/branding/logo')
      setLogo(undefined)
    } catch (e) {
      toast(e instanceof ApiError ? e.message : t('err.network'), 'error')
    } finally {
      setBusy(false)
    }
  }
  return (
    <fieldset className="setting-group">
      <legend>{t('set.logo')}</legend>
      <div className="row gap wrap logo-setting">
        <BrandMark logoVersion={logoVersion} />
        <input
          ref={input}
          type="file"
          accept="image/png,image/jpeg,image/gif,image/webp,image/svg+xml,image/x-icon"
          hidden
          onChange={(e) => {
            void upload(e.target.files?.[0])
            e.target.value = ''
          }}
        />
        <button className="btn small" disabled={busy} onClick={() => input.current?.click()}>
          <Icon name="image" size={14} /> {t('set.logo.choose')}
        </button>
        {!!logoVersion && (
          <button className="btn small" disabled={busy} onClick={() => void reset()}>
            {t('set.logo.reset')}
          </button>
        )}
      </div>
      <p className="muted small">{t('set.logo.hint')}</p>
    </fieldset>
  )
}

function AccountOrganizer() {
  const accounts = useStore((s) => s.accounts)
  const raw = useStore((s) => s.settings.accountLayout)
  const layout = accountLayout(raw, accounts)
  const [name, setName] = useState('')
  const [dragged, setDragged] = useState<number | null>(null)
  const [over, setOver] = useState<string | null>(null)
  const byId = new Map(accounts.map((a) => [a.id, a]))

  const drop = (groupId: string | null, beforeId?: number) => {
    if (dragged !== null && dragged !== beforeId) void saveAccountLayout(moveAccount(layout, dragged, groupId, beforeId))
    setDragged(null)
    setOver(null)
  }
  const addGroup = (e: React.FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    void saveAccountLayout({ ...layout, groups: [...layout.groups, { id: 'g' + Date.now().toString(36), name: name.trim(), accounts: [] }] })
    setName('')
  }
  const moveGroup = (i: number, delta: number) => {
    const groups = [...layout.groups]
    groups.splice(i + delta, 0, groups.splice(i, 1)[0])
    void saveAccountLayout({ ...layout, groups })
  }
  const renameGroup = (id: string, value: string) => {
    void saveAccountLayout({ ...layout, groups: layout.groups.map((g) => (g.id === id ? { ...g, name: value.trim() } : g)) })
  }
  const removeGroup = async (id: string, groupName: string) => {
    if (!(await confirmDialog(t('acct.categoryDeleteConfirm', { name: groupName }), { danger: true, confirmLabel: t('act.delete') }))) return
    const g = layout.groups.find((x) => x.id === id)!
    void saveAccountLayout({ ...layout, ungrouped: [...layout.ungrouped, ...g.accounts], groups: layout.groups.filter((x) => x.id !== id) })
  }

  const renderList = (groupId: string | null, ids: number[]) => {
    const zone = groupId ?? ''
    return (
      <ul
        className={'organizer-list' + (over === 'end:' + zone ? ' drop-end' : '')}
        onDragOver={(e) => {
          if (dragged === null) return
          e.preventDefault()
          setOver('end:' + zone)
        }}
        onDrop={(e) => {
          e.preventDefault()
          drop(groupId)
        }}
      >
        {ids.map((id, i) => {
          const a = byId.get(id)!
          return (
            <li
              key={id}
              className={'organizer-row' + (dragged === id ? ' dragging' : '') + (over === 'before:' + id ? ' drop-before' : '')}
              draggable
              onDragStart={(e) => {
                e.dataTransfer.effectAllowed = 'move'
                e.dataTransfer.setData('text/plain', a.email)
                setDragged(id)
              }}
              onDragEnd={() => {
                setDragged(null)
                setOver(null)
              }}
              onDragOver={(e) => {
                if (dragged === null) return
                e.preventDefault()
                e.stopPropagation()
                setOver('before:' + id)
              }}
              onDrop={(e) => {
                e.preventDefault()
                e.stopPropagation()
                drop(groupId, id)
              }}
            >
              <Icon name="grip" size={16} className="muted organizer-grip" />
              <span className="dot" style={{ background: accountColor(a) }} />
              <span className="nav-label" title={a.email}>{accountLabel(a)}</span>
              <button className="icon-btn small" disabled={i === 0} aria-label={t('acct.moveUp', { name: accountLabel(a) })} title={t('acct.moveUp', { name: accountLabel(a) })} onClick={() => void saveAccountLayout(moveAccount(layout, id, groupId, ids[i - 1]))}>
                <Icon name="up" size={15} />
              </button>
              <button className="icon-btn small" disabled={i === ids.length - 1} aria-label={t('acct.moveDown', { name: accountLabel(a) })} title={t('acct.moveDown', { name: accountLabel(a) })} onClick={() => void saveAccountLayout(moveAccount(layout, id, groupId, ids[i + 2]))}>
                <Icon name="down" size={15} />
              </button>
              {layout.groups.length > 0 && (
                <select className="organizer-select" aria-label={t('acct.categoryOf', { name: accountLabel(a) })} value={zone} onChange={(e) => void saveAccountLayout(moveAccount(layout, id, e.target.value || null))}>
                  <option value="">{t('acct.noCategory')}</option>
                  {layout.groups.map((g) => (
                    <option key={g.id} value={g.id}>
                      {g.name}
                    </option>
                  ))}
                </select>
              )}
            </li>
          )
        })}
        {ids.length === 0 && <li className="muted small organizer-empty">{t('acct.categoryEmpty')}</li>}
      </ul>
    )
  }

  return (
    <section className="card organizer">
      <h3>{t('acct.organize')}</h3>
      <p className="muted small">{t('acct.organizeHint')}</p>
      {layout.groups.length > 0 && <div className="field-label">{t('acct.noCategory')}</div>}
      {renderList(null, layout.ungrouped)}
      {layout.groups.map((g, i) => (
        <div key={g.id} className="organizer-group">
          <div className="row gap organizer-group-head">
            <input key={g.name} defaultValue={g.name} maxLength={60} aria-label={t('acct.categoryName')} onBlur={(e) => (e.target.value.trim() ? e.target.value.trim() !== g.name && renameGroup(g.id, e.target.value) : (e.target.value = g.name))} onKeyDown={(e) => e.key === 'Enter' && e.currentTarget.blur()} />
            <button className="icon-btn small" disabled={i === 0} aria-label={t('acct.moveUp', { name: g.name })} title={t('acct.moveUp', { name: g.name })} onClick={() => moveGroup(i, -1)}>
              <Icon name="up" size={15} />
            </button>
            <button className="icon-btn small" disabled={i === layout.groups.length - 1} aria-label={t('acct.moveDown', { name: g.name })} title={t('acct.moveDown', { name: g.name })} onClick={() => moveGroup(i, 1)}>
              <Icon name="down" size={15} />
            </button>
            <button className="icon-btn small" aria-label={t('acct.categoryDelete', { name: g.name })} title={t('acct.categoryDelete', { name: g.name })} onClick={() => void removeGroup(g.id, g.name)}>
              <Icon name="trash" size={15} />
            </button>
          </div>
          {renderList(g.id, g.accounts)}
        </div>
      ))}
      <form className="row gap organizer-add" onSubmit={addGroup}>
        <input value={name} onChange={(e) => setName(e.target.value)} maxLength={60} placeholder={t('acct.categoryName')} aria-label={t('acct.categoryName')} />
        <button className="btn" disabled={!name.trim()}>
          <Icon name="plus" size={15} /> {t('acct.categoryAdd')}
        </button>
      </form>
    </section>
  )
}

function Accounts({ selected }: { selected?: number }) {
  const accounts = useStore((s) => s.accounts)
  const raw = useStore((s) => s.settings.accountLayout)
  if (accounts.length === 0) {
    return (
      <div className="card">
        <p>{t('acct.none')}</p>
        <a className="btn primary" href={href('add-account')}>
          <Icon name="plus" /> {t('nav.addAccount')}
        </a>
      </div>
    )
  }
  return (
    <div className="accounts-settings">
      {accounts.length > 1 && <AccountOrganizer />}
      {orderedAccounts(accountLayout(raw, accounts), accounts).map((a) => (
        <AccountCard key={a.id} account={a} open={selected === a.id || accounts.length === 1} />
      ))}
      <a className="btn" href={href('add-account')}>
        <Icon name="plus" /> {t('nav.addAccount')}
      </a>
    </div>
  )
}

function AccountCard({ account: a, open: initiallyOpen }: { account: Account; open: boolean }) {
  const folders = useStore((s) => s.folders).filter((f) => f.accountId === a.id && f.selectable)
  const [open, setOpen] = useState(initiallyOpen)
  const [form, setForm] = useState({ displayName: a.displayName, senderName: a.senderName, color: a.color || accountColor(a), signatureHtml: a.signatureHtml, syncIntervalSec: a.syncIntervalSec, username: a.username, imap: a.imap, smtp: a.smtp, password: '', tlsCaPem: '' })
  const [busy, setBusy] = useState(false)
  const [sigKey, setSigKey] = useState(0)
  const [creatingFolder, setCreatingFolder] = useState(false)
  useEffect(() => {
    setForm((f) => ({ ...f, displayName: a.displayName, senderName: a.senderName, signatureHtml: a.signatureHtml }))
    setSigKey((k) => k + 1)
  }, [a.id, a.displayName, a.senderName, a.signatureHtml])

  const patch = async (body: Record<string, unknown>, okMsg = t('acct.saved')) => {
    setBusy(true)
    try {
      await api.patch<Account>(`/accounts/${a.id}`, body)
      await Promise.allSettled([loadAccounts(), loadFolders()])
      toast(okMsg, 'success')
      setForm((f) => ({ ...f, password: '' }))
    } catch (e) {
      const test = e instanceof ApiError ? (e.data as { test?: { imapError?: string; smtpError?: string } })?.test : undefined
      toast(test ? [test.imapError, test.smtpError].filter(Boolean).join(' · ') : e instanceof ApiError ? e.message : t('err.network'), 'error')
    } finally {
      setBusy(false)
    }
  }
  const post = async (path: string, ok?: string) => {
    try {
      const r = await api.post<{ authUrl?: string; warning?: string }>(`/accounts/${a.id}/${path}`)
      if (r?.authUrl) {
        window.location.assign(r.authUrl)
        return
      }
      if (r?.warning) toast(r.warning, 'error')
      else if (ok) toast(ok, 'success')
      void loadAccounts()
    } catch (e) {
      toast(e instanceof ApiError ? e.message : t('err.network'), 'error')
    }
  }
  const remove = async () => {
    if (!(await confirmDialog(t('acct.deleteConfirm', { email: a.email }), { danger: true, confirmLabel: t('acct.delete') }))) return
    try {
      await api.del(`/accounts/${a.id}`)
      await Promise.allSettled([loadAccounts(), loadFolders()])
      // SQLite can reuse the id: a future account must not inherit this place.
      const { settings, accounts } = getState()
      void saveAccountLayout(accountLayout(settings.accountLayout, accounts.filter((x) => x.id !== a.id)))
      navigate('settings/accounts')
    } catch (e) {
      toast(e instanceof ApiError ? e.message : t('err.network'), 'error')
    }
  }
  const problem = a.status === 'error' || a.status === 'auth_error'
  return (
    <section className={'card account-card' + (problem ? ' problem' : '')}>
      <button className="account-card-head" onClick={() => setOpen((v) => !v)} aria-expanded={open}>
        <span className="dot big" style={{ background: accountColor(a) }} />
        <span className="grow">
          <strong>{accountLabel(a)}</strong>
          <span className="muted small block">
            {a.email} · {a.authType === 'oauth2' ? 'OAuth2' : 'IMAP/SMTP'} {a.isDefault && <span className="tag">{t('acct.default')}</span>}
          </span>
        </span>
        <span className={'status s-' + a.status}>{t(('acct.status.' + a.status) as never)}</span>
      </button>
      <div className="muted small">{t('acct.lastSync', { when: relativeTime(a.lastSyncOkAt) })}</div>
      {a.lastError && <p className="notice error small">{a.lastError}</p>}
      <div className="row gap wrap">
        <button className="btn small" onClick={() => void post('sync', t('act.done'))}>
          <Icon name="refresh" size={14} /> {t('acct.syncNow')}
        </button>
        {(problem || (a.authType === 'oauth2' && !a.oauthConnected)) && (
          <button className="btn small primary" onClick={() => (a.authType === 'oauth2' && !a.oauthConnected ? void startOAuth(a.provider, a.id, a.email) : void post('reconnect', t('act.done')))}>
            {t('acct.reconnect')}
          </button>
        )}
        {!a.isDefault && (
          <button className="btn small" onClick={() => void post('default', t('act.done'))}>
            {t('acct.makeDefault')}
          </button>
        )}
      </div>
      {open && (
        <div className="form account-form">
          <label className="setting toggle">
            <span>{t('acct.pause')}</span>
            <input type="checkbox" checked={a.syncEnabled} onChange={(e) => void patch({ syncEnabled: e.target.checked })} />
          </label>
          <label className="setting toggle">
            <span>{t('acct.idle')}</span>
            <input type="checkbox" checked={a.idleEnabled} onChange={(e) => void patch({ idleEnabled: e.target.checked })} />
          </label>
          <label className="setting">
            <span>{t('acct.interval')}</span>
            <select value={form.syncIntervalSec} onChange={(e) => void patch({ syncIntervalSec: Number(e.target.value) })}>
              <option value={0}>{t('acct.intervalDefault')}</option>
              {[60, 120, 300, 600, 900, 1800, 3600].map((s) => (
                <option key={s} value={s}>
                  {s < 3600 ? `${s / 60} min` : '1 h'}
                </option>
              ))}
            </select>
          </label>
          <div className="grid2">
            <label>
              {t('acct.displayName')}
              <input value={form.displayName} onChange={(e) => setForm({ ...form, displayName: e.target.value })} maxLength={200} />
            </label>
            <label>
              {t('acct.senderName')}
              <input value={form.senderName} onChange={(e) => setForm({ ...form, senderName: e.target.value })} maxLength={200} />
            </label>
          </div>
          <label className="setting">
            <span>{t('acct.color')}</span>
            <input type="color" value={form.color} onChange={(e) => setForm({ ...form, color: e.target.value })} />
          </label>
          <div>
            <div className="field-label">{t('acct.signature')}</div>
            <RichEditor key={sigKey} initialHTML={form.signatureHtml} onChange={(html) => setForm((f) => ({ ...f, signatureHtml: html }))} />
          </div>
          <div className="row end">
            <button className="btn primary" disabled={busy} onClick={() => void patch({ displayName: form.displayName, senderName: form.senderName, color: form.color, signatureHtml: form.signatureHtml })}>
              {t('act.save')}
            </button>
          </div>

          <h3>{t('acct.folders')}</h3>
          <div className="folder-toggles">
            {folders.map((f) => (
              <label key={f.id} className="check small" title={f.name}>
                <input
                  type="checkbox"
                  checked={f.syncEnabled}
                  disabled={f.role === 'inbox'}
                  onChange={(e) => void api.patch(`/folders/${f.id}`, { syncEnabled: e.target.checked }).then(() => loadFolders())}
                />{' '}
                {folderLabel(f)} <span className="muted">({f.local}/{f.total})</span>
                {!f.role && (
                  <button
                    type="button"
                    className="icon-btn small"
                    onClick={(e) => {
                      e.preventDefault()
                      void deleteFolder(f)
                    }}
                    title={t('folder.delete')}
                    aria-label={t('folder.delete')}
                  >
                    <Icon name="trash" size={14} />
                  </button>
                )}
              </label>
            ))}
          </div>
          <div className="row">
            <button className="btn small" onClick={() => setCreatingFolder(true)}>
              <Icon name="plus" size={14} /> {t('folder.new')}
            </button>
          </div>
          {creatingFolder && <NewFolderDialog account={a} openAfter={false} onClose={() => setCreatingFolder(false)} />}

          <h3>{t('acct.server')}</h3>
          {a.authType === 'oauth2' ? (
            <p className={a.oauthConnected ? 'muted' : 'notice warn'}>{a.oauthConnected ? t('acct.oauthConnected') : t('acct.oauthDisconnected')}</p>
          ) : (
            <>
              <label>
                {t('wiz.username')}
                <input value={form.username} onChange={(e) => setForm({ ...form, username: e.target.value })} maxLength={320} />
              </label>
              <label>
                {t('acct.newPassword')}
                <input type="password" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} autoComplete="new-password" maxLength={1024} />
              </label>
            </>
          )}
          <EndpointFields label={t('wiz.imap')} value={form.imap} onChange={(imap) => setForm({ ...form, imap })} />
          <EndpointFields label={t('wiz.smtp')} value={form.smtp} onChange={(smtp) => setForm({ ...form, smtp })} />
          <label>
            {t('wiz.ca')} {a.hasCustomCa && <span className="tag">✓</span>}
            <textarea rows={2} value={form.tlsCaPem} onChange={(e) => setForm({ ...form, tlsCaPem: e.target.value })} placeholder="-----BEGIN CERTIFICATE-----" spellCheck={false} />
          </label>
          <label className="setting toggle">
            <span>{t('acct.savesSent')}</span>
            <input type="checkbox" checked={a.smtpSavesSent} onChange={(e) => void patch({ smtpSavesSent: e.target.checked })} />
          </label>
          <div className="row end">
            <button
              className="btn primary"
              disabled={busy}
              onClick={() => {
                const body: Record<string, unknown> = { imap: form.imap, smtp: form.smtp }
                if (a.authType === 'password') {
                  body.username = form.username
                  if (form.password) body.password = form.password
                }
                if (form.tlsCaPem) body.tlsCaPem = form.tlsCaPem
                void patch(body)
              }}
            >
              {t('act.save')}
            </button>
          </div>
          <div className="row gap wrap danger-zone">
            {a.authType === 'oauth2' && a.oauthConnected && (
              <button className="btn small" onClick={async () => (await confirmDialog(t('acct.revokeConfirm'), { danger: true })) && void post('revoke', t('act.done'))}>
                {t('acct.revoke')}
              </button>
            )}
            <button className="btn small danger" onClick={() => void remove()}>
              {t('acct.delete')}
            </button>
          </div>
        </div>
      )}
    </section>
  )
}

const notifyKinds = ['new_mail', 'sync_error', 'auth_error', 'reauth_required', 'send_failed', 'send_ambiguous', 'quota']

function Notifications() {
  const settings = useStore((s) => s.settings)
  const [perm, setPerm] = useState(typeof Notification !== 'undefined' ? Notification.permission : 'denied')
  return (
    <div className="card form">
      <label className="setting toggle">
        <span>
          {t('set.browserNotif')}
          <span className="muted small block">{perm === 'denied' ? t('set.browserNotifDenied') : t('set.browserNotifHint')}</span>
        </span>
        <input
          type="checkbox"
          disabled={perm === 'denied'}
          checked={settings.browserNotifications === 'true' && perm === 'granted'}
          onChange={async (e) => {
            if (e.target.checked && typeof Notification !== 'undefined' && Notification.permission !== 'granted') {
              const p = await Notification.requestPermission()
              setPerm(p)
              if (p !== 'granted') return
            }
            void saveSettings({ browserNotifications: String(e.target.checked) })
          }}
        />
      </label>
      {notifyKinds.map((k) => (
        <Toggle key={k} k={'notify.' + k} label={t(('set.notifyKind.' + k) as never)} />
      ))}
    </div>
  )
}

function Security() {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [sessions, setSessions] = useState<{ createdAt: number; lastSeenAt: number; userAgent: string; ip: string; current: boolean }[]>([])
  useEffect(() => {
    api.get<{ items: typeof sessions }>('/auth/sessions').then((r) => setSessions(r.items)).catch(() => {})
  }, [])
  return (
    <>
      <form
        className="card form"
        onSubmit={async (e) => {
          e.preventDefault()
          try {
            await api.post('/auth/password', { current, new: next })
            setCurrent('')
            setNext('')
            toast(t('set.passwordChanged'), 'success')
          } catch (err) {
            toast(err instanceof ApiError ? err.message : t('err.network'), 'error')
          }
        }}
      >
        <h3>{t('set.password')}</h3>
        <label>
          {t('set.currentPassword')}
          <input type="password" value={current} onChange={(e) => setCurrent(e.target.value)} autoComplete="current-password" required />
        </label>
        <label>
          {t('set.newPassword')}
          <input type="password" value={next} onChange={(e) => setNext(e.target.value)} autoComplete="new-password" minLength={10} required />
        </label>
        <div className="row end">
          <button className="btn primary">{t('act.save')}</button>
        </div>
      </form>
      <TwoFactorSettings />
      <div className="card">
        <h3>{t('set.sessions')}</h3>
        <ul className="plain-list">
          {sessions.map((s, i) => (
            <li key={i}>
              <strong>{s.current ? t('set.currentSession') : s.ip}</strong> <span className="muted small">{s.userAgent.slice(0, 90)}</span>
              <span className="muted small block">{relativeTime(s.lastSeenAt)}</span>
            </li>
          ))}
        </ul>
        <button
          className="btn danger small"
          onClick={async () => {
            if (await confirmDialog(t('set.logoutAll') + ' ?', { danger: true })) {
              await api.post('/auth/logout-all').catch(() => {})
              location.reload()
            }
          }}
        >
          {t('set.logoutAll')}
        </button>
      </div>
    </>
  )
}

interface UserRow {
  id: number
  username: string
  isAdmin: boolean
  totpEnabled: boolean
  createdAt: number
  current: boolean
}

function Users() {
  const [users, setUsers] = useState<UserRow[] | null>(null)
  const load = () =>
    api
      .get<{ items: UserRow[] }>('/users')
      .then((r) => setUsers(r.items))
      .catch((e) => toast(e instanceof ApiError ? e.message : t('err.network'), 'error'))
  useEffect(() => {
    void load()
  }, [])
  const adminCount = users?.filter((u) => u.isAdmin).length ?? 0

  const setAdmin = async (u: UserRow, isAdmin: boolean) => {
    if (!isAdmin && u.current && !(await confirmDialog(t('users.demoteSelf'), { danger: true, confirmLabel: t('users.demote') }))) return
    try {
      await api.patch('/users/' + u.id, { isAdmin })
      if (u.current && !isAdmin) {
        // Our own rights changed: hide the admin-only parts.
        setState((s) => (s.me?.user ? { me: { ...s.me, user: { ...s.me.user, isAdmin: false } } } : {}))
        navigate('settings/general')
        return
      }
      toast(isAdmin ? t('users.promoted', { name: u.username }) : t('users.demoted', { name: u.username }), 'success')
      await load()
    } catch (e) {
      toast(e instanceof ApiError ? (e.code === 'last_admin' ? t('users.lastAdmin') : e.message) : t('err.network'), 'error')
    }
  }

  const remove = async (u: UserRow) => {
    if (!(await confirmDialog(t('users.deleteConfirm', { name: u.username }), { danger: true, confirmLabel: t('act.delete') }))) return
    try {
      await api.del('/users/' + u.id)
      toast(t('users.deleted', { name: u.username }), 'success')
      await load()
    } catch (e) {
      toast(e instanceof ApiError ? (e.code === 'last_admin' ? t('users.lastAdmin') : e.message) : t('err.network'), 'error')
    }
  }

  if (!users) return <p className="muted">{t('app.loading')}</p>
  return (
    <>
      <div className="card">
        <p className="muted small">{t('users.hint')}</p>
        <ul className="plain-list user-list">
          {users.map((u) => (
            <li key={u.id} className="row gap wrap">
              <span className="grow">
                <strong>{u.username}</strong> {u.current && <span className="tag">{t('users.you')}</span>} {u.isAdmin && <span className="tag ok">{t('users.admin')}</span>}{' '}
                {u.totpEnabled && <span className="tag">2FA</span>}
                <span className="muted small block">{t('users.since', { date: new Date(u.createdAt * 1000).toLocaleDateString() })}</span>
              </span>
              <label className="check small" title={u.isAdmin && adminCount === 1 ? t('users.lastAdmin') : undefined}>
                <input type="checkbox" checked={u.isAdmin} disabled={u.isAdmin && adminCount === 1} onChange={(e) => void setAdmin(u, e.target.checked)} /> {t('users.admin')}
              </label>
              {!u.current && (
                <button className="icon-btn small" onClick={() => void remove(u)} title={t('users.delete', { name: u.username })} aria-label={t('users.delete', { name: u.username })}>
                  <Icon name="trash" size={14} />
                </button>
              )}
            </li>
          ))}
        </ul>
      </div>
      <NewUserForm onCreated={load} />
    </>
  )
}

function NewUserForm({ onCreated }: { onCreated: () => Promise<unknown> }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [isAdmin, setIsAdmin] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await api.post('/users', { username: username.trim(), password, isAdmin })
      toast(t('users.created', { name: username.trim() }), 'success')
      setUsername('')
      setPassword('')
      setIsAdmin(false)
      await onCreated()
    } catch (err) {
      if (err instanceof ApiError && err.code === 'username_taken') setError(t('users.taken'))
      else if (err instanceof ApiError && err.code === 'invalid_username') setError(t('users.invalidName'))
      else if (err instanceof ApiError && err.code === 'weak_password') setError(t('users.weakPassword'))
      else setError(err instanceof ApiError ? err.message : t('err.network'))
    } finally {
      setBusy(false)
    }
  }
  return (
    <form className="card form" onSubmit={(e) => void submit(e)}>
      <h3>{t('users.new')}</h3>
      <div className="grid2">
        <label>
          {t('login.username')}
          <input value={username} onChange={(e) => setUsername(e.target.value)} required maxLength={64} pattern="[\p{L}\p{N}._@+\-]+" autoComplete="off" spellCheck={false} />
        </label>
        <label>
          {t('login.password')}
          <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} required minLength={10} maxLength={1024} autoComplete="new-password" />
        </label>
      </div>
      <label className="setting toggle">
        <span>{t('users.makeAdmin')}</span>
        <input type="checkbox" checked={isAdmin} onChange={(e) => setIsAdmin(e.target.checked)} />
      </label>
      <p className="muted small">{t('users.newHint')}</p>
      {error && <p className="notice error small">{error}</p>}
      <div className="row end">
        <button className="btn primary" disabled={busy}>
          {t('users.create')}
        </button>
      </div>
    </form>
  )
}

interface Status {
  version: string
  goVersion: string
  schemaVersion: number
  database: { accounts: number; messages: number; bodies: number; blobBytes: number; dbBytes: number; blobs: number }
  runtime: { uptimeSeconds: number; heapAllocBytes: number; sysBytes: number; rssBytes: number; goroutines: number; cpuSeconds: number; imapConnections: number; idleConnections: number; imapErrors: number; smtpErrors: number; smtpSent: number }
  keyId: string
}

function About() {
  const [s, setS] = useState<Status | null>(null)
  useEffect(() => {
    const load = () => api.get<Status>('/status').then(setS).catch(() => {})
    void load()
    const h = setInterval(load, 5000)
    return () => clearInterval(h)
  }, [])
  if (!s) return <p className="muted">{t('app.loading')}</p>
  const rt = s.runtime
  const rows: [string, string][] = [
    [t('about.version'), `${s.version} (${s.goVersion}, schema v${s.schemaVersion})`],
    [t('about.uptime'), `${Math.floor(rt.uptimeSeconds / 3600)} h ${Math.floor((rt.uptimeSeconds % 3600) / 60)} min`],
    [t('about.memory'), formatSize(rt.heapAllocBytes)],
    [t('about.sys'), formatSize(rt.sysBytes)],
    ['RSS', rt.rssBytes ? formatSize(rt.rssBytes) : '—'],
    [t('about.cpu'), `${rt.cpuSeconds.toFixed(1)} s`],
    [t('about.goroutines'), String(rt.goroutines)],
    [t('about.connections'), `${rt.imapConnections} (IDLE : ${rt.idleConnections})`],
    [t('about.db'), formatSize(s.database.dbBytes)],
    [t('about.messages'), `${s.database.messages} (${s.database.bodies} corps)`],
    [t('about.cache'), `${formatSize(s.database.blobBytes)} (${s.database.blobs})`],
    [t('about.errors'), `${rt.imapErrors} / ${rt.smtpErrors}`],
    ['Key ID', s.keyId],
  ]
  return (
    <div className="card">
      <table className="kv">
        <tbody>
          {rows.map(([k, v]) => (
            <tr key={k}>
              <th>{k}</th>
              <td>{v}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="muted small">
        <a href="/api/v1/metrics" target="_blank" rel="noreferrer">/api/v1/metrics</a> · <a href="/api/v1/openapi.yaml" target="_blank" rel="noreferrer">OpenAPI</a>
      </p>
    </div>
  )
}
