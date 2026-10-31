import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { ApiError, api, qs } from '../api'
import { optimisticPatch, runAction, type Action } from '../actions'
import { t } from '../i18n'
import { openCompose, useStore } from '../store'
import type { Account, Message, Page } from '../types'
import { accountColor, displayName, shortDate } from '../util'
import { Icon } from './Icons'

export type ListSpec =
  | { kind: 'unified'; role: string }
  | { kind: 'starred' }
  | { kind: 'folder'; folderId: number }
  | { kind: 'search'; q: string; folderId?: number; server?: boolean }

function specURL(spec: ListSpec, limit: number, cursor: string, filters: { unread: boolean; att: boolean }) {
  const f = { unread: filters.unread ? 1 : undefined, attachments: filters.att ? 1 : undefined, limit, cursor }
  switch (spec.kind) {
    case 'unified':
      return '/messages' + qs({ view: 'unified', role: spec.role, ...f })
    case 'starred':
      return '/messages' + qs({ view: 'starred', ...f })
    case 'folder':
      return '/messages' + qs({ view: 'folder', folderId: spec.folderId, ...f })
    case 'search': {
      let q = spec.q
      if (filters.unread) q += ' is:unread'
      if (filters.att) q += ' has:attachment'
      return '/search' + qs({ q, folderId: spec.folderId, server: spec.server ? 1 : undefined, limit, cursor })
    }
  }
}

export interface ListHandle {
  items: Message[]
  selectNext: (dir: 1 | -1) => void
  act: (action: Action, ids?: number[], folderId?: number) => Promise<void>
}

interface Props {
  spec: ListSpec
  selectedId: number | null
  onSelect: (m: Message | null) => void
  handleRef: React.MutableRefObject<ListHandle | null>
  title: string
  showRecipients: boolean
}

export function MessageList({ spec, selectedId, onSelect, handleRef, title, showRecipients }: Props) {
  const settings = useStore((s) => s.settings)
  const listVersion = useStore((s) => s.listVersion)
  const accounts = useStore((s) => s.accounts)
  const pageSize = Number(settings.pageSize) || 50
  const compact = settings.density === 'compact'
  const rowH = compact ? 44 : 76
  const [items, setItems] = useState<Message[]>([])
  const [cursor, setCursor] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [warning, setWarning] = useState('')
  const [checked, setChecked] = useState<Set<number>>(new Set())
  const [filters, setFilters] = useState({ unread: false, att: false })
  const [scrollTop, setScrollTop] = useState(0)
  const [viewH, setViewH] = useState(600)
  const scroller = useRef<HTMLDivElement>(null)
  const specKey = JSON.stringify(spec)
  const acctMap = useMemo(() => new Map(accounts.map((a) => [a.id, a])), [accounts])
  const multiAccount = accounts.length > 1 && spec.kind !== 'folder'
  const loadSeq = useRef(0)

  const load = useCallback(
    async (reset: boolean, keepScroll = false) => {
      const seq = ++loadSeq.current
      setLoading(true)
      setError('')
      try {
        // On refresh, reload as many rows as currently shown (bounded).
        const limit = reset && keepScroll ? Math.min(200, Math.max(pageSize, items.length)) : pageSize
        const r = await api.get<Page<Message>>(specURL(spec, limit, reset ? '' : cursor, filters))
        if (seq !== loadSeq.current) return
        setItems((prev) => (reset ? r.items : [...prev, ...r.items.filter((m) => !prev.some((p) => p.id === m.id))]))
        setCursor(r.nextCursor ?? '')
        setWarning(r.warning ?? '')
        if (reset && !keepScroll) {
          scroller.current?.scrollTo(0, 0)
          setChecked(new Set())
        }
      } catch (e) {
        if (seq === loadSeq.current) setError(e instanceof ApiError ? e.message : t('err.network'))
      } finally {
        if (seq === loadSeq.current) setLoading(false)
      }
    },
    [specKey, cursor, pageSize, filters, items.length],
  )

  useEffect(() => void load(true), [specKey, pageSize, filters])
  // Live refresh on server events, keeping position.
  const firstVersion = useRef(listVersion)
  useEffect(() => {
    if (listVersion !== firstVersion.current) void load(true, true)
  }, [listVersion])

  useLayoutEffect(() => {
    const el = scroller.current
    if (!el) return
    const ro = new ResizeObserver(() => setViewH(el.clientHeight))
    ro.observe(el)
    setViewH(el.clientHeight)
    return () => ro.disconnect()
  }, [])

  // Infinite scroll
  useEffect(() => {
    if (!loading && cursor && scrollTop + viewH > items.length * rowH - rowH * 5) void load(false)
  }, [scrollTop, viewH, items.length, cursor, loading, rowH, load])

  const patch = (ids: number[], p: Partial<Message> | 'remove') => {
    setItems((prev) => (p === 'remove' ? prev.filter((m) => !ids.includes(m.id)) : prev.map((m) => (ids.includes(m.id) ? { ...m, ...p } : m))))
  }

  const act = async (action: Action, ids?: number[], folderId?: number) => {
    const targetIds = ids ?? [...checked]
    const targets = items.filter((m) => targetIds.includes(m.id))
    if (!targets.length) return
    const before = items
    const op = optimisticPatch(action)
    const removing = op === 'remove'
    let nextSel: Message | null = null
    if (removing && selectedId && targetIds.includes(selectedId)) {
      const idx = items.findIndex((m) => m.id === selectedId)
      nextSel = items.slice(idx + 1).find((m) => !targetIds.includes(m.id)) ?? items.slice(0, idx).reverse().find((m) => !targetIds.includes(m.id)) ?? null
    }
    if (op && action !== 'delete') patch(targetIds, op)
    const ok = await runAction(targets, action, folderId)
    if (ok.length < targets.length) {
      setItems(before) // revert on failure
      if (ok.length && op) patch(ok, op)
      return
    }
    if (action === 'delete') patch(ok, 'remove')
    if (removing) {
      setChecked(new Set())
      if (selectedId && targetIds.includes(selectedId)) onSelect(nextSel)
    }
  }

  const selectNext = (dir: 1 | -1) => {
    const idx = items.findIndex((m) => m.id === selectedId)
    const next = items[Math.max(0, Math.min(items.length - 1, idx < 0 ? 0 : idx + dir))]
    if (next) {
      onSelect(next)
      const top = items.indexOf(next) * rowH
      const el = scroller.current
      if (el && (top < el.scrollTop || top + rowH > el.scrollTop + el.clientHeight)) el.scrollTo({ top: top - el.clientHeight / 2 })
    }
  }

  handleRef.current = { items, selectNext, act }

  // Mark as read locally when opened (the reader performs the server call).
  useEffect(() => {
    if (selectedId && settings.markReadOnOpen === 'true') patch([selectedId], { seen: true })
  }, [selectedId])

  const overscan = 8
  const start = Math.max(0, Math.floor(scrollTop / rowH) - overscan)
  const end = Math.min(items.length, Math.ceil((scrollTop + viewH) / rowH) + overscan)
  const visible = items.slice(start, end)
  const toggle = (id: number, range: boolean) => {
    setChecked((prev) => {
      const n = new Set(prev)
      if (range && prev.size) {
        const last = [...prev].pop()!
        const a = items.findIndex((m) => m.id === last)
        const b = items.findIndex((m) => m.id === id)
        for (let i = Math.min(a, b); i <= Math.max(a, b); i++) n.add(items[i].id)
      } else if (n.has(id)) n.delete(id)
      else n.add(id)
      return n
    })
  }
  const allChecked = items.length > 0 && checked.size === items.length
  const checkedMsgs = items.filter((m) => checked.has(m.id))
  const anyUnread = checkedMsgs.some((m) => !m.seen)
  const anyUnstarred = checkedMsgs.some((m) => !m.flagged)

  return (
    <section className="list-pane" aria-label={title}>
      <header className="list-head">
        {checked.size > 0 ? (
          <div className="bulk-bar">
            <input type="checkbox" checked={allChecked} onChange={() => setChecked(allChecked ? new Set() : new Set(items.map((m) => m.id)))} aria-label={t('list.selectAll')} />
            <span className="muted">{t('list.selected', { n: checked.size })}</span>
            <button className="icon-btn" title={t('act.archive')} onClick={() => act('archive')}>
              <Icon name="archive" />
            </button>
            <button className="icon-btn" title={t('act.trash')} onClick={() => act('trash')}>
              <Icon name="trash" />
            </button>
            <button className="icon-btn" title={anyUnread ? t('act.markRead') : t('act.markUnread')} onClick={() => act(anyUnread ? 'read' : 'unread')}>
              <Icon name={anyUnread ? 'mailOpen' : 'mail'} />
            </button>
            <button className="icon-btn" title={anyUnstarred ? t('act.star') : t('act.unstar')} onClick={() => act(anyUnstarred ? 'star' : 'unstar')}>
              <Icon name="star" filled={!anyUnstarred} />
            </button>
            <button className="icon-btn" title={t('act.spam')} onClick={() => act('spam')}>
              <Icon name="spam" />
            </button>
          </div>
        ) : (
          <>
            <input type="checkbox" checked={false} onChange={() => setChecked(new Set(items.map((m) => m.id)))} aria-label={t('list.selectAll')} disabled={!items.length} />
            <h2 className="list-title" title={title}>
              {title}
            </h2>
            <button className={'chip' + (filters.unread ? ' on' : '')} onClick={() => setFilters((f) => ({ ...f, unread: !f.unread }))}>
              {t('list.unreadOnly')}
            </button>
            <button className={'chip' + (filters.att ? ' on' : '')} onClick={() => setFilters((f) => ({ ...f, att: !f.att }))} title={t('list.attachmentsOnly')}>
              <Icon name="paperclip" size={14} />
            </button>
            <button className="icon-btn" onClick={() => void load(true)} title="↻" aria-label="refresh">
              <Icon name="refresh" className={loading ? 'spin' : ''} />
            </button>
          </>
        )}
      </header>
      {warning && <p className="notice warn small">{warning}</p>}
      {error && (
        <p className="notice error small" role="alert">
          {error}
        </p>
      )}
      <div className="list-scroll" ref={scroller} onScroll={(e) => setScrollTop(e.currentTarget.scrollTop)} role="listbox" aria-multiselectable="true" tabIndex={-1}>
        {!loading && items.length === 0 && !error && (
          <div className="empty">
            <Icon name="inbox" size={40} />
            <p>{t('list.empty')}</p>
            <p className="muted small">{t('list.emptyHint')}</p>
          </div>
        )}
        <div style={{ height: items.length * rowH, position: 'relative' }}>
          {visible.map((m, i) => (
            <Row
              key={m.id}
              m={m}
              top={(start + i) * rowH}
              height={rowH}
              compact={compact}
              selected={m.id === selectedId}
              checked={checked.has(m.id)}
              account={multiAccount ? acctMap.get(m.accountId) : undefined}
              showRecipients={showRecipients}
              onOpen={onSelect}
              onToggle={toggle}
              onAct={act}
            />
          ))}
        </div>
        {loading && items.length > 0 && <div className="list-loading muted small">{t('list.loading')}</div>}
      </div>
    </section>
  )
}

interface RowProps {
  m: Message
  top: number
  height: number
  compact: boolean
  selected: boolean
  checked: boolean
  account?: Account
  showRecipients: boolean
  onOpen: (m: Message) => void
  onToggle: (id: number, range: boolean) => void
  onAct: (a: Action, ids: number[]) => void
}

const Row = memo(function Row({ m, top, height, compact, selected, checked, account, showRecipients, onOpen, onToggle, onAct }: RowProps) {
  const who = showRecipients ? t('list.to', { who: m.to.map(displayName).join(', ') || '—' }) : displayName(m.from) || '—'
  return (
    <div
      className={'row-msg' + (m.seen ? '' : ' unread') + (selected ? ' selected' : '') + (checked ? ' checked' : '') + (compact ? ' compact' : '')}
      style={{ top, height }}
      role="option"
      aria-selected={selected}
      onClick={(e) => {
        if (e.shiftKey || e.ctrlKey || e.metaKey) onToggle(m.id, e.shiftKey)
        else onOpen(m)
      }}
      onDoubleClick={() => m.draft && openCompose({ mode: 'new', messageId: m.id })}
    >
      <input
        type="checkbox"
        checked={checked}
        onClick={(e) => e.stopPropagation()}
        onChange={(e) => onToggle(m.id, (e.nativeEvent as MouseEvent).shiftKey)}
        aria-label={m.subject}
      />
      {account && <span className="acct-bar" style={{ background: accountColor(account) }} title={account.email} />}
      <div className="row-main">
        <div className="row-top">
          <span className="row-from">{who}</span>
          {m.priority > 0 && <Icon name="alert" size={13} className="warn" title={t('read.priorityHigh')} />}
          {m.hasAttachments && <Icon name="paperclip" size={13} className="muted" />}
          {m.answered && <Icon name="reply" size={13} className="muted" />}
          <span className="row-date">{shortDate(m.date)}</span>
        </div>
        <div className="row-subject">
          {m.subject || t('list.noSubject')}
          {compact && m.snippet && <span className="row-snippet inline"> — {m.snippet}</span>}
        </div>
        {!compact && <div className="row-snippet">{m.snippet}</div>}
      </div>
      <div className="row-actions">
        <button
          className={'icon-btn small star' + (m.flagged ? ' on' : '')}
          onClick={(e) => {
            e.stopPropagation()
            onAct(m.flagged ? 'unstar' : 'star', [m.id])
          }}
          aria-label={m.flagged ? t('act.unstar') : t('act.star')}
        >
          <Icon name="star" size={15} filled={m.flagged} />
        </button>
        <button
          className="icon-btn small hover-only"
          onClick={(e) => {
            e.stopPropagation()
            onAct('archive', [m.id])
          }}
          aria-label={t('act.archive')}
        >
          <Icon name="archive" size={15} />
        </button>
        <button
          className="icon-btn small hover-only"
          onClick={(e) => {
            e.stopPropagation()
            onAct('trash', [m.id])
          }}
          aria-label={t('act.trash')}
        >
          <Icon name="trash" size={15} />
        </button>
      </div>
    </div>
  )
})
