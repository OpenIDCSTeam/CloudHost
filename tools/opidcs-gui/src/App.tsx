import { useEffect, useMemo, useState } from 'react'
import { Api } from './api'
import { loadCores, saveCores } from './store/cores'
import type { CoresFile, PageKey, Toast } from './types'
import Layout from './components/Layout'
import CoreSwitcher from './components/CoreSwitcher'
import Overview from './pages/Overview'
import Hosts from './pages/Hosts'
import Instances from './pages/Instances'
import Ports from './pages/Ports'
import Proxies from './pages/Proxies'
import Users from './pages/Users'
import Tasks from './pages/Tasks'
import Logs from './pages/Logs'
import Settings from './pages/Settings'

export type PageProps = {
  api: Api
  coreUrl: string
  notify: (text: string, kind?: 'ok' | 'error' | 'info') => void
}

export default function App() {
  const [file, setFile] = useState<CoresFile>({ default: '', cores: [] })
  const [active, setActive] = useState('')
  const [page, setPage] = useState<PageKey>('overview')
  const [online, setOnline] = useState<boolean | null>(null)
  const [toasts, setToasts] = useState<Toast[]>([])
  const [theme, setTheme] = useState<'light' | 'dark'>(() => (localStorage.getItem('opidcs.theme') as 'light' | 'dark') || 'dark')

  useEffect(() => {
    loadCores().then((data) => {
      setFile(data)
      setActive(data.default || data.cores[0]?.name || '')
    })
  }, [])

  useEffect(() => {
    document.documentElement.dataset.theme = theme
    localStorage.setItem('opidcs.theme', theme)
  }, [theme])

  const core = useMemo(() => file.cores.find((c) => c.name === active), [file, active])
  const api = useMemo(() => (core ? new Api(core) : null), [core])

  useEffect(() => {
    if (!api) {
      setOnline(null)
      return
    }
    let cancelled = false
    api
      .get('/api/system/statis')
      .then(() => !cancelled && setOnline(true))
      .catch(() => !cancelled && setOnline(false))
    return () => {
      cancelled = true
    }
  }, [api])

  function notify(text: string, kind: 'ok' | 'error' | 'info' = 'info') {
    const id = Date.now() + Math.random()
    setToasts((prev) => [...prev, { id, kind, text }])
    window.setTimeout(() => setToasts((prev) => prev.filter((t) => t.id !== id)), 3600)
  }

  async function handleSave(next: CoresFile) {
    setFile(next)
    await saveCores(next)
    if (!next.cores.some((c) => c.name === active)) {
      setActive(next.default || next.cores[0]?.name || '')
    }
  }

  const props: PageProps | null = api && core ? { api, coreUrl: core.url, notify } : null

  return (
    <Layout
      page={page}
      onNav={setPage}
      theme={theme}
      onTheme={() => setTheme(theme === 'dark' ? 'light' : 'dark')}
      toolbar={
        <CoreSwitcher
          file={file}
          active={active}
          online={online}
          onSelect={setActive}
          onSave={handleSave}
          notify={notify}
        />
      }
    >
      {!props ? (
        <WelcomeCard />
      ) : page === 'overview' ? (
        <Overview {...props} />
      ) : page === 'hosts' ? (
        <Hosts {...props} />
      ) : page === 'instances' ? (
        <Instances {...props} />
      ) : page === 'ports' ? (
        <Ports {...props} />
      ) : page === 'proxies' ? (
        <Proxies {...props} />
      ) : page === 'users' ? (
        <Users {...props} />
      ) : page === 'tasks' ? (
        <Tasks {...props} />
      ) : page === 'logs' ? (
        <Logs {...props} />
      ) : (
        <Settings {...props} core={core!} onSaveCore={handleSave} file={file} />
      )}

      <div className="toasts">
        {toasts.map((t) => (
          <div key={t.id} className={`toast toast--${t.kind}`}>
            {t.text}
          </div>
        ))}
      </div>
    </Layout>
  )
}

function WelcomeCard() {
  return (
    <div style={{ maxWidth: 720, margin: '8vh auto 0', textAlign: 'center' }}>
      <div
        style={{
          width: 64,
          height: 64,
          margin: '0 auto 18px',
          borderRadius: 20,
          background: 'var(--grad)',
          display: 'grid',
          placeItems: 'center',
          color: '#fff',
          fontSize: 26,
          fontWeight: 800,
          boxShadow: '0 24px 50px -24px rgba(47,107,255,.8)',
        }}
      >
        O
      </div>
      <h1 style={{ margin: 0, fontSize: 30, letterSpacing: '-0.02em' }}>连接你的第一台主控</h1>
      <p style={{ margin: '12px auto 0', maxWidth: 520, color: 'var(--muted)', lineHeight: 1.8 }}>
        点击右上角「主控」新增一台 CloudHost。地址形如 http://127.0.0.1:1881，Token 在「系统设置 → 令牌」获取。
      </p>
      <div className="cards" style={{ marginTop: 28, gridTemplateColumns: 'repeat(3, minmax(0, 1fr))' }}>
        {[
          ['01', '新增主控', '名称、地址、Token'],
          ['02', '测试连接', '保存前先验证'],
          ['03', '开始管理', '主机 / 实例 / 网络'],
        ].map(([n, t, d]) => (
          <div className="card" key={n} style={{ textAlign: 'left' }}>
            <span className="card__label">{n}</span>
            <div style={{ marginTop: 8, fontWeight: 600 }}>{t}</div>
            <div className="card__hint">{d}</div>
          </div>
        ))}
      </div>
    </div>
  )
}
