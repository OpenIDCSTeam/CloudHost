import type { ReactNode } from 'react'
import type { PageKey } from '../types'
import {
  IconBox,
  IconGlobe,
  IconGrid,
  IconLogs,
  IconMoon,
  IconNetwork,
  IconServer,
  IconSettings,
  IconSun,
  IconTasks,
  IconUsers,
} from './Icons'

type Props = {
  page: PageKey
  onNav: (page: PageKey) => void
  theme: 'light' | 'dark'
  onTheme: () => void
  children: ReactNode
  toolbar: ReactNode
}

const nav: Array<{ key: PageKey; label: string; icon: ReactNode }> = [
  { key: 'overview', label: '概览', icon: <IconGrid /> },
  { key: 'hosts', label: '主机', icon: <IconServer /> },
  { key: 'instances', label: '实例', icon: <IconBox /> },
  { key: 'ports', label: '端口转发', icon: <IconNetwork /> },
  { key: 'proxies', label: '反向代理', icon: <IconGlobe /> },
  { key: 'users', label: '用户', icon: <IconUsers /> },
  { key: 'tasks', label: '任务', icon: <IconTasks /> },
  { key: 'logs', label: '日志', icon: <IconLogs /> },
  { key: 'settings', label: '设置', icon: <IconSettings /> },
]

export default function Layout({ page, onNav, theme, onTheme, children, toolbar }: Props) {
  return (
    <div className="app">
      <aside className="sidebar">
        <div className="brand">
          <span className="brand__mark">O</span>
          <span className="brand__text">
            <strong>opidcs</strong>
            <small>OpenIDCS</small>
          </span>
        </div>

        <nav className="nav">
          {nav.map((item) => (
            <button
              key={item.key}
              className={`nav__item${page === item.key ? ' is-active' : ''}`}
              onClick={() => onNav(item.key)}
              type="button"
            >
              {item.icon}
              <span>{item.label}</span>
            </button>
          ))}
        </nav>

        <div className="sidebar__foot">
          多主控管理 · 与 CLI 同源
          <br />
          AGPLv3 · OpenIDCS Team
        </div>
      </aside>

      <main className="main">
        <header className="topbar">
          <span className="topbar__title">{nav.find((n) => n.key === page)?.label}</span>
          <span className="topbar__spacer" />
          {toolbar}
          <button className="icon-btn" onClick={onTheme} title="切换外观" type="button">
            {theme === 'dark' ? <IconSun size={17} /> : <IconMoon size={17} />}
          </button>
        </header>
        <div className="content">{children}</div>
      </main>
    </div>
  )
}
