// Hash-based router: #/view/param?query
import { useSyncExternalStore } from 'react'

export interface Route {
  path: string[]
  query: URLSearchParams
}

function parse(): Route {
  const h = location.hash.replace(/^#\/?/, '')
  const [p, q = ''] = h.split('?')
  return { path: p.split('/').filter(Boolean).map(decodeURIComponent), query: new URLSearchParams(q) }
}

let current = parse()
const listeners = new Set<() => void>()
window.addEventListener('hashchange', () => {
  current = parse()
  listeners.forEach((l) => l())
})

export function useRoute(): Route {
  return useSyncExternalStore(
    (cb) => {
      listeners.add(cb)
      return () => listeners.delete(cb)
    },
    () => current,
  )
}

export function navigate(path: string, replace = false) {
  const target = '#/' + path.replace(/^#?\/?/, '')
  if (replace) history.replaceState(null, '', target)
  else if (location.hash !== target) location.hash = target
  else return
  if (replace) {
    current = parse()
    listeners.forEach((l) => l())
  }
}

export function href(path: string) {
  return '#/' + path.replace(/^\/?/, '')
}
