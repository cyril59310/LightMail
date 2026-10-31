// Minimal API client: JSON, CSRF token, structured errors.

let csrfToken = ''

export function setCsrfToken(t: string | undefined) {
  csrfToken = t ?? ''
}

export class ApiError extends Error {
  status: number
  code: string
  data: unknown
  constructor(status: number, code: string, message: string, data?: unknown) {
    super(message)
    this.status = status
    this.code = code
    this.data = data
  }
}

type Listener = () => void
const unauthorizedListeners = new Set<Listener>()
export function onUnauthorized(l: Listener) {
  unauthorizedListeners.add(l)
  return () => {
    unauthorizedListeners.delete(l)
  }
}

async function request<T>(method: string, path: string, body?: unknown, opts: { raw?: boolean; signal?: AbortSignal } = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  let payload: BodyInit | undefined
  if (body instanceof FormData) {
    payload = body
  } else if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    payload = JSON.stringify(body)
  }
  if (method !== 'GET' && csrfToken) headers['X-CSRF-Token'] = csrfToken
  const res = await fetch('/api/v1' + path, { method, headers, body: payload, credentials: 'same-origin', signal: opts.signal })
  if (res.status === 204) return undefined as T
  const isJSON = (res.headers.get('Content-Type') || '').includes('application/json')
  const data = isJSON ? await res.json().catch(() => ({})) : await res.text()
  if (!res.ok) {
    if (res.status === 401 && !path.startsWith('/auth/')) unauthorizedListeners.forEach((l) => l())
    const err = (data as { error?: { code?: string; message?: string } })?.error
    throw new ApiError(res.status, err?.code ?? 'http_' + res.status, err?.message ?? res.statusText, data)
  }
  return data as T
}

export const api = {
  get: <T>(path: string, signal?: AbortSignal) => request<T>('GET', path, undefined, { signal }),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body ?? {}),
  put: <T>(path: string, body?: unknown) => request<T>('PUT', path, body ?? {}),
  patch: <T>(path: string, body?: unknown) => request<T>('PATCH', path, body ?? {}),
  del: <T>(path: string) => request<T>('DELETE', path),
  upload: <T>(path: string, form: FormData) => request<T>('POST', path, form),
}

export function qs(params: Record<string, string | number | boolean | undefined | null>): string {
  const p = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== '' && v !== false) p.set(k, String(v))
  }
  const s = p.toString()
  return s ? '?' + s : ''
}
