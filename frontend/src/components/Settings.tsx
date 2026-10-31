import { useEffect, useState } from 'react'
import { ApiError, api } from '../api'
import { setLanguage, t } from '../i18n'
import { href, navigate, useRoute } from '../router'
import { loadAccounts, loadFolders, saveSettings, toast, useStore } from '../store'
import type { Account } from '../types'
import { accountColor, accountLabel, folderLabel, formatSize, relativeTime } from '../util'
import { EndpointFields, startOAuth } from './AccountWizard'
import { confirmDialog } from './Dialogs'
import { RichEditor } from './Editor'
import { Icon } from './Icons'

const tabs = ['general', 'accounts', 'notifications', 'security', 'about'] as const

export function SettingsPage() {
  const route = useRoute()
  const tab = (route.path[1] as (typeof tabs)[number]) || 'general'
  return (
    <div className="page settings">
      <h1>{t('set.title')}</h1>
      <div className="tabs" role="tablist">
        {tabs.map((x) => (
          <a key={x} role="tab" aria-selected={tab === x} className={'tab' + (tab === x ? ' active' : '')} href={href('settings/' + x)}>
            {t(('set.' + x) as never)}
          </a>
        ))}
      </div>
      {tab === 'general' && <General />}
      {tab === 'accounts' && <Accounts selected={Number(route.query.get('id')) || undefined} />}
      {tab === 'notifications' && <Notifications />}
      {tab === 'security' && <Security />}
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
    </div>
  )
}

function Accounts({ selected }: { selected?: number }) {
  const accounts = useStore((s) => s.accounts)
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
      {accounts.map((a) => (
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
              </label>
            ))}
          </div>

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
