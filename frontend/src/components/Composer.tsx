import { useCallback, useEffect, useRef, useState } from 'react'
import { ApiError, api } from '../api'
import { t } from '../i18n'
import { getState, loadOutbox, openCompose, setState, toast, useStore, type ComposeRequest } from '../store'
import type { Account, Address, Draft, MessageDetail, OutboxItem, Upload } from '../types'
import { accountLayout, cleanFragment, displayName, escapeHTML, formatAddress, formatSize, fullDate, htmlToText, mentionsAttachment, orderedAccounts, textToHTML } from '../util'
import { confirmDialog } from './Dialogs'
import { AddressField, RichEditor } from './Editor'
import { Icon } from './Icons'

const SIG_START = '<div class="lm-signature">'

function signatureBlock(a?: Account) {
  return a?.signatureHtml ? `${SIG_START}<br>-- <br>${a.signatureHtml}</div>` : ''
}

function replaceSignature(html: string, a?: Account) {
  const doc = new DOMParser().parseFromString(`<div id="root">${html}</div>`, 'text/html')
  const root = doc.getElementById('root')!
  const old = root.querySelector('.lm-signature')
  const fresh = signatureBlock(a)
  if (old) {
    if (fresh) old.outerHTML = fresh
    else old.remove()
  } else if (fresh) {
    const quote = root.querySelector('.lm-quote')
    if (quote) quote.insertAdjacentHTML('beforebegin', fresh)
    else root.insertAdjacentHTML('beforeend', fresh)
  }
  return root.innerHTML
}

function ownAddresses(accounts: Account[]) {
  return new Set(accounts.map((a) => a.email.toLowerCase()))
}

interface State {
  accountId: number
  mode: ComposeRequest['mode']
  relatedId?: number
  to: Address[]
  cc: Address[]
  bcc: Address[]
  subject: string
  isHtml: boolean
  html: string
  text: string
  uploads: Upload[]
  keepOrig: boolean
  origAttachments: number
  receipt: boolean
}

export function Composer({ req, onClose }: { req: ComposeRequest; onClose: () => void }) {
  const accounts = useStore((s) => s.accounts)
  const settings = useStore((s) => s.settings)
  const limits = useStore((s) => s.me?.limits)
  const [st, setSt] = useState<State | null>(null)
  const [editorKey, setEditorKey] = useState(0)
  const [showCc, setShowCc] = useState(false)
  const [saveState, setSaveState] = useState<'idle' | 'dirty' | 'saving' | 'saved'>('idle')
  const [sending, setSending] = useState(false)
  const [uploading, setUploading] = useState<string[]>([])
  const [minimized, setMinimized] = useState(false)
  const [error, setError] = useState('')
  const fileInput = useRef<HTMLInputElement>(null)
  const draftRef = useRef<number | undefined>(req.draftId)

  // ---- initialisation ------------------------------------------------
  useEffect(() => {
    let cancelled = false
    const defaultAcct = accounts.find((a) => a.isDefault) ?? accounts[0]
    const base: State = {
      accountId: defaultAcct?.id ?? 0, mode: req.mode, to: [], cc: [], bcc: [], subject: '', isHtml: settings.defaultFormat !== 'text',
      html: '', text: '', uploads: [], keepOrig: false, origAttachments: 0, receipt: false,
    }
    const finish = (s: State) => {
      if (cancelled) return
      setSt(s)
      setShowCc(s.cc.length > 0 || s.bcc.length > 0)
      setEditorKey((k) => k + 1)
    }
    async function init() {
      try {
        if (req.draftId) {
          const r = await api.get<{ draft: Draft; uploads: Upload[] }>(`/drafts/${req.draftId}`)
          const d = r.draft
          return finish({ ...base, accountId: d.accountId || base.accountId, mode: d.mode, relatedId: d.relatedMessageId, to: d.to, cc: d.cc, bcc: d.bcc, subject: d.subject, isHtml: d.isHtml, html: d.bodyHtml, text: d.bodyText, uploads: r.uploads, keepOrig: d.keepOriginalAttachments })
        }
        if (req.messageId && req.mode === 'new') {
          // Edit a draft stored on the server (e.g. written in another client).
          const d = await api.post<Draft>(`/messages/${req.messageId}/edit-draft`)
          draftRef.current = d.id
          return finish({ ...base, accountId: d.accountId || base.accountId, to: d.to, cc: d.cc, bcc: d.bcc, subject: d.subject, isHtml: d.isHtml || !d.bodyText, html: d.bodyHtml, text: d.bodyText })
        }
        if (req.messageId) {
          const d = await api.get<MessageDetail>(`/messages/${req.messageId}?quote=1`)
          const m = d.message
          const acct = accounts.find((a) => a.id === m.accountId) ?? defaultAcct
          const own = ownAddresses(accounts)
          const sentByMe = own.has(m.from.addr.toLowerCase())
          let to: Address[] = []
          let cc: Address[] = []
          if (req.mode === 'reply' || req.mode === 'replyall') {
            to = sentByMe ? m.to : m.replyTo?.length ? m.replyTo : [m.from]
            if (req.mode === 'replyall') {
              const extra = [...(sentByMe ? [] : m.to), ...m.cc].filter((a) => !own.has(a.addr.toLowerCase()) && !to.some((x) => x.addr === a.addr))
              cc = extra
            }
          }
          const subjectPrefix = req.mode === 'forward' ? 'Fwd: ' : 'Re: '
          const hasPrefix = req.mode === 'forward' ? /^(fwd?|tr)\s*:/i.test(m.subject) : /^re\s*:/i.test(m.subject)
          const quoteBody = d.quoteHtml ? cleanFragment(d.quoteHtml) : textToHTML(d.text)
          const header =
            req.mode === 'forward'
              ? `<p>${t('read.forwarded')}<br>${t('read.from')} : ${escapeHTML(formatAddress(m.from))}<br>${t('read.date')} : ${escapeHTML(fullDate(m.date))}<br>${t('compose.subject')} : ${escapeHTML(m.subject)}<br>${t('read.to')} : ${escapeHTML(m.to.map(formatAddress).join(', '))}</p>`
              : `<p>${escapeHTML(t('read.wrote', { date: fullDate(m.date), who: displayName(m.from) }))}</p>`
          const quote = `<div class="lm-quote">${header}<blockquote>${quoteBody}</blockquote></div>`
          const html = `<p><br></p>${signatureBlock(acct)}${quote}`
          const plainQuote = (req.mode === 'forward' ? t('read.forwarded') + '\n' : t('read.wrote', { date: fullDate(m.date), who: displayName(m.from) }) + '\n') + d.text.split('\n').map((l) => '> ' + l).join('\n')
          const visibleAtts = d.attachments.filter((a) => !a.inline).length
          return finish({
            ...base, accountId: acct?.id ?? base.accountId, relatedId: m.id, to, cc,
            subject: hasPrefix ? m.subject : subjectPrefix + m.subject, html, text: '\n\n' + plainQuote,
            keepOrig: req.mode === 'forward' && visibleAtts > 0, origAttachments: req.mode === 'forward' ? visibleAtts : 0,
          })
        }
        const to = req.to ? [{ addr: req.to }] : []
        finish({ ...base, to, html: `<p><br></p>${signatureBlock(defaultAcct)}` })
      } catch (e) {
        if (!cancelled) setError(e instanceof ApiError ? e.message : t('err.network'))
        finish(base)
      }
    }
    void init()
    return () => {
      cancelled = true
    }
  }, [req.key])

  const update = (p: Partial<State>) => {
    setSt((s) => (s ? { ...s, ...p } : s))
    setSaveState('dirty')
  }

  // ---- drafts ----------------------------------------------------------
  const payload = useCallback(
    (s: State) => ({
      accountId: s.accountId, mode: s.mode, relatedMessageId: s.relatedId ?? 0, to: s.to, cc: s.cc, bcc: s.bcc, subject: s.subject,
      bodyHtml: s.isHtml ? s.html : '', bodyText: s.isHtml ? htmlToText(s.html) : s.text, isHtml: s.isHtml,
      uploads: s.uploads.map((u) => u.id), keepOriginalAttachments: s.keepOrig,
    }),
    [],
  )

  const saveDraft = useCallback(
    async (s: State) => {
      setSaveState('saving')
      try {
        if (draftRef.current) await api.put<Draft>(`/drafts/${draftRef.current}`, payload(s))
        else {
          const d = await api.post<Draft>('/drafts', payload(s))
          draftRef.current = d.id
        }
        setSaveState('saved')
        return true
      } catch {
        setSaveState('dirty')
        return false
      }
    },
    [payload],
  )

  useEffect(() => {
    if (!st || saveState !== 'dirty') return
    const h = setTimeout(() => void saveDraft(st), 2500)
    return () => clearTimeout(h)
  }, [st, saveState, saveDraft])

  useEffect(() => {
    const warn = (e: BeforeUnloadEvent) => {
      if (saveState === 'dirty' || saveState === 'saving') {
        e.preventDefault()
        e.returnValue = t('compose.unsaved')
      }
    }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [saveState])

  if (!st) {
    return (
      <div className="composer">
        <div className="composer-body muted pad">{error || t('app.loading')}</div>
      </div>
    )
  }
  const account = accounts.find((a) => a.id === st.accountId)

  // ---- attachments -------------------------------------------------------
  const addFiles = async (files: FileList | null) => {
    if (!files) return
    for (const f of Array.from(files)) {
      if (limits && f.size > limits.maxAttachmentBytes) {
        toast(t('compose.tooLarge', { name: f.name, max: formatSize(limits.maxAttachmentBytes) }), 'error')
        continue
      }
      setUploading((u) => [...u, f.name])
      try {
        const form = new FormData()
        form.append('file', f, f.name)
        const up = await api.upload<Upload>('/uploads', form)
        setSt((s) => (s ? { ...s, uploads: [...s.uploads, up] } : s))
        setSaveState('dirty')
      } catch (e) {
        toast(e instanceof ApiError ? e.message : t('err.network'), 'error')
      } finally {
        setUploading((u) => u.filter((n) => n !== f.name))
      }
    }
  }

  // ---- send ------------------------------------------------------------
  const send = async () => {
    if (!account) return
    const rcpts = st.to.length + st.cc.length + st.bcc.length
    if (!rcpts) {
      setError(t('compose.noRecipient'))
      return
    }
    setError('')
    const bodyText = st.isHtml ? htmlToText(st.html) : st.text
    if (!st.subject.trim() && settings.confirmSendNoSubject !== 'false' && !(await confirmDialog(t('compose.noSubject'), { confirmLabel: t('compose.send') }))) return
    const ownText = bodyText.split(/\n>|<blockquote/)[0]
    if (st.uploads.length === 0 && !st.keepOrig && mentionsAttachment(st.subject + ' ' + ownText) && !(await confirmDialog(t('compose.forgotAttachment'), { confirmLabel: t('compose.send') }))) return
    setSending(true)
    // Keep a draft so that "undo" can reopen the message.
    await saveDraft(st)
    try {
      const undo = Number(settings.undoSendSeconds ?? limits?.undoSendSeconds ?? 5)
      const item = await api.post<OutboxItem>('/messages/send', { ...payload(st), draftId: draftRef.current ?? 0, requestReceipt: st.receipt, undoSeconds: undo })
      setSaveState('saved')
      onClose()
      void loadOutbox().catch(() => {})
      const id = draftRef.current
      toast(t('compose.queued'), 'info', {
        id: 1_000_000 + item.id,
        ttl: Math.max(undo * 1000, 3000),
        action: undo > 0 ? { label: t('compose.undo'), run: () => void undoSend(item.id, id) } : undefined,
      })
    } catch (e) {
      setSending(false)
      setError(e instanceof ApiError ? e.message : t('err.network'))
    }
  }

  const close = async () => {
    if (saveState === 'dirty') await saveDraft(st)
    onClose()
  }

  const discard = async () => {
    if (!(await confirmDialog(t('compose.discard') + ' ?', { danger: true, confirmLabel: t('act.delete') }))) return
    if (draftRef.current) await api.del(`/drafts/${draftRef.current}`).catch(() => {})
    for (const u of st.uploads) void api.del(`/uploads/${u.id}`).catch(() => {})
    onClose()
  }

  const switchAccount = (id: number) => {
    const a = accounts.find((x) => x.id === id)
    if (st.isHtml) {
      update({ accountId: id, html: replaceSignature(st.html, a) })
      setEditorKey((k) => k + 1)
    } else update({ accountId: id })
  }

  const toggleFormat = () => {
    if (st.isHtml) update({ isHtml: false, text: htmlToText(st.html) })
    else {
      update({ isHtml: true, html: textToHTML(st.text) })
      setEditorKey((k) => k + 1)
    }
  }

  const title = st.subject || t('compose.title')
  return (
    <div className={'composer' + (minimized ? ' minimized' : '')} role="dialog" aria-label={title}
      onKeyDown={(e) => {
        if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
          e.preventDefault()
          void send()
        }
      }}>
      <div className="composer-head" onClick={() => minimized && setMinimized(false)}>
        <span className="ellipsis grow">{title}</span>
        <span className="muted small">{saveState === 'saving' ? t('compose.saving') : saveState === 'saved' ? t('compose.saved') : ''}</span>
        <button className="icon-btn small" onClick={(e) => { e.stopPropagation(); setMinimized((v) => !v) }} aria-label={t('compose.minimize')}>
          <Icon name="minimize" size={15} />
        </button>
        <button className="icon-btn small" onClick={(e) => { e.stopPropagation(); void close() }} aria-label={t('act.close')}>
          <Icon name="x" size={15} />
        </button>
      </div>
      {!minimized && (
        <div className="composer-body">
          {accounts.length > 1 && (
            <div className="field-row">
              <label className="field-label" htmlFor="cmp-from">{t('compose.from')}</label>
              <select id="cmp-from" value={st.accountId} onChange={(e) => switchAccount(Number(e.target.value))}>
                {orderedAccounts(accountLayout(settings.accountLayout, accounts), accounts).map((a) => (
                  <option key={a.id} value={a.id}>
                    {(a.senderName || a.displayName) && (a.senderName || a.displayName) !== a.email ? `${a.senderName || a.displayName} <${a.email}>` : a.email}
                  </option>
                ))}
              </select>
            </div>
          )}
          <AddressField label={t('compose.to')} value={st.to} onChange={(to) => update({ to })} autoFocus={st.mode === 'new' || st.mode === 'forward'} />
          {!showCc ? (
            <button className="link small cc-toggle" onClick={() => setShowCc(true)}>
              {t('compose.cc')} / {t('compose.bcc')}
            </button>
          ) : (
            <>
              <AddressField label={t('compose.cc')} value={st.cc} onChange={(cc) => update({ cc })} />
              <AddressField label={t('compose.bcc')} value={st.bcc} onChange={(bcc) => update({ bcc })} />
            </>
          )}
          <div className="field-row">
            <label className="field-label" htmlFor="cmp-subject">{t('compose.subject')}</label>
            <input id="cmp-subject" value={st.subject} onChange={(e) => update({ subject: e.target.value })} maxLength={900} />
          </div>
          {st.isHtml ? (
            <RichEditor key={editorKey} initialHTML={st.html} onChange={(html) => update({ html })} autoFocus={st.mode === 'reply' || st.mode === 'replyall'} />
          ) : (
            <textarea className="plain-editor" value={st.text} onChange={(e) => update({ text: e.target.value })} autoFocus={st.mode === 'reply' || st.mode === 'replyall'} />
          )}
          {(st.uploads.length > 0 || uploading.length > 0 || st.origAttachments > 0) && (
            <div className="att-list">
              {st.origAttachments > 0 && (
                <label className="check small">
                  <input type="checkbox" checked={st.keepOrig} onChange={(e) => update({ keepOrig: e.target.checked })} /> {t('compose.keepAttachments')} ({st.origAttachments})
                </label>
              )}
              {st.uploads.map((u) => (
                <span key={u.id} className="att-chip">
                  <Icon name="paperclip" size={13} /> {u.filename} <span className="muted">{formatSize(u.size)}</span>
                  <button aria-label={t('act.delete')} onClick={() => { update({ uploads: st.uploads.filter((x) => x.id !== u.id) }); void api.del(`/uploads/${u.id}`).catch(() => {}) }}>
                    ×
                  </button>
                </span>
              ))}
              {uploading.map((n) => (
                <span key={n} className="att-chip muted">{t('compose.uploading', { name: n })}</span>
              ))}
            </div>
          )}
          {error && <p className="notice error small" role="alert">{error}</p>}
          <div className="composer-foot">
            <button className="btn primary" onClick={() => void send()} disabled={sending || uploading.length > 0 || !account} title="Ctrl+Entrée">
              <Icon name="send" size={15} /> {sending ? t('compose.sending') : t('compose.send')}
            </button>
            <button className="icon-btn" onClick={() => fileInput.current?.click()} title={t('compose.attach')} aria-label={t('compose.attach')}>
              <Icon name="paperclip" />
            </button>
            <input ref={fileInput} type="file" multiple hidden onChange={(e) => { void addFiles(e.target.files); e.target.value = '' }} />
            <button className="link small" onClick={toggleFormat}>{st.isHtml ? t('compose.plain') : t('compose.rich')}</button>
            <label className="check small" title={t('compose.receipt')}>
              <input type="checkbox" checked={st.receipt} onChange={(e) => update({ receipt: e.target.checked })} /> {t('compose.receipt')}
            </label>
            <span className="grow" />
            <button className="icon-btn" onClick={() => void discard()} title={t('compose.discard')} aria-label={t('compose.discard')}>
              <Icon name="trash" />
            </button>
          </div>
        </div>
      )}
    </div>
  )

  async function undoSend(outboxId: number, id?: number) {
    try {
      await api.post(`/outbox/${outboxId}/cancel`)
      toast(t('compose.canceled'), 'info')
      void loadOutbox().catch(() => {})
      if (id) openCompose({ mode: 'new', draftId: id })
    } catch (e) {
      toast(e instanceof ApiError ? e.message : t('err.network'), 'error')
    }
  }
}

/** Mounts the active composer from the global store. */
export function ComposerHost() {
  const req = useStore((s) => s.compose)
  if (!req || getState().accounts.length === 0) return null
  return <Composer key={req.key} req={req} onClose={() => setState({ compose: null })} />
}
