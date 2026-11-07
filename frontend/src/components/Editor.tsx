import { useEffect, useRef, useState, type CSSProperties, type KeyboardEvent } from 'react'
import { api, qs } from '../api'
import { t, type Key } from '../i18n'
import type { Address } from '../types'
import { formatAddress, parseAddresses } from '../util'
import { Icon } from './Icons'

// Sizes use <font size>, understood by every mail client (Outlook included).
const fontSizes: [string, Key, string][] = [
  ['2', 'compose.size.small', '0.82em'],
  ['3', 'compose.size.normal', '1em'],
  ['5', 'compose.size.large', '1.4em'],
  ['6', 'compose.size.huge', '1.8em'],
]

// Mid-tone colours, readable on the white background of most mail clients
// and on the dark theme of the editor.
const textColors = [
  '#d93025', '#e8710a', '#f9ab00', '#188038', '#12a4af', '#1a73e8', '#7b3fe4', '#c2185b',
  '#5f6368', '#000000', '#8d6e63', '#9aa0a6',
]

/** Minimal rich-text editor based on contentEditable. */
export function RichEditor({ initialHTML, onChange, autoFocus, onFiles }: { initialHTML: string; onChange: (html: string) => void; autoFocus?: boolean; onFiles?: (files: File[]) => void }) {
  const ref = useRef<HTMLDivElement>(null)
  // Last selection inside the editor: menus and the colour picker take the
  // focus, the selection is restored before applying a command.
  const range = useRef<Range | null>(null)
  const [menu, setMenu] = useState<'size' | 'color' | null>(null)
  const [lastColor, setLastColor] = useState(textColors[0])
  const menus = useRef<HTMLDivElement>(null)

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

  useEffect(() => {
    if (!menu) return
    const close = (e: MouseEvent) => {
      if (!menus.current?.contains(e.target as Node)) setMenu(null)
    }
    const esc = (e: globalThis.KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        setMenu(null)
      }
    }
    document.addEventListener('mousedown', close)
    window.addEventListener('keydown', esc, true)
    return () => {
      document.removeEventListener('mousedown', close)
      window.removeEventListener('keydown', esc, true)
    }
  }, [menu])

  const inEditor = (sel: Selection | null): boolean => !!sel && sel.rangeCount > 0 && !!ref.current?.contains(sel.anchorNode) && !!ref.current?.contains(sel.focusNode)
  const saveRange = () => {
    const sel = window.getSelection()
    if (sel && inEditor(sel)) range.current = sel.getRangeAt(0).cloneRange()
  }
  // Follow every selection change (mouse released outside the editor,
  // double click, Shift+arrows, select all…), not only clicks inside it.
  useEffect(() => {
    document.addEventListener('selectionchange', saveRange)
    return () => document.removeEventListener('selectionchange', saveRange)
  }, [])

  const exec = (cmd: string, value?: string) => {
    const el = ref.current
    if (!el) return
    const sel = window.getSelection()
    // The toolbar keeps the focus in the editor: use the live selection when
    // it is there, and only restore the saved one when something took the
    // focus (colour picker).
    if (!inEditor(sel) && range.current) {
      el.focus()
      sel?.removeAllRanges()
      sel?.addRange(range.current)
    } else if (document.activeElement !== el) {
      el.focus()
    }
    // Presentational tags (<font>) rather than CSS spans: better support in mail clients.
    document.execCommand('styleWithCSS', false, 'false')
    document.execCommand(cmd, false, value)
    saveRange()
    onChange(el.innerHTML)
  }
  const color = (c: string) => {
    setLastColor(c)
    setMenu(null)
    exec('foreColor', c)
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
  const toolButton = (icon: string, label: string, run: () => void, extra?: { expanded?: boolean; style?: CSSProperties }) => (
    <button
      key={icon}
      type="button"
      className={'icon-btn small' + (extra?.expanded ? ' active' : '')}
      title={label}
      aria-label={label}
      aria-haspopup={extra?.expanded !== undefined ? 'true' : undefined}
      aria-expanded={extra?.expanded}
      onMouseDown={(e) => e.preventDefault()}
      onClick={run}
      style={extra?.style}
    >
      <Icon name={icon} size={15} />
    </button>
  )

  return (
    <div className="rich-editor">
      <div className="editor-toolbar" role="toolbar" ref={menus}>
        {tools.slice(0, 3).map(([icon, label, run]) => toolButton(icon, label, run))}
        <span className="tool-menu">
          {toolButton('textsize', t('compose.size'), () => setMenu((m) => (m === 'size' ? null : 'size')), { expanded: menu === 'size' })}
          {menu === 'size' && (
            <div className="tool-popover size-menu" role="menu">
              {fontSizes.map(([size, key, preview]) => (
                <button key={size} type="button" role="menuitem" onMouseDown={(e) => e.preventDefault()} onClick={() => { setMenu(null); exec('fontSize', size) }}>
                  <span style={{ fontSize: preview }}>{t(key)}</span>
                </button>
              ))}
            </div>
          )}
        </span>
        <span className="tool-menu">
          {toolButton('palette', t('compose.color'), () => setMenu((m) => (m === 'color' ? null : 'color')), { expanded: menu === 'color', style: { color: lastColor } })}
          {menu === 'color' && (
            <div className="tool-popover color-menu" role="menu" aria-label={t('compose.color')}>
              <div className="swatches">
                {textColors.map((c) => (
                  <button key={c} type="button" role="menuitem" className="swatch" style={{ background: c }} title={c} aria-label={c} onMouseDown={(e) => e.preventDefault()} onClick={() => color(c)} />
                ))}
              </div>
              <div className="custom-color">
                <input type="color" value={lastColor} onChange={(e) => setLastColor(e.target.value)} aria-label={t('compose.customColor')} />
                <span className="grow small">{t('compose.customColor')}</span>
                <button type="button" className="btn small" onMouseDown={(e) => e.preventDefault()} onClick={() => color(lastColor)}>
                  {t('compose.apply')}
                </button>
              </div>
            </div>
          )}
        </span>
        {tools.slice(3).map(([icon, label, run]) => toolButton(icon, label, run))}
      </div>
      <div
        ref={ref}
        className="editor-area"
        contentEditable
        role="textbox"
        aria-multiline="true"
        onInput={(e) => {
          saveRange()
          onChange(e.currentTarget.innerHTML)
        }}
        onKeyUp={saveRange}
        onMouseUp={saveRange}
        onBlur={saveRange}
        onPaste={(e) => {
          // Pasted files become attachments; inline images are not supported.
          if (e.clipboardData.files.length) {
            e.preventDefault()
            onFiles?.(Array.from(e.clipboardData.files))
          }
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
