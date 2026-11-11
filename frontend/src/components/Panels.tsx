import { ApiError, api } from '../api'
import { t } from '../i18n'
import { deleteNotification, loadNotifications, loadOutbox, toast, useStore } from '../store'
import type { OutboxItem } from '../types'
import { relativeTime, shortDate } from '../util'
import { Icon } from './Icons'

export function OutboxView() {
  const outbox = useStore((s) => s.outbox)
  const accounts = useStore((s) => s.accounts)
  const run = async (fn: () => Promise<unknown>) => {
    try {
      await fn()
      await loadOutbox()
    } catch (e) {
      toast(e instanceof ApiError ? e.message : t('err.network'), 'error')
    }
  }
  const label = (o: OutboxItem) => {
    switch (o.status) {
      case 'retry':
        return t('outbox.retry', { time: shortDate(o.nextAttemptAt) })
      default:
        return t(('outbox.' + o.status) as never)
    }
  }
  return (
    <div className="page">
      <h1>{t('outbox.title')}</h1>
      {outbox.length === 0 && <p className="muted">{t('outbox.empty')}</p>}
      <ul className="outbox-list">
        {outbox.map((o) => (
          <li key={o.id} className={'card outbox-item s-' + o.status}>
            <div className="row gap">
              <strong className="grow ellipsis">{o.subject || t('list.noSubject')}</strong>
              <span className={'status s-' + o.status}>{label(o)}</span>
            </div>
            <div className="muted small">
              {accounts.find((a) => a.id === o.accountId)?.email} → {o.recipients.join(', ')} · {relativeTime(o.createdAt)}
            </div>
            {o.lastError && <p className="notice error small">{o.lastError}</p>}
            {o.status === 'ambiguous' && <p className="notice warn small">{t('outbox.ambiguousHelp')}</p>}
            <div className="row gap">
              {(o.status === 'scheduled' || o.status === 'retry') && (
                <button className="btn small" onClick={() => run(() => api.post(`/outbox/${o.id}/cancel`))}>
                  {t('act.cancel')}
                </button>
              )}
              {(o.status === 'failed' || o.status === 'ambiguous') && (
                <button className="btn small primary" onClick={() => run(() => api.post(`/outbox/${o.id}/retry`))}>
                  {t('act.retry')}
                </button>
              )}
              {['sent', 'failed', 'canceled', 'ambiguous'].includes(o.status) && (
                <button className="btn small" onClick={() => run(() => api.del(`/outbox/${o.id}`))}>
                  {t('act.delete')}
                </button>
              )}
            </div>
          </li>
        ))}
      </ul>
    </div>
  )
}

export function NotificationsPanel({ onClose }: { onClose: () => void }) {
  const items = useStore((s) => s.notifications)
  return (
    <div className="popover notif-panel" role="dialog" aria-label={t('notif.title')}>
      <div className="row gap">
        <strong className="grow">{t('notif.title')}</strong>
        <button
          className="link small"
          onClick={async () => {
            await api.post('/notifications/read', { ids: [] }).catch(() => {})
            void loadNotifications()
          }}
        >
          {t('notif.markAll')}
        </button>
        {items.length > 0 && (
          <button className="link small" onClick={() => void deleteNotification()}>
            {t('notif.clearAll')}
          </button>
        )}
        <button className="icon-btn small" onClick={onClose} aria-label={t('act.close')}>
          <Icon name="x" size={14} />
        </button>
      </div>
      {items.length === 0 && <p className="muted small">{t('notif.empty')}</p>}
      <ul className="plain-list">
        {items.slice(0, 30).map((n) => (
          <li key={n.id} className={`notif-item${n.readAt ? ' read' : ''}`}>
            <div className="grow">
              <strong>{n.title}</strong>
              {n.body && <span className="block small">{n.body}</span>}
              <span className="muted small">{relativeTime(n.createdAt)}</span>
            </div>
            <button className="icon-btn small" onClick={() => void deleteNotification(n.id)} aria-label={t('act.delete')} title={t('act.delete')}>
              <Icon name="trash" size={14} />
            </button>
          </li>
        ))}
      </ul>
    </div>
  )
}

export function KeyboardHelp({ onClose }: { onClose: () => void }) {
  return (
    <div className="popover kbd-help" role="dialog" aria-label={t('kbd.title')}>
      <div className="row gap">
        <strong className="grow">{t('kbd.title')}</strong>
        <button className="icon-btn small" onClick={onClose} aria-label={t('act.close')}>
          <Icon name="x" size={14} />
        </button>
      </div>
      <p className="small">{t('kbd.help')}</p>
    </div>
  )
}
