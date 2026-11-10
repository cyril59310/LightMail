// Address book: contacts saved by the signed-in user (private to that user).
import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { ApiError, api } from '../api'
import { t } from '../i18n'
import { openCompose, toast } from '../store'
import type { Address, SavedContact } from '../types'
import { confirmDialog, Modal } from './Dialogs'
import { Icon } from './Icons'

export function contactErrorText(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.code === 'contact_exists') return t('contacts.exists')
    if (e.code === 'invalid_email') return t('contacts.invalidEmail')
    if (e.code === 'contact_limit') return t('contacts.limit')
    return e.message
  }
  return t('err.network')
}

export function loadSavedContacts(q = ''): Promise<SavedContact[]> {
  return api.get<{ items: SavedContact[] }>('/address-book' + (q ? '?q=' + encodeURIComponent(q) : '')).then((r) => r.items)
}

/** Adds an address to the address book, from a message for instance. */
export async function saveToContacts(a: Address) {
  try {
    await api.post('/address-book', { name: a.name ?? '', email: a.addr, notes: '' })
    toast(t('contacts.added', { who: a.name || a.addr }), 'success')
  } catch (e) {
    toast(contactErrorText(e), e instanceof ApiError && e.code === 'contact_exists' ? 'info' : 'error')
  }
}

function matches(c: SavedContact, q: string) {
  const s = q.trim().toLowerCase()
  return !s || c.name.toLowerCase().includes(s) || c.email.toLowerCase().includes(s) || c.notes.toLowerCase().includes(s)
}

export function ContactsPage() {
  const [items, setItems] = useState<SavedContact[] | null>(null)
  const [error, setError] = useState('')
  const [q, setQ] = useState('')
  // null: no form; 0: new contact; id: contact being edited
  const [editing, setEditing] = useState<number | null>(null)

  const load = () =>
    loadSavedContacts()
      .then((l) => {
        setItems(l)
        setError('')
      })
      .catch((e) => setError(contactErrorText(e)))
  useEffect(() => void load(), [])

  const shown = useMemo(() => (items ?? []).filter((c) => matches(c, q)), [items, q])

  const remove = async (c: SavedContact) => {
    if (!(await confirmDialog(t('contacts.deleteConfirm', { who: c.name || c.email }), { danger: true, confirmLabel: t('act.delete') }))) return
    try {
      await api.del(`/address-book/${c.id}`)
      setItems((l) => (l ?? []).filter((x) => x.id !== c.id))
    } catch (e) {
      toast(contactErrorText(e), 'error')
    }
  }

  return (
    <div className="page">
      <div className="row gap page-head">
        <h1 className="grow">{t('contacts.title')}</h1>
        <button className="btn primary" onClick={() => setEditing(0)} disabled={editing === 0}>
          <Icon name="plus" size={16} /> {t('contacts.new')}
        </button>
      </div>
      <p className="muted small">{t('contacts.hint')}</p>
      {editing === 0 && (
        <ContactForm
          onCancel={() => setEditing(null)}
          onSaved={(c) => {
            setItems((l) => [...(l ?? []), c])
            setEditing(null)
          }}
        />
      )}
      {error && <p className="notice error small">{error}</p>}
      {items && items.length > 0 && (
        <input className="contacts-search" type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('contacts.search')} aria-label={t('contacts.search')} />
      )}
      {items === null && !error && <p className="muted">{t('app.loading')}</p>}
      {items && items.length === 0 && editing !== 0 && <p className="muted">{t('contacts.empty')}</p>}
      {items && items.length > 0 && shown.length === 0 && <p className="muted">{t('contacts.noMatch')}</p>}
      <ul className="contact-list">
        {shown.map((c) =>
          editing === c.id ? (
            <li key={c.id}>
              <ContactForm
                initial={c}
                onCancel={() => setEditing(null)}
                onSaved={(saved) => {
                  setItems((l) => (l ?? []).map((x) => (x.id === saved.id ? saved : x)))
                  setEditing(null)
                }}
              />
            </li>
          ) : (
            <li key={c.id} className="card contact-item">
              <div className="avatar" aria-hidden="true">
                {(c.name || c.email)[0].toUpperCase()}
              </div>
              <div className="grow contact-main">
                <strong className="ellipsis">{c.name || c.email}</strong>
                {c.name && <span className="muted small ellipsis">{c.email}</span>}
                {c.notes && <span className="small contact-notes">{c.notes}</span>}
              </div>
              <div className="row gap contact-actions">
                <button className="icon-btn" title={t('contacts.write')} onClick={() => openCompose({ mode: 'new', to: c.email, toName: c.name || undefined })}>
                  <Icon name="edit" />
                </button>
                <button className="icon-btn" title={t('contacts.edit')} onClick={() => setEditing(c.id)}>
                  <Icon name="settings" />
                </button>
                <button className="icon-btn" title={t('act.delete')} onClick={() => void remove(c)}>
                  <Icon name="trash" />
                </button>
              </div>
            </li>
          ),
        )}
      </ul>
    </div>
  )
}

function ContactForm({ initial, onSaved, onCancel }: { initial?: SavedContact; onSaved: (c: SavedContact) => void; onCancel: () => void }) {
  const [name, setName] = useState(initial?.name ?? '')
  const [email, setEmail] = useState(initial?.email ?? '')
  const [notes, setNotes] = useState(initial?.notes ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      const body = { name: name.trim(), email: email.trim(), notes }
      const saved = initial ? await api.put<SavedContact>(`/address-book/${initial.id}`, body) : await api.post<SavedContact>('/address-book', body)
      onSaved(saved)
    } catch (err) {
      setError(contactErrorText(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <form className="card form" onSubmit={(e) => void submit(e)}>
      <h3>{initial ? t('contacts.edit') : t('contacts.new')}</h3>
      <div className="grid2">
        <label>
          {t('contacts.name')}
          <input value={name} onChange={(e) => setName(e.target.value)} maxLength={200} autoComplete="off" autoFocus={!initial} />
        </label>
        <label>
          {t('contacts.email')}
          <input type="email" value={email} onChange={(e) => setEmail(e.target.value)} required maxLength={320} autoComplete="off" spellCheck={false} />
        </label>
      </div>
      <label>
        {t('contacts.notes')}
        <textarea value={notes} onChange={(e) => setNotes(e.target.value)} maxLength={2000} rows={2} />
      </label>
      {error && <p className="notice error small">{error}</p>}
      <div className="row end gap">
        <button type="button" className="btn" onClick={onCancel}>
          {t('act.cancel')}
        </button>
        <button className="btn primary" disabled={busy}>
          {t('act.save')}
        </button>
      </div>
    </form>
  )
}

/** Dialog to choose recipients from the address book. */
export function ContactPicker({ exclude, onPick, onClose }: { exclude: Address[]; onPick: (list: Address[]) => void; onClose: () => void }) {
  const [items, setItems] = useState<SavedContact[] | null>(null)
  const [error, setError] = useState('')
  const [q, setQ] = useState('')
  const [picked, setPicked] = useState<Set<number>>(new Set())
  const search = useRef<HTMLInputElement>(null)
  useEffect(() => {
    loadSavedContacts()
      .then(setItems)
      .catch((e) => setError(contactErrorText(e)))
    search.current?.focus()
  }, [])
  const already = useMemo(() => new Set(exclude.map((a) => a.addr.toLowerCase())), [exclude])
  const shown = useMemo(() => (items ?? []).filter((c) => matches(c, q)), [items, q])
  const toggle = (id: number) =>
    setPicked((s) => {
      const n = new Set(s)
      if (n.has(id)) n.delete(id)
      else n.add(id)
      return n
    })
  const confirm = () => {
    const list = (items ?? []).filter((c) => picked.has(c.id)).map((c) => ({ name: c.name || undefined, addr: c.email }))
    onPick(list)
    onClose()
  }
  return (
    <Modal title={t('contacts.pickTitle')} onClose={onClose}>
      <input ref={search} className="contacts-search" type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('contacts.search')} aria-label={t('contacts.search')} />
      {error && <p className="notice error small">{error}</p>}
      {items === null && !error && <p className="muted">{t('app.loading')}</p>}
      {items && items.length === 0 && (
        <p className="muted">
          {t('contacts.empty')}{' '}
          <a href="#/contacts" onClick={onClose}>
            {t('contacts.manage')}
          </a>
        </p>
      )}
      <ul className="contact-pick-list">
        {shown.map((c) => {
          const has = already.has(c.email.toLowerCase())
          return (
            <li key={c.id}>
              <label className={'contact-pick' + (has ? ' disabled' : '')}>
                <input type="checkbox" checked={has || picked.has(c.id)} disabled={has} onChange={() => toggle(c.id)} />
                <span className="grow contact-main">
                  <span className="ellipsis">{c.name || c.email}</span>
                  {c.name && <span className="muted small ellipsis">{c.email}</span>}
                </span>
              </label>
            </li>
          )
        })}
      </ul>
      <div className="row end gap">
        <button className="btn" onClick={onClose}>
          {t('act.cancel')}
        </button>
        <button className="btn primary" disabled={picked.size === 0} onClick={confirm}>
          {t('contacts.addSelected', { n: picked.size })}
        </button>
      </div>
    </Modal>
  )
}
