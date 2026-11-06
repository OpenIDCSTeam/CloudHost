import type { Core, Row } from './types'

type Envelope = {
  code?: number
  msg?: string
  data?: unknown
}

export class ApiError extends Error {
  code: number
  constructor(code: number, message: string) {
    super(message)
    this.code = code
  }
}

export class Api {
  private base: string
  private token: string
  private csrf = ''

  constructor(core: Core) {
    this.base = core.url.replace(/\/$/, '')
    this.token = core.token
  }

  private async csrfToken(): Promise<string> {
    if (this.csrf) return this.csrf
    const res = await fetch(`${this.base}/api/csrf-token`, {
      headers: this.token ? { Authorization: `Bearer ${this.token}` } : {},
    })
    const data = (await res.json()) as { csrf_token?: string }
    this.csrf = data.csrf_token || ''
    return this.csrf
  }

  async request<T = unknown>(path: string, init: RequestInit = {}, query?: Record<string, string>): Promise<T> {
    const url = new URL(`${this.base}${path}`)
    if (query) {
      Object.entries(query).forEach(([k, v]) => {
        if (v !== '' && v !== undefined && v !== null) url.searchParams.set(k, v)
      })
    }
    const method = (init.method || 'GET').toUpperCase()
    const headers: Record<string, string> = {
      Accept: 'application/json',
      ...(init.headers as Record<string, string>),
    }
    if (this.token) headers.Authorization = `Bearer ${this.token}`
    if (method !== 'GET') {
      headers['X-CSRF-Token'] = await this.csrfToken()
      if (init.body) headers['Content-Type'] = 'application/json'
    }

    const res = await fetch(url.toString(), { ...init, method, headers })
    const text = await res.text()
    let payload: Envelope | null = null
    try {
      payload = text ? (JSON.parse(text) as Envelope) : null
    } catch {
      payload = null
    }
    if (!res.ok) {
      throw new ApiError(res.status, payload?.msg || text || `HTTP ${res.status}`)
    }
    if (payload && typeof payload.code === 'number' && payload.code !== 200) {
      if (payload.code === 401) this.csrf = ''
      throw new ApiError(payload.code, payload.msg || '接口错误')
    }
    return (payload?.data ?? payload) as T
  }

  get<T = unknown>(path: string, query?: Record<string, string>): Promise<T> {
    return this.request<T>(path, {}, query)
  }

  post<T = unknown>(path: string, body?: unknown): Promise<T> {
    return this.request<T>(path, { method: 'POST', body: body === undefined ? undefined : JSON.stringify(body) })
  }

  put<T = unknown>(path: string, body?: unknown): Promise<T> {
    return this.request<T>(path, { method: 'PUT', body: body === undefined ? undefined : JSON.stringify(body) })
  }

  del<T = unknown>(path: string): Promise<T> {
    return this.request<T>(path, { method: 'DELETE' })
  }
}

export async function testCore(core: Core): Promise<boolean> {
  const api = new Api(core)
  await api.get('/api/system/statis')
  return true
}

export function toRows(data: unknown): Row[] {
  if (!data) return []
  if (Array.isArray(data)) {
    return data.filter((x): x is Row => typeof x === 'object' && x !== null)
  }
  if (typeof data === 'object') {
    const map = data as Record<string, unknown>
    for (const key of ['list', 'items', 'tasks', 'users']) {
      if (Array.isArray(map[key])) return toRows(map[key])
    }
    const values = Object.values(map)
    if (values.length > 0 && values.every((v) => typeof v === 'object' && v !== null)) {
      return Object.entries(map).map(([name, value]) => ({ name, ...(value as Row) }))
    }
    return [map as Row]
  }
  return []
}

export function pick(row: Row, keys: string[]): string {
  for (const key of keys) {
    const v = row[key]
    if (v !== undefined && v !== null && v !== '') return String(v)
  }
  return '—'
}
