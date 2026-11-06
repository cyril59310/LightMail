import { useState, type FormEvent } from 'react'
import { ApiError, api, setCsrfToken } from '../api'
import { t } from '../i18n'
import type { Me } from '../types'
import { BrandMark } from './Icons'

export function Login({ me, onLogin }: { me: Me; onLogin: () => void }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      const r = await api.post<{ csrfToken: string }>('/auth/login', { username, password })
      setCsrfToken(r.csrfToken)
      setPassword('')
      onLogin()
    } catch (err) {
      if (err instanceof ApiError && err.status === 429) setError(t('login.locked'))
      else if (err instanceof ApiError && err.status === 401) setError(t('login.invalid'))
      else setError(t('err.network'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="login-page">
      <form className="login-card" onSubmit={submit}>
        <div className="brand big">
          <BrandMark logoVersion={me.logoVersion} />
          {me.appName}
        </div>
        <h1>{t('login.title')}</h1>
        {me.setupRequired && <p className="notice warn">{t('login.setup')}</p>}
        <label>
          {t('login.username')}
          <input autoFocus autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required maxLength={200} />
        </label>
        <label>
          {t('login.password')}
          <input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required maxLength={1024} />
        </label>
        {error && (
          <p className="notice error" role="alert">
            {error}
          </p>
        )}
        <button className="btn primary block" disabled={busy}>
          {t('login.submit')}
        </button>
        <p className="muted small center">v{me.version}</p>
      </form>
    </div>
  )
}
