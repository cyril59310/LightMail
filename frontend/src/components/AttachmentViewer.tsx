// Full-screen preview of a message's attachments (images, PDF, audio, video, text),
// without downloading them. Left/right arrows switch between attachments.
import { useEffect, useState, type MouseEvent } from 'react'
import { t } from '../i18n'
import type { Attachment } from '../types'
import { formatSize, previewKind } from '../util'
import { Icon } from './Icons'

// Text above this size is not previewed: the browser would struggle to lay it out.
const maxTextBytes = 2 << 20

export function attachmentURL(messageId: number, att: Attachment, inline = false) {
  return `/api/v1/messages/${messageId}/attachments/${att.id}${inline ? '?inline=1' : ''}`
}

export function AttachmentViewer({ messageId, attachments, start, onClose }: { messageId: number; attachments: Attachment[]; start: number; onClose: () => void }) {
  const [index, setIndex] = useState(start)
  const att = attachments[index]
  const count = attachments.length
  const go = (step: number) => setIndex((i) => (i + step + count) % count)

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
      else if (e.key === 'ArrowLeft' && count > 1) go(-1)
      else if (e.key === 'ArrowRight' && count > 1) go(1)
      else return
      e.preventDefault()
      e.stopPropagation()
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  })

  if (!att) return null
  const kind = previewKind(att.contentType, att.filename)
  const src = attachmentURL(messageId, att, true)
  const closeOnBackdrop = (e: MouseEvent) => e.target === e.currentTarget && onClose()

  return (
    // .modal-backdrop also pauses the mail keyboard shortcuts while the preview is open.
    <div className="modal-backdrop viewer" role="dialog" aria-modal="true" aria-label={att.filename}>
      <div className="viewer-bar">
        <Icon name={kind === 'image' ? 'image' : 'file'} />
        <span className="viewer-name" title={att.filename}>
          {att.filename}
        </span>
        <span className="viewer-meta">
          {formatSize(att.size)}
          {count > 1 && ` · ${index + 1} / ${count}`}
        </span>
        {count > 1 && (
          <>
            <button className="icon-btn" onClick={() => go(-1)} title={t('att.prev')} aria-label={t('att.prev')}>
              <Icon name="back" />
            </button>
            <button className="icon-btn flip" onClick={() => go(1)} title={t('att.next')} aria-label={t('att.next')}>
              <Icon name="back" />
            </button>
          </>
        )}
        <a className="icon-btn" href={attachmentURL(messageId, att)} download={att.filename} title={t('act.download')} aria-label={t('act.download')}>
          <Icon name="download" />
        </a>
        <button className="icon-btn" onClick={onClose} title={t('act.close')} aria-label={t('act.close')}>
          <Icon name="x" />
        </button>
      </div>
      <div className="viewer-stage" onMouseDown={closeOnBackdrop}>
        {/* key: reset the media element and its error state when switching attachment */}
        <Preview key={att.id} kind={kind} src={src} att={att} messageId={messageId} />
      </div>
    </div>
  )
}

function Preview({ kind, src, att, messageId }: { kind: ReturnType<typeof previewKind>; src: string; att: Attachment; messageId: number }) {
  const [failed, setFailed] = useState(false)
  if (failed || !kind) return <Unavailable message={failed ? t('att.loadError') : t('att.noPreview')} href={attachmentURL(messageId, att)} filename={att.filename} />
  switch (kind) {
    case 'image':
      return <img className="viewer-image" src={src} alt={att.filename} onError={() => setFailed(true)} />
    case 'pdf':
      return <iframe className="viewer-frame" src={src} title={att.filename} />
    case 'audio':
      return <audio className="viewer-audio" src={src} controls onError={() => setFailed(true)} />
    case 'video':
      return <video className="viewer-video" src={src} controls onError={() => setFailed(true)} />
    case 'text':
      return <TextPreview att={att} messageId={messageId} />
  }
}

function TextPreview({ att, messageId }: { att: Attachment; messageId: number }) {
  const tooLarge = att.size > maxTextBytes
  const [text, setText] = useState<string | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    if (tooLarge) return
    const ctrl = new AbortController()
    fetch(attachmentURL(messageId, att), { signal: ctrl.signal, credentials: 'same-origin' })
      .then((r) => (r.ok ? r.arrayBuffer() : Promise.reject(new Error(String(r.status)))))
      .then((buf) => setText(decodeText(buf)))
      .catch(() => !ctrl.signal.aborted && setFailed(true))
    return () => ctrl.abort()
  }, [messageId, att, tooLarge])

  const href = attachmentURL(messageId, att)
  if (tooLarge) return <Unavailable message={t('att.tooLarge')} href={href} filename={att.filename} />
  if (failed) return <Unavailable message={t('att.loadError')} href={href} filename={att.filename} />
  if (text === null) return <p className="viewer-note">{t('app.loading')}</p>
  return <pre className="viewer-text">{text}</pre>
}

/** UTF-8 when valid, otherwise Windows-1252 (common for files from older mail clients). */
function decodeText(buf: ArrayBuffer): string {
  try {
    return new TextDecoder('utf-8', { fatal: true }).decode(buf)
  } catch {
    return new TextDecoder('windows-1252').decode(buf)
  }
}

function Unavailable({ message, href, filename }: { message: string; href: string; filename: string }) {
  return (
    <div className="viewer-note">
      <p>{message}</p>
      <a className="btn primary" href={href} download={filename}>
        <Icon name="download" /> {t('act.download')}
      </a>
    </div>
  )
}
