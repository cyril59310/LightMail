import { describe, expect, it } from 'vitest'
import { escapeHTML, isEmail, mentionsAttachment, parseAddresses } from './util'

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
