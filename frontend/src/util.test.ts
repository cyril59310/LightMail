import { describe, expect, it } from 'vitest'
import type { Account, Endpoint } from './types'
import { accountLayout, escapeHTML, hasServerPreset, isEmail, mentionsAttachment, moveAccount, orderedAccounts, parseAddresses, serverPresets } from './util'

describe('server presets', () => {
  const imap: Endpoint = { host: 'imap.x.fr', port: 993, security: 'tls' }
  const smtp: Endpoint = { host: 'smtp.x.fr', port: 587, security: 'starttls' }

  it('ignores unreadable values', () => {
    for (const raw of [undefined, '', '{}', 'null', 'not json']) expect(serverPresets(raw)).toEqual([])
  })

  it('recognises the same servers', () => {
    const list = serverPresets(JSON.stringify([{ id: 's', name: 'X', imap, smtp }]))
    expect(hasServerPreset(list, { ...imap, host: 'IMAP.x.fr' }, smtp)).toBe(true)
    expect(hasServerPreset(list, imap, { ...smtp, port: 465 })).toBe(false)
  })
})

describe('account layout', () => {
  const accounts = [1, 2, 3, 4].map((id) => ({ id })) as Account[]
  const stored = JSON.stringify({ ungrouped: [3, 99], groups: [{ id: 'a', name: 'Pro', accounts: [2, 3] }, { id: 'b', name: 'Perso', accounts: [] }] })

  it('defaults to the server order', () => {
    for (const raw of [undefined, '{}', 'null', 'not json']) {
      expect(accountLayout(raw, accounts)).toEqual({ ungrouped: [1, 2, 3, 4], groups: [] })
    }
  })

  it('drops unknown and duplicate ids and keeps new accounts', () => {
    const l = accountLayout(stored, accounts)
    expect(l).toEqual({ ungrouped: [3, 1, 4], groups: [{ id: 'a', name: 'Pro', accounts: [2] }, { id: 'b', name: 'Perso', accounts: [] }] })
    expect(orderedAccounts(l, accounts).map((a) => a.id)).toEqual([3, 1, 4, 2])
  })

  it('keeps the collapsed state of categories', () => {
    const raw = JSON.stringify({ groups: [{ id: 'a', name: 'Pro', accounts: [2], collapsed: true }] })
    const l = accountLayout(raw, accounts)
    expect(l.groups[0].collapsed).toBe(true)
    expect(moveAccount(l, 1, 'a').groups[0].collapsed).toBe(true)
  })

  it('keeps the collapsed state of accounts', () => {
    const raw = JSON.stringify({ groups: [{ id: 'a', name: 'Pro', accounts: [2] }], collapsedAccounts: [2, 99] })
    const l = accountLayout(raw, accounts)
    expect(l.collapsedAccounts).toEqual([2])
    expect(moveAccount(l, 2, null).collapsedAccounts).toEqual([2])
  })

  it('moves accounts within and across categories', () => {
    const l = accountLayout(stored, accounts)
    expect(moveAccount(l, 4, null, 3).ungrouped).toEqual([4, 3, 1])
    expect(moveAccount(l, 3, null).ungrouped).toEqual([1, 4, 3])
    const moved = moveAccount(l, 1, 'a', 2)
    expect(moved.ungrouped).toEqual([3, 4])
    expect(moved.groups[0].accounts).toEqual([1, 2])
    expect(moveAccount(moved, 1, 'b').groups.map((g) => g.accounts)).toEqual([[2], [1]])
  })
})

describe('parseAddresses', () => {
  it('parses names, quotes and separators', () => {
    const r = parseAddresses('"Dupont, Jean" <jean@exemple.fr>; marie@exemple.fr, Bob <bob@x.io>\nnot-an-address')
    expect(r.valid).toEqual([
      { name: 'Dupont, Jean', addr: 'jean@exemple.fr' },
      { addr: 'marie@exemple.fr' },
      { name: 'Bob', addr: 'bob@x.io' },
    ])
    expect(r.invalid).toEqual(['not-an-address'])
  })

  it('rejects header injection attempts', () => {
    expect(parseAddresses('a@b.c\r\nBcc: evil@x.io').valid.map((a) => a.addr)).not.toContain('Bcc: evil@x.io')
    expect(isEmail('a@b.c>, evil@x.io')).toBe(false)
    expect(isEmail('user@domain')).toBe(false)
  })
})

describe('mentionsAttachment', () => {
  it('detects French and English mentions', () => {
    expect(mentionsAttachment('Vous trouverez la pièce jointe')).toBe(true)
    expect(mentionsAttachment('voir ci-joint le devis')).toBe(true)
    expect(mentionsAttachment('Please find attached')).toBe(true)
    expect(mentionsAttachment('Bonjour, à demain')).toBe(false)
  })
})

describe('escapeHTML', () => {
  it('escapes markup', () => {
    expect(escapeHTML(`<img src=x onerror="alert('x')">&`)).toBe('&lt;img src=x onerror=&quot;alert(&#39;x&#39;)&quot;&gt;&amp;')
  })
})
