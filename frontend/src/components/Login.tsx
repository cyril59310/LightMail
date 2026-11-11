import { useState, type FormEvent } from 'react'
import { ApiError, api, setCsrfToken } from '../api'
import { getLanguage, languages, setLanguage, t, useLanguage } from '../i18n'
import { saveSettings } from '../store'
import type { Me } from '../types'
import { BrandMark } from './Icons'

export function Login({ me, onLogin }: { me: Me; onLogin: () => void }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  // Second step, shown once the password is accepted for a 2FA user.
  const [needCode, setNeedCode] = useState(false)
  const [code, setCode] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const lang = useLanguage()
  // Language picked on this page: saved to the user's settings once signed in,
  // otherwise their saved language would replace it right after login.
  const [pickedLang, setPickedLang] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      const r = await api.post<{ csrfToken: string }>('/auth/login', { username, password, ...(needCode ? { code } : {}) })
      setCsrfToken(r.csrfToken)
      setPassword('')
      setCode('')
      if (pickedLang) await saveSettings({ language: getLanguage() }).catch(() => {})
      onLogin()
    } catch (err) {
      if (err instanceof ApiError && err.status === 429) setError(t('login.locked'))
      else if (err instanceof ApiError && err.code === 'totp_required') setNeedCode(true)
      else if (err instanceof ApiError && err.code === 'invalid_totp') {
        setCode('')
        setError(t('login.invalidCode'))
      } else if (err instanceof ApiError && err.status === 401) setError(t('login.invalid'))
      else setError(t('err.network'))
    } finally {
      setBusy(false)
    }
  }

  const back = () => {
    setNeedCode(false)
    setCode('')
    setPassword('')
    setError('')
  }

  return (
    <div className="login-page">
      <form className="login-card" onSubmit={submit}>
        <div className="brand big">
          <BrandMark logoVersion={me.logoVersion} />
          {me.appName}
        </div>
        <h1>{needCode ? t('login.2faTitle') : t('login.title')}</h1>
        {me.setupRequired && <p className="notice warn">{t('login.setup')}</p>}
        {needCode ? (
          <>
            <p className="muted small">{t('login.2faHint')}</p>
            <label>
              {t('login.code')}
              <input
                key="code"
                autoFocus
                autoComplete="one-time-code"
                inputMode="text"
                spellCheck={false}
                value={code}
                onChange={(e) => setCode(e.target.value)}
                required
                maxLength={32}
                placeholder="123456"
              />
            </label>
          </>
        ) : (
          <>
            <label>
              {t('login.username')}
              <input autoFocus autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required maxLength={200} />
            </label>
            <label>
              {t('login.password')}
              <input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required maxLength={1024} />
            </label>
          </>
        )}
        {error && (
          <p className="notice error" role="alert">
            {error}
          </p>
        )}
        <button className="btn primary block" disabled={busy}>
          {needCode ? t('login.verify') : t('login.submit')}
        </button>
        {needCode && (
          <button type="button" className="btn block" onClick={back}>
            {t('act.back')}
          </button>
        )}
        <div className="login-footer muted small">
          <span>v{me.version}</span>
          <select
            className="login-lang"
            aria-label={t('set.language')}
            value={lang}
            onChange={(e) => {
              setLanguage(e.target.value)
              setPickedLang(true)
            }}
          >
            {languages.map((l) => (
              <option key={l.code} value={l.code}>
                {l.name}
              </option>
            ))}
          </select>
        </div>
      </form>
    </div>
  )
}
