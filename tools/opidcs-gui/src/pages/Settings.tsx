import { useEffect, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import type { PageProps } from '../App'
import type { Core, CoresFile } from '../types'
import { IconRefresh } from '../components/Icons'

type Props = PageProps & {
  core: Core
  file: CoresFile
  onSaveCore: (file: CoresFile) => Promise<void>
}

export default function Settings({ api, core, notify }: Props) {
  const [token, setToken] = useState('')
  const [reveal, setReveal] = useState(false)
  const [password, setPassword] = useState('')
  const [version, setVersion] = useState('1.0.0')
  const [checking, setChecking] = useState(false)

  useEffect(() => {
    api
      .get<{ token?: string }>('/api/token/current')
      .then((data) => setToken(data?.token || ''))
      .catch((err) => console.error('读取 Token 失败', err))
  }, [api])

  useEffect(() => {
    invoke<string>('app_version')
      .then(setVersion)
      .catch(() => setVersion('web'))
  }, [])

  async function resetToken() {
    if (!window.confirm('重置后所有插件与 CLI 配置都会失效，继续？')) return
    try {
      const data = await api.post<{ token?: string }>('/api/token/reset')
      setToken(data?.token || '')
      notify('Token 已重置，请同步更新插件配置', 'ok')
    } catch (err) {
      notify(err instanceof Error ? err.message : '重置失败', 'error')
    }
  }

  async function resetAdmin() {
    if (password.length < 8) {
      notify('密码至少 8 位', 'error')
      return
    }
    try {
      const data = await api.get<unknown>('/api/users', { limit: '500' })
      const rows = Array.isArray(data) ? data : ((data as { list?: unknown[] })?.list || [])
      const admin = (rows as Array<Record<string, unknown>>).find((u) => String(u.username) === 'admin')
      if (!admin) throw new Error('未找到 admin 用户')
      await api.put(`/api/users/${String(admin.id)}`, { password })
      setPassword('')
      notify('管理员密码已重置', 'ok')
    } catch (err) {
      notify(err instanceof Error ? err.message : '重置失败', 'error')
    }
  }

  async function checkUpdate() {
    setChecking(true)
    try {
      const updater = await import('@tauri-apps/plugin-updater')
      const update = await updater.check()
      if (update) {
        notify(`发现新版本 ${update.version}`, 'info')
      } else {
        notify('已是最新版本', 'ok')
      }
    } catch (err) {
      notify(err instanceof Error ? err.message : '检查更新失败', 'error')
    } finally {
      setChecking(false)
    }
  }

  return (
    <>
      <div className="page-head">
        <div>
          <h1>设置</h1>
          <p>主控凭据、管理员密码与客户端更新</p>
        </div>
      </div>

      <div className="panel" style={{ marginBottom: 16 }}>
        <div className="panel__head">
          <span className="panel__title">当前主控</span>
        </div>
        <div style={{ padding: 18 }}>
          <dl className="kv">
            <dt>name</dt><dd>{core.name}</dd>
            <dt>url</dt><dd>{core.url}</dd>
            <dt>token</dt><dd className="mono">{reveal ? token : mask(token)}</dd>
            <dt>client</dt><dd>{version}</dd>
          </dl>
          <div style={{ display: 'flex', gap: 10, marginTop: 16 }}>
            <button className="btn btn--sm" type="button" onClick={() => setReveal(!reveal)}>
              {reveal ? '隐藏 Token' : '显示 Token'}
            </button>
            <button className="btn btn--sm btn--danger" type="button" onClick={resetToken}>
              重置 Token
            </button>
            <button className="btn btn--sm" type="button" onClick={checkUpdate} disabled={checking}>
              <IconRefresh size={14} /> {checking ? '检查中…' : '检查更新'}
            </button>
          </div>
        </div>
      </div>

      <div className="panel">
        <div className="panel__head">
          <span className="panel__title">管理员密码</span>
        </div>
        <div style={{ padding: 18, maxWidth: 420 }}>
          <div className="field">
            <label>新密码</label>
            <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
            <span className="hint">通过管理员 Token 调用 /api/users 更新，不依赖旧密码</span>
          </div>
          <button className="btn btn--primary" type="button" onClick={resetAdmin}>
            重置 admin 密码
          </button>
        </div>
      </div>
    </>
  )
}

function mask(value: string): string {
  if (!value) return '—'
  if (value.length <= 8) return '****'
  return `${value.slice(0, 4)}****${value.slice(-4)}`
}
