import { useEffect, useRef, useState } from 'react'
import { ApiError, api } from '../api'
import type { Action } from '../actions'
import { t } from '../i18n'
import { openCompose, useStore } from '../store'
import type { Folder, MessageDetail } from '../types'
import { accountColor, accountLabel, displayName, folderLabel, formatAddress, formatSize, fullDate, shortDate } from '../util'
import { Modal } from './Dialogs'
import { Icon } from './Icons'

interface Props {
  messageId: number
  onAct: (action: Action, ids: number[], folderId?: number) => Promise<void>
  onClose: () => void
  onOpenMessage: (id: number) => void
}

export function Reader({ messageId, onAct, onClose, onOpenMessage }: Props) {
  const settings = useStore((s) => s.settings)
  const accounts = useStore((s) => s.accounts)
  const folders = useStore((s) => s.folders)
  const [detail, setDetail] = useState<MessageDetail | null>(null)
  const [error, setError] = useState('')
  const [remote, setRemote] = useState(settings.remoteImages === 'always')
  const [plain, setPlain] = useState(false)
  const [headers, setHeaders] = useState<string | null>(null)
  const [moveOpen, setMoveOpen] = useState(false)

  useEffect(() => {
    const ctl = new AbortController()
    setDetail(null)
    setError('')
    setRemote(settings.remoteImages === 'always')
    setPlain(false)
    api
      .get<MessageDetail>(`/messages/${messageId}`, ctl.signal)
      .then((d) => {
        setDetail(d)
        if (!d.message.seen && settings.markReadOnOpen === 'true') void onAct('read', [d.message.id])
      })
      .catch((e) => {
        if (ctl.signal.aborted) return
        setError(e instanceof ApiError && e.status === 410 ? t('read.gone') : e instanceof ApiError ? e.message : t('err.network'))
      })
    return () => ctl.abort()
  }, [messageId])

  if (error) {
    return (
      <div className="reader empty">
        <p className="notice error">{error}</p>
      </div>
    )
  }
  if (!detail) {
    return (
      <div className="reader empty">
        <p className="muted">{t('read.loading')}</p>
      </div>
    )
  }
  const m = detail.message
  const acct = accounts.find((a) => a.id === m.accountId)
  const role = detail.folder?.role
  const isDraft = m.draft || role === 'drafts'
  const accountFolders = folders.filter((f) => f.accountId === m.accountId && f.selectable && f.id !== m.folderId)
  const visibleAtts = detail.attachments.filter((a) => !a.inline)

  return (
    <article className="reader" aria-label={m.subject}>
      <div className="reader-toolbar">
        <button className="icon-btn mobile-only" onClick={onClose} aria-label={t('act.back')}>
          <Icon name="back" />
        </button>
        {isDraft ? (
          <button className="btn primary small" onClick={() => openCompose({ mode: 'new', messageId: m.id })}>
            <Icon name="edit" size={15} /> {t('act.editDraft')}
          </button>
        ) : (
          <>
            <button className="icon-btn" title={t('act.reply') + ' (r)'} onClick={() => openCompose({ mode: 'reply', messageId: m.id })}>
              <Icon name="reply" />
            </button>
            <button className="icon-btn" title={t('act.replyAll') + ' (a)'} onClick={() => openCompose({ mode: 'replyall', messageId: m.id })}>
              <Icon name="replyAll" />
            </button>
            <button className="icon-btn" title={t('act.forward') + ' (f)'} onClick={() => openCompose({ mode: 'forward', messageId: m.id })}>
              <Icon name="forward" />
            </button>
          </>
        )}
        <span className="sep" />
        {role !== 'archive' && role !== 'all' && (
          <button className="icon-btn" title={t('act.archive') + ' (e)'} onClick={() => onAct('archive', [m.id])}>
            <Icon name="archive" />
          </button>
        )}
        <button className="icon-btn" title={(role === 'trash' ? t('act.deleteForever') : t('act.trash')) + ' (#)'} onClick={() => onAct('trash', [m.id])}>
          <Icon name="trash" />
        </button>
        <button className="icon-btn" title={t('act.markUnread') + ' (m)'} onClick={() => onAct('unread', [m.id])}>
          <Icon name="mail" />
        </button>
        <button className={'icon-btn star' + (m.flagged ? ' on' : '')} title={(m.flagged ? t('act.unstar') : t('act.star')) + ' (s)'} onClick={() => {
          setDetail({ ...detail, message: { ...m, flagged: !m.flagged } })
          void onAct(m.flagged ? 'unstar' : 'star', [m.id])
        }}>
          <Icon name="star" filled={m.flagged} />
        </button>
        <div className="menu-wrap">
          <button className="icon-btn" title={t('act.move')} onClick={() => setMoveOpen((v) => !v)} aria-expanded={moveOpen}>
            <Icon name="folder" />
          </button>
          {moveOpen && (
            <div className="menu" role="menu" onMouseLeave={() => setMoveOpen(false)}>
              {accountFolders.map((f: Folder) => (
                <button key={f.id} role="menuitem" onClick={() => { setMoveOpen(false); void onAct('move', [m.id], f.id) }}>
                  {folderLabel(f)}
                </button>
              ))}
            </div>
          )}
        </div>
        <button className="icon-btn" title={role === 'junk' ? t('act.notSpam') : t('act.spam')} onClick={() => onAct(role === 'junk' ? 'notspam' : 'spam', [m.id])}>
          <Icon name="spam" />
        </button>
        <span className="grow" />
        <button className="icon-btn" title={t('act.details')} onClick={() => api.get<{ headers: string }>(`/messages/${m.id}/headers`).then((r) => setHeaders(r.headers)).catch(() => setHeaders(''))}>
          <Icon name="code" />
        </button>
        <a className="icon-btn" title={t('act.exportEml')} href={`/api/v1/messages/${m.id}/raw`} download>
          <Icon name="download" />
        </a>
      </div>

      <div className="reader-scroll">
        <h1 className="reader-subject">
          {m.subject || t('list.noSubject')}
          {m.priority > 0 && <span className="tag warn">{t('read.priorityHigh')}</span>}
        </h1>
        <div className="reader-meta">
          <div className="avatar" style={{ background: accountColor(acct) }} aria-hidden="true">
            {(displayName(m.from)[0] || '?').toUpperCase()}
          </div>
          <div className="meta-lines">
            <div>
              <strong>{displayName(m.from)}</strong> <span className="muted small">&lt;{m.from.addr}&gt;</span>
            </div>
            <div className="muted small">
              {t('read.to')} : {m.to.map(formatAddress).join(', ') || '—'}
              {m.cc.length > 0 && <> · {t('read.cc')} : {m.cc.map(formatAddress).join(', ')}</>}
            </div>
            <div className="muted small" title={fullDate(m.date)}>
              {fullDate(m.date)} · <span className="dot inline" style={{ background: accountColor(acct) }} /> {accountLabel(acct)}
              {detail.folder && <> · {folderLabel(detail.folder)}</>}
            </div>
          </div>
        </div>

        {detail.hasHtml && detail.remoteContent > 0 && !remote && (
          <div className="notice info row gap">
            <Icon name="shield" />
            <span className="grow">{t('read.remoteBlocked', { n: detail.remoteContent })}</span>
            <button className="btn small" onClick={() => setRemote(true)}>
              {t('read.showRemote')}
            </button>
          </div>
        )}
        {detail.hasHtml && (
          <div className="row end">
            <button className="link small" onClick={() => setPlain((p) => !p)}>
              {plain ? t('read.html') : t('read.plain')}
            </button>
          </div>
        )}

        {detail.hasHtml && !plain ? (
          <MessageFrame src={`/api/v1/messages/${m.id}/body.html${remote ? '?remote=1' : ''}`} />
        ) : (
          <pre className="plain-body">{detail.text}</pre>
        )}

        {visibleAtts.length > 0 && (
          <div className="attachments">
            <div className="muted small">{t('read.attachments', { n: visibleAtts.length })}</div>
            <div className="att-grid">
              {visibleAtts.map((a) => (
                <a key={a.id} className="att" href={`/api/v1/messages/${m.id}/attachments/${a.id}`} download={a.filename} title={t('act.download')}>
                  <Icon name={a.contentType.startsWith('image/') ? 'image' : 'paperclip'} />
                  <span className="att-name">{a.filename}</span>
                  <span className="muted small">{formatSize(a.size)}</span>
                </a>
              ))}
            </div>
          </div>
        )}

        {detail.thread.length > 1 && (
          <div className="thread">
            <div className="muted small">{t('read.thread', { n: detail.thread.length })}</div>
            {detail.thread.map((x) => (
              <button key={x.id} className={'thread-item' + (x.id === m.id ? ' current' : '')} onClick={() => x.id !== m.id && onOpenMessage(x.id)}>
                <span className="row-from">{displayName(x.from)}</span>
                <span className="grow ellipsis muted">{x.snippet}</span>
                <span className="muted small">{shortDate(x.date)}</span>
              </button>
            ))}
          </div>
        )}
      </div>

      {headers !== null && (
        <Modal title={t('read.headers')} onClose={() => setHeaders(null)} wide>
          <pre className="headers">{headers}</pre>
        </Modal>
      )}
    </article>
  )
}

/**
 * Renders the sanitised HTML body in a sandboxed iframe without scripts.
 * The frame is same-origin (no allow-scripts) so the parent can size it
 * and intercept mailto: links.
 */
function MessageFrame({ src }: { src: string }) {
  const ref = useRef<HTMLIFrameElement>(null)
  const [height, setHeight] = useState(200)
  useEffect(() => {
    const frame = ref.current
    if (!frame) return
    let ro: ResizeObserver | undefined
    const onLoad = () => {
      const doc = frame.contentDocument
      if (!doc) return
      const fit = () => setHeight(Math.max(80, doc.documentElement.scrollHeight + 4))
      fit()
      ro = new ResizeObserver(fit)
      ro.observe(doc.body)
      doc.addEventListener('click', (e) => {
        const a = (e.target as Element)?.closest?.('a')
        const href = a?.getAttribute('href') || ''
        if (href.toLowerCase().startsWith('mailto:')) {
          e.preventDefault()
          openCompose({ mode: 'new', to: decodeURIComponent(href.slice(7).split('?')[0]) })
        }
      })
    }
    frame.addEventListener('load', onLoad)
    return () => {
      frame.removeEventListener('load', onLoad)
      ro?.disconnect()
    }
  }, [src])
  return (
    <iframe
      ref={ref}
      className="mail-frame"
      src={src}
      title="message"
      sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"
      referrerPolicy="no-referrer"
      style={{ height }}
    />
  )
}
