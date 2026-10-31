import { useEffect, useRef, useState, type KeyboardEvent } from 'react'
import { api, qs } from '../api'
import { t } from '../i18n'
import type { Address } from '../types'
import { formatAddress, parseAddresses } from '../util'
import { Icon } from './Icons'

/** Minimal rich-text editor based on contentEditable. */
export function RichEditor({ initialHTML, onChange, autoFocus }: { initialHTML: string; onChange: (html: string) => void; autoFocus?: boolean }) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (ref.current) {
      ref.current.innerHTML = initialHTML
      if (autoFocus) {
        ref.current.focus()
        const sel = window.getSelection()
        sel?.collapse(ref.current, 0)
      }
    }
    // initialHTML is only applied on mount / when the editor key changes
  }, [])
  const exec = (cmd: string, value?: string) => {
    ref.current?.focus()
    document.execCommand(cmd, false, value)
    if (ref.current) onChange(ref.current.innerHTML)
  }
  const tools: [string, string, () => void][] = [
    ['bold', t('compose.bold'), () => exec('bold')],
    ['italic', t('compose.italic'), () => exec('italic')],
    ['underline', t('compose.underline'), () => exec('underline')],
    ['list', t('compose.list'), () => exec('insertUnorderedList')],
    ['olist', t('compose.olist'), () => exec('insertOrderedList')],
    ['quote', t('compose.quote'), () => exec('formatBlock', 'blockquote')],
    [
      'link',
      t('compose.link'),
      () => {
        const url = window.prompt(t('compose.linkPrompt'), 'https://')
        if (url && /^(https?:|mailto:)/i.test(url.trim())) exec('createLink', url.trim())
      },
    ],
    ['eraser', t('compose.clear'), () => exec('removeFormat')],
  ]
  return (
    <div className="rich-editor">
      <div className="editor-toolbar" role="toolbar">
        {tools.map(([icon, label, run]) => (
          <button key={icon} type="button" className="icon-btn small" title={label} aria-label={label} onMouseDown={(e) => e.preventDefault()} onClick={run}>
            <Icon name={icon} size={15} />
          </button>
        ))}
      </div>
      <div
        ref={ref}
        className="editor-area"
        contentEditable
        role="textbox"
        aria-multiline="true"
        onInput={(e) => onChange(e.currentTarget.innerHTML)}
        onPaste={(e) => {
          // Paste files/images is not supported inline; plain text keeps
          // foreign styles out of the message.
          if (e.clipboardData.files.length) e.preventDefault()
        }}
        suppressContentEditableWarning
      />
    </div>
  )
}

/** Recipient input with chips and autocompletion from known contacts. */
export function AddressField({ label, value, onChange, autoFocus }: { label: string; value: Address[]; onChange: (v: Address[]) => void; autoFocus?: boolean }) {
  const [text, setText] = useState('')
  const [invalid, setInvalid] = useState('')
  const [suggestions, setSuggestions] = useState<{ name?: string; addr: string }[]>([])
  const [active, setActive] = useState(0)
  const seq = useRef(0)

  useEffect(() => {
    const q = text.trim()
    if (q.length < 2 || q.includes(',')) {
      setSuggestions([])
      return
    }
    const s = ++seq.current
    const h = setTimeout(() => {
      api
        .get<{ items: { name?: string; addr: string }[] }>('/contacts' + qs({ q }))
        .then((r) => {
          if (s === seq.current) {
            setSuggestions(r.items.filter((c) => !value.some((v) => v.addr.toLowerCase() === c.addr.toLowerCase())))
            setActive(0)
          }
        })
        .catch(() => {})
    }, 150)
    return () => clearTimeout(h)
  }, [text, value])

  const commit = (raw = text) => {
    const { valid, invalid: bad } = parseAddresses(raw)
    const merged = [...value]
    for (const a of valid) if (!merged.some((x) => x.addr.toLowerCase() === a.addr.toLowerCase())) merged.push(a)
    if (valid.length) onChange(merged)
    setText(bad.join(', '))
    setInvalid(bad.length ? t('compose.invalidAddress', { addr: bad.join(', ') }) : '')
    setSuggestions([])
  }
  const pick = (c: { name?: string; addr: string }) => {
    onChange([...value, { name: c.name || undefined, addr: c.addr }])
    setText('')
    setSuggestions([])
  }
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (suggestions.length && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
      e.preventDefault()
      setActive((i) => (i + (e.key === 'ArrowDown' ? 1 : -1) + suggestions.length) % suggestions.length)
    } else if ((e.key === 'Enter' || e.key === 'Tab') && suggestions.length && text.trim()) {
      e.preventDefault()
      pick(suggestions[active])
    } else if (e.key === 'Enter' || e.key === ',' || e.key === ';') {
      if (text.trim()) {
        e.preventDefault()
        commit()
      }
    } else if (e.key === 'Backspace' && !text && value.length) {
      onChange(value.slice(0, -1))
    }
  }
  return (
    <div className="field-row address-field">
      <label className="field-label">{label}</label>
      <div className="chips">
        {value.map((a, i) => (
          <span key={a.addr + i} className="addr-chip" title={a.addr}>
            {a.name || a.addr}
            <button type="button" aria-label={t('act.delete')} onClick={() => onChange(value.filter((_, j) => j !== i))}>
              ×
            </button>
          </span>
        ))}
        <input
          value={text}
          autoFocus={autoFocus}
          onChange={(e) => {
            setText(e.target.value)
            setInvalid('')
          }}
          onKeyDown={onKey}
          onBlur={() => setTimeout(() => text.trim() && commit(), 150)}
          onPaste={(e) => {
            const p = e.clipboardData.getData('text')
            if (p.includes(',') || p.includes(';') || p.includes('\n')) {
              e.preventDefault()
              commit(text + p)
            }
          }}
          placeholder={value.length ? '' : t('compose.recipientsPlaceholder')}
          aria-label={label}
          aria-invalid={!!invalid}
        />
        {suggestions.length > 0 && (
          <div className="suggestions" role="listbox">
            {suggestions.map((c, i) => (
              <button key={c.addr} type="button" role="option" aria-selected={i === active} className={i === active ? 'active' : ''} onMouseDown={(e) => { e.preventDefault(); pick(c) }}>
                {formatAddress(c)}
              </button>
            ))}
          </div>
        )}
      </div>
      {invalid && <div className="field-error">{invalid}</div>}
    </div>
  )
}
