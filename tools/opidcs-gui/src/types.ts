export type Core = {
  name: string
  url: string
  token: string
}

export type CoresFile = {
  default: string
  cores: Core[]
}

export type Row = Record<string, unknown>

export type PageKey =
  | 'overview'
  | 'hosts'
  | 'instances'
  | 'ports'
  | 'proxies'
  | 'users'
  | 'tasks'
  | 'logs'
  | 'settings'

export type Column = {
  key: string
  label: string
  width?: string
  render?: (row: Row) => React.ReactNode
}

export type Toast = {
  id: number
  kind: 'ok' | 'error' | 'info'
  text: string
}
