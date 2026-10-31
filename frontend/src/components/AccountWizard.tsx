import { useEffect, useState, type FormEvent } from 'react'
import { ApiError, api } from '../api'
import { hasKey, t } from '../i18n'
import { navigate } from '../router'
import { loadAccounts, loadFolders, toast } from '../store'
import type { Account, Endpoint, Provider } from '../types'
import { Icon } from './Icons'

interface TestResult {
  imapOk: boolean
  imapError?: string
  smtpOk: boolean
  smtpError?: string
}

export async function startOAuth(provider: string, accountId?: number, loginHint?: string) {
  try {
    const r = await api.post<{ authUrl: string }>(`/oauth/${provider}/start`, { accountId: accountId ?? 0, loginHint: loginHint ?? '' })
    // Top-level navigation to the provider's official sign-in page.
    window.location.assign(r.authUrl)
  } catch (e) {
    toast(e instanceof ApiError ? e.message : t('err.network'), 'error')
  }
}

export function EndpointFields({ label, value, onChange }: { label: string; value: Endpoint; onChange: (e: Endpoint) => void }) {
  return (
    <fieldset className="endpoint">
      <legend>{label}</legend>
      <div className="grid3">
        <label>
          {t('wiz.host')}
          <input value={value.host} onChange={(e) => onChange({ ...value, host: e.target.value.trim() })} required maxLength={253} autoCapitalize="off" spellCheck={false} />
        </label>
        <label>
          {t('wiz.port')}
          <input type="number" min={1} max={65535} value={value.port} onChange={(e) => onChange({ ...value, port: Number(e.target.value) })} required />
        </label>
        <label>
          {t('wiz.security')}
          <select value={value.security} onChange={(e) => onChange({ ...value, security: e.target.value as Endpoint['security'] })}>
            <option value="tls">{t('wiz.tls')}</option>
            <option value="starttls">{t('wiz.starttls')}</option>
          </select>
        </label>
      </div>
    </fieldset>
  )
}

const featured = ['google', 'microsoft', 'yahoo', 'icloud', 'fastmail', 'zoho', 'proton', 'orange', 'free', 'laposte', 'custom']

export function AccountWizard() {
  const [providers, setProviders] = useState<Provider[]>([])
  const [provider, setProvider] = useState<Provider | null>(null)
  const [forcePassword, setForcePassword] = useState(false)

  useEffect(() => {
    api.get<{ items: Provider[] }>('/providers').then((r) => setProviders(r.items)).catch(() => {})
  }, [])

  if (!provider) {
    const list = featured.map((id) => providers.find((p) => p.id === id)).filter(Boolean) as Provider[]
    return (
      <div className="page">
        <h1>{t('wiz.title')}</h1>
        <p className="muted">{t('wiz.choose')}</p>
        <div className="provider-grid">
          {list.map((p) => (
            <button key={p.id} className="provider-tile" onClick={() => { setProvider(p); setForcePassword(false) }}>
              <span className={'provider-logo p-' + p.id} aria-hidden="true">{p.id === 'custom' ? '@' : p.name[0]}</span>
              <span>{p.id === 'custom' ? t('wiz.other') : p.name}</span>
              {p.authMethods.includes('oauth2') && <span className="tag">OAuth2</span>}
            </button>
          ))}
        </div>
      </div>
    )
  }

  const oauth = provider.authMethods.includes('oauth2')
  const passwordAllowed = provider.authMethods.includes('password')
  return (
    <div className="page narrow">
      <button className="link" onClick={() => setProvider(null)}>
        <Icon name="back" size={14} /> {t('wiz.back')}
      </button>
      <h1>{provider.id === 'custom' ? t('wiz.other') : provider.name}</h1>
      {oauth && !forcePassword && (
        <div className="card">
          {provider.oauthConfigured ? (
            <>
              <p>{t('wiz.oauthHint', { name: provider.name })}</p>
              <button className="btn primary" onClick={() => void startOAuth(provider.id)}>
                {t('wiz.oauthButton', { name: provider.name })}
              </button>
            </>
          ) : (
            <p className="notice warn">{t('wiz.oauthNotConfigured', { name: provider.name })}</p>
          )}
          {passwordAllowed ? (
            <p>
              <button className="link" onClick={() => setForcePassword(true)}>
                {t('wiz.usePassword')}
              </button>
            </p>
          ) : (
            <p className="muted small">{t('wiz.oauthOnly')}</p>
          )}
        </div>
      )}
      {(!oauth || forcePassword) && passwordAllowed && <PasswordForm provider={provider} />}
    </div>
  )
}

function PasswordForm({ provider }: { provider: Provider }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [senderName, setSenderName] = useState('')
  const [username, setUsername] = useState('')
  const [imap, setImap] = useState<Endpoint>(provider.imap)
  const [smtp, setSmtp] = useState<Endpoint>(provider.smtp)
  const [ca, setCa] = useState('')
  const [advanced, setAdvanced] = useState(provider.id === 'custom' || provider.id === 'proton')
  const [result, setResult] = useState<TestResult | null>(null)
  const [busy, setBusy] = useState<'' | 'test' | 'add'>('')
  const [error, setError] = useState('')
  const [allowSmtpError, setAllowSmtpError] = useState(false)

  // Suggest imap./smtp.<domain> for custom servers until the user edits them.
  const [hostsEdited, setHostsEdited] = useState(false)
  useEffect(() => {
    if (provider.id !== 'custom' || hostsEdited) return
    const domain = email.split('@')[1]
    if (!domain || !domain.includes('.')) return
    setImap((v) => ({ ...v, host: 'imap.' + domain }))
    setSmtp((v) => ({ ...v, host: 'smtp.' + domain }))
  }, [email, provider.id, hostsEdited])

  const body = () => ({
    provider: provider.id, email: email.trim(), username: (username || email).trim(), password, senderName,
    displayName: email.trim(), imap, smtp, tlsCaPem: ca, allowSmtpError,
  })

  const test = async () => {
    setBusy('test')
    setError('')
    try {
      setResult(await api.post<TestResult>('/accounts/test', body()))
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('err.network'))
    } finally {
      setBusy('')
    }
  }

  const add = async (e: FormEvent) => {
    e.preventDefault()
    setBusy('add')
    setError('')
    try {
      const a = await api.post<Account>('/accounts', body())
      setPassword('')
      toast(t('wiz.added'), 'success')
      await Promise.allSettled([loadAccounts(), loadFolders()])
      navigate('settings/accounts?id=' + a.id)
    } catch (err) {
      if (err instanceof ApiError && err.status === 422) {
        setResult((err.data as { test: TestResult }).test)
      } else setError(err instanceof ApiError ? err.message : t('err.network'))
    } finally {
      setBusy('')
    }
  }

  const hint = provider.passwordHint && hasKey('hint.' + provider.passwordHint) ? t(('hint.' + provider.passwordHint) as never) : ''
  return (
    <form className="card form" onSubmit={add}>
      {hint && <p className="notice info small">{hint}</p>}
      <label>
        {t('wiz.email')}
        <input type="email" value={email} onChange={(e) => setEmail(e.target.value)} required autoComplete="off" maxLength={320} />
      </label>
      <label>
        {provider.passwordHint ? t('wiz.appPassword') : t('wiz.password')}
        <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} required autoComplete="new-password" maxLength={1024} />
      </label>
      <label>
        {t('wiz.displayName')}
        <input value={senderName} onChange={(e) => setSenderName(e.target.value)} maxLength={200} />
      </label>
      <button type="button" className="link small" onClick={() => setAdvanced((v) => !v)} aria-expanded={advanced}>
        {t('wiz.advanced')}
      </button>
      {advanced && (
        <>
          <label>
            {t('wiz.username')}
            <input value={username} placeholder={email} onChange={(e) => setUsername(e.target.value)} maxLength={320} autoCapitalize="off" />
          </label>
          <EndpointFields label={t('wiz.imap')} value={imap} onChange={(v) => { setHostsEdited(true); setImap(v) }} />
          <EndpointFields label={t('wiz.smtp')} value={smtp} onChange={(v) => { setHostsEdited(true); setSmtp(v) }} />
          <label>
            {t('wiz.ca')}
            <textarea rows={3} value={ca} onChange={(e) => setCa(e.target.value)} placeholder="-----BEGIN CERTIFICATE-----" spellCheck={false} />
            <span className="muted small">{t('wiz.caHint')}</span>
          </label>
        </>
      )}
      {result && (
        <div className="test-result">
          <p className={result.imapOk ? 'ok' : 'error'}>
            <Icon name={result.imapOk ? 'check' : 'alert'} size={15} /> {result.imapOk ? t('wiz.imapOk') : t('wiz.imapErr', { err: result.imapError ?? '' })}
          </p>
          <p className={result.smtpOk ? 'ok' : 'error'}>
            <Icon name={result.smtpOk ? 'check' : 'alert'} size={15} /> {result.smtpOk ? t('wiz.smtpOk') : t('wiz.smtpErr', { err: result.smtpError ?? '' })}
          </p>
          {result.imapOk && !result.smtpOk && (
            <label className="check small">
              <input type="checkbox" checked={allowSmtpError} onChange={(e) => setAllowSmtpError(e.target.checked)} /> {t('wiz.allowSmtpError')}
            </label>
          )}
        </div>
      )}
      {error && <p className="notice error small">{error}</p>}
      <div className="row gap">
        <button type="button" className="btn" onClick={() => void test()} disabled={!!busy || !email || !password}>
          {busy === 'test' ? t('wiz.testing') : t('wiz.test')}
        </button>
        <button className="btn primary" disabled={!!busy}>
          {busy === 'add' ? t('wiz.testing') : t('wiz.add')}
        </button>
      </div>
    </form>
  )
}
