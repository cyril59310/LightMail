// Two-factor authentication (TOTP) settings: set-up with a QR code,
// recovery codes, deactivation.
import { useEffect, useMemo, useState, type FormEvent } from 'react'
import qrcode from 'qrcode-generator'
import { ApiError, api } from '../api'
import { t } from '../i18n'
import { setState, toast, useStore } from '../store'

type Step = { kind: 'idle' } | { kind: 'password' } | { kind: 'scan'; secret: string; uri: string } | { kind: 'codes'; codes: string[] } | { kind: 'disable' } | { kind: 'regenerate' }

function errorText(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.code === 'invalid_credentials') return t('set.2fa.badPassword')
    if (e.code === 'invalid_totp') return t('login.invalidCode')
    if (e.status === 429) return t('login.locked')
    return e.message
  }
  return t('err.network')
}

function QRCode({ text }: { text: string }) {
  const src = useMemo(() => {
    const qr = qrcode(0, 'M')
    qr.addData(text)
    qr.make()
    return qr.createDataURL(5, 4)
  }, [text])
  return <img className="qr-code" src={src} alt={t('set.2fa.qrAlt')} width={220} height={220} />
}

function RecoveryCodes({ codes, onDone }: { codes: string[]; onDone: () => void }) {
  const text = codes.join('\n')
  const download = () => {
    const url = URL.createObjectURL(new Blob([text + '\n'], { type: 'text/plain' }))
    const a = document.createElement('a')
    a.href = url
    a.download = 'lightmail-recovery-codes.txt'
    a.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  }
  return (
    <>
      <p className="notice warn small">{t('set.2fa.codesHint')}</p>
      <ul className="recovery-codes">
        {codes.map((c) => (
          <li key={c}>
            <code>{c}</code>
          </li>
        ))}
      </ul>
      <div className="row gap wrap">
        <button
          type="button"
          className="btn small"
          onClick={() =>
            void navigator.clipboard?.writeText(text).then(
              () => toast(t('set.2fa.copied'), 'success'),
              () => {},
            )
          }
        >
          {t('set.2fa.copy')}
        </button>
        <button type="button" className="btn small" onClick={download}>
          {t('set.2fa.download')}
        </button>
        <span className="grow" />
        <button type="button" className="btn primary small" onClick={onDone}>
          {t('set.2fa.saved')}
        </button>
      </div>
    </>
  )
}

export function TwoFactorSettings() {
  const enabled = useStore((s) => !!s.me?.user?.totpEnabled)
  const [recoveryLeft, setRecoveryLeft] = useState<number | null>(null)
  const [step, setStep] = useState<Step>({ kind: 'idle' })
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const refresh = () =>
    api
      .get<{ enabled: boolean; recoveryLeft: number }>('/auth/2fa')
      .then((r) => {
        setRecoveryLeft(r.recoveryLeft)
        setEnabledInStore(r.enabled)
      })
      .catch(() => {})
  useEffect(() => {
    void refresh()
  }, [])

  const go = (s: Step) => {
    setStep(s)
    setPassword('')
    setCode('')
    setError('')
  }
  const run = async (e: FormEvent, fn: () => Promise<void>) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await fn()
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  const codeInput = (
    <label>
      {t('login.code')}
      <input autoComplete="one-time-code" spellCheck={false} value={code} onChange={(e) => setCode(e.target.value)} required maxLength={32} placeholder="123456" />
    </label>
  )
  const passwordInput = (
    <label>
      {t('set.currentPassword')}
      <input type="password" autoFocus autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required maxLength={1024} />
    </label>
  )
  const actions = (submit: string, danger = false) => (
    <div className="row end gap">
      <button type="button" className="btn" onClick={() => go({ kind: 'idle' })} disabled={busy}>
        {t('act.cancel')}
      </button>
      <button className={'btn ' + (danger ? 'danger' : 'primary')} disabled={busy}>
        {submit}
      </button>
    </div>
  )

  return (
    <div className="card form">
      <h3>
        {t('set.2fa')} <span className={'tag' + (enabled ? ' ok' : '')}>{enabled ? t('set.2fa.on') : t('set.2fa.off')}</span>
      </h3>
      {step.kind === 'idle' && (
        <>
          <p className="muted small">{enabled ? t('set.2fa.onHint', { n: recoveryLeft ?? '…' }) : t('set.2fa.offHint')}</p>
          {enabled && recoveryLeft !== null && recoveryLeft <= 2 && <p className="notice warn small">{t('set.2fa.fewCodes')}</p>}
          <div className="row gap wrap">
            {enabled ? (
              <>
                <button className="btn small" onClick={() => go({ kind: 'regenerate' })}>
                  {t('set.2fa.regenerate')}
                </button>
                <button className="btn danger small" onClick={() => go({ kind: 'disable' })}>
                  {t('set.2fa.disable')}
                </button>
              </>
            ) : (
              <button className="btn primary small" onClick={() => go({ kind: 'password' })}>
                {t('set.2fa.enable')}
              </button>
            )}
          </div>
        </>
      )}

      {step.kind === 'password' && (
        <form
          className="form"
          onSubmit={(e) =>
            void run(e, async () => {
              const r = await api.post<{ secret: string; uri: string }>('/auth/2fa/setup', { password })
              go({ kind: 'scan', ...r })
            })
          }
        >
          <p className="muted small">{t('set.2fa.passwordHint')}</p>
          {passwordInput}
          {error && <p className="notice error small">{error}</p>}
          {actions(t('act.continue'))}
        </form>
      )}

      {step.kind === 'scan' && (
        <form
          className="form"
          onSubmit={(e) =>
            void run(e, async () => {
              const r = await api.post<{ recoveryCodes: string[] }>('/auth/2fa/enable', { code })
              setEnabledInStore(true)
              setRecoveryLeft(r.recoveryCodes.length)
              go({ kind: 'codes', codes: r.recoveryCodes })
            })
          }
        >
          <p className="muted small">{t('set.2fa.scanHint')}</p>
          <div className="qr-wrap">
            <QRCode text={step.uri} />
          </div>
          <p className="muted small">
            {t('set.2fa.manual')} <code className="totp-secret">{step.secret.replace(/(.{4})/g, '$1 ').trim()}</code>
          </p>
          {codeInput}
          {error && <p className="notice error small">{error}</p>}
          {actions(t('set.2fa.activate'))}
        </form>
      )}

      {step.kind === 'codes' && <RecoveryCodes codes={step.codes} onDone={() => go({ kind: 'idle' })} />}

      {step.kind === 'regenerate' && (
        <form
          className="form"
          onSubmit={(e) =>
            void run(e, async () => {
              const r = await api.post<{ recoveryCodes: string[] }>('/auth/2fa/recovery', { code })
              setRecoveryLeft(r.recoveryCodes.length)
              go({ kind: 'codes', codes: r.recoveryCodes })
            })
          }
        >
          <p className="muted small">{t('set.2fa.regenerateHint')}</p>
          {codeInput}
          {error && <p className="notice error small">{error}</p>}
          {actions(t('set.2fa.regenerate'))}
        </form>
      )}

      {step.kind === 'disable' && (
        <form
          className="form"
          onSubmit={(e) =>
            void run(e, async () => {
              await api.post('/auth/2fa/disable', { password, code })
              setEnabledInStore(false)
              setRecoveryLeft(0)
              go({ kind: 'idle' })
              toast(t('set.2fa.disabled'), 'success')
            })
          }
        >
          <p className="muted small">{t('set.2fa.disableHint')}</p>
          {passwordInput}
          {codeInput}
          {error && <p className="notice error small">{error}</p>}
          {actions(t('set.2fa.disable'), true)}
        </form>
      )}
    </div>
  )
}

function setEnabledInStore(enabled: boolean) {
  setState((s) => (s.me?.user ? { me: { ...s.me, user: { ...s.me.user, totpEnabled: enabled } } } : {}))
}
