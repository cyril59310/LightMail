// Interface strings. One file per language in ./locales; French is the reference.
//
// To add a language:
//   1. copy locales/en.ts to locales/<code>.ts and translate the values
//      (TypeScript reports any missing or unknown key);
//   2. register it in `languages` below;
//   3. add its code to the "language" rule in backend/internal/api/misc_handlers.go.
import { useSyncExternalStore } from 'react'
import en from './locales/en'
import fr, { type Dict } from './locales/fr'

export type Key = keyof Dict

/** Available languages, in the order shown to the user. `name` is written in the language itself. */
export const languages: { code: string; name: string; locale: string; dict: Dict }[] = [
  { code: 'fr', name: 'Français', locale: 'fr-FR', dict: fr },
  { code: 'en', name: 'English', locale: 'en-GB', dict: en },
]

const byCode = new Map(languages.map((l) => [l.code, l]))

let lang = (() => {
  try {
    const saved = localStorage.getItem('lm.lang')
    if (saved && byCode.has(saved)) return saved
  } catch {
    /* storage unavailable */
  }
  const browser = navigator.language?.split('-')[0]
  return browser && byCode.has(browser) ? browser : 'en'
})()

const listeners = new Set<() => void>()

export function setLanguage(l: string) {
  if (!byCode.has(l) || l === lang) return
  lang = l
  try {
    localStorage.setItem('lm.lang', l)
  } catch {
    /* ignore */
  }
  document.documentElement.lang = l
  listeners.forEach((f) => f())
}

export function getLanguage() {
  return lang
}

/** BCP 47 locale used to format dates and numbers in the current language. */
export function getLocale() {
  return byCode.get(lang)?.locale ?? 'en-GB'
}

export function t(key: Key, vars?: Record<string, string | number>): string {
  let s: string = byCode.get(lang)?.dict[key] ?? fr[key] ?? key
  if (vars) for (const [k, v] of Object.entries(vars)) s = s.split('{' + k + '}').join(String(v))
  return s
}

export function hasKey(key: string): key is Key {
  return key in fr
}

/** Re-render on language change. */
export function useLanguage() {
  return useSyncExternalStore(
    (cb) => {
      listeners.add(cb)
      return () => listeners.delete(cb)
    },
    () => lang,
  )
}
