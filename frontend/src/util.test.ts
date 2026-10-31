import { describe, expect, it } from 'vitest'
import type { Account } from './types'
import { accountLayout, escapeHTML, isEmail, mentionsAttachment, moveAccount, orderedAccounts, parseAddresses } from './util'

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
