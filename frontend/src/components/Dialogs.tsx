// Confirmation dialogs and toasts (no blocking window.confirm).
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { t } from '../i18n'
import { dismissToast, useStore } from '../store'
import { Icon } from './Icons'

interface ConfirmReq {
  message: string
  confirmLabel?: string
  danger?: boolean
  resolve: (ok: boolean) => void
}

let pushConfirm: ((r: ConfirmReq) => void) | null = null

/** Promise-based confirmation rendered in-app. */
export function confirmDialog(message: string, opts: { confirmLabel?: string; danger?: boolean } = {}): Promise<boolean> {
  return new Promise((resolve) => {
    if (!pushConfirm) return resolve(false)
    pushConfirm({ message, ...opts, resolve })
  })
}

export function ConfirmHost() {
  const [req, setReq] = useState<ConfirmReq | null>(null)
  const okRef = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    pushConfirm = setReq
    return () => {
      pushConfirm = null
    }
  }, [])
  useEffect(() => {
    if (req) okRef.current?.focus()
  }, [req])
  if (!req) return null
  const close = (ok: boolean) => {
    req.resolve(ok)
    setReq(null)
  }
  return (
    <Modal onClose={() => close(false)} small>
      <p className="confirm-text">{req.message}</p>
      <div className="row end gap">
        <button className="btn" onClick={() => close(false)}>
          {t('act.cancel')}
        </button>
        <button ref={okRef} className={'btn ' + (req.danger ? 'danger' : 'primary')} onClick={() => close(true)}>
          {req.confirmLabel ?? t('act.confirm')}
        </button>
      </div>
    </Modal>
  )
}

export function Modal({ children, onClose, title, small, wide }: { children: ReactNode; onClose: () => void; title?: string; small?: boolean; wide?: boolean }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        onClose()
      }
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [onClose])
  return (
    <div className="modal-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className={'modal' + (small ? ' small' : '') + (wide ? ' wide' : '')} role="dialog" aria-modal="true" aria-label={title}>
        {title && (
          <div className="modal-head">
            <h2>{title}</h2>
            <button className="icon-btn" onClick={onClose} aria-label={t('act.close')}>
              <Icon name="x" />
            </button>
          </div>
        )}
        <div className="modal-body">{children}</div>
      </div>
    </div>
  )
}

export function Toasts() {
  const toasts = useStore((s) => s.toasts)
  return (
    <div className="toasts" role="status" aria-live="polite">
      {toasts.map((x) => (
        <div key={x.id} className={'toast ' + x.kind}>
          <span>{x.text}</span>
          {x.action && (
            <button
              className="toast-action"
              onClick={() => {
                x.action!.run()
                dismissToast(x.id)
              }}
            >
              {x.action.label}
            </button>
          )}
          <button className="icon-btn small" onClick={() => dismissToast(x.id)} aria-label={t('act.close')}>
            <Icon name="x" size={14} />
          </button>
        </div>
      ))}
    </div>
  )
}
