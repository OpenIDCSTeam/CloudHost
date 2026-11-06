import { useMemo } from 'react'
import type { PageProps } from '../App'
import type { Row } from '../types'
import { pick, toRows } from '../api'
import { useLoad } from '../hooks/useLoad'
import PageHeader from '../components/PageHeader'
import DataTable from '../components/DataTable'
import StatusPill from '../components/StatusPill'
import { IconRefresh } from '../components/Icons'

type Statis = {
  host_count?: number
  vm_count?: number
  hosts?: Array<Record<string, unknown>>
}

export default function Overview({ api, notify }: PageProps) {
  const statis = useLoad<Statis>(() => api.get<Statis>('/api/system/statis'), [api])
  const stats = useLoad<Record<string, unknown>>(() => api.get('/api/system/stats'), [api])
  const tasks = useLoad<unknown>(() => api.get('/api/tasks', { limit: '6' }), [api])

  const hostRows = useMemo<Row[]>(() => {
    const hosts = statis.data?.hosts
    return Array.isArray(hosts) ? (hosts as Row[]) : []
  }, [statis.data])

  const online = hostRows.filter((h) => h.enable_host === true).length

  const numericStats = useMemo(() => {
    const data = stats.data || {}
    return Object.entries(data)
      .filter(([, v]) => typeof v === 'number')
      .slice(0, 4)
  }, [stats.data])

  function refresh() {
    statis.reload()
    stats.reload()
    tasks.reload()
    notify('已刷新', 'ok')
  }

  return (
    <>
      <PageHeader
        title="概览"
        desc="主控健康度、资源规模与最近任务"
        actions={
          <button className="btn" onClick={refresh} type="button">
            <IconRefresh size={15} /> 刷新
          </button>
        }
      />

      {(statis.error || stats.error) && (
        <div className="panel" style={{ padding: 14, marginBottom: 16, color: 'var(--red)' }}>
          连接异常：{statis.error || stats.error}
        </div>
      )}

      <div className="cards">
        <div className="card card--accent">
          <span className="card__label">Hosts</span>
          <div className="card__value">{statis.loading ? '—' : statis.data?.host_count ?? 0}</div>
          <div className="card__hint">{online} 台启用 · {hostRows.length} 条记录</div>
        </div>
        <div className="card">
          <span className="card__label">Instances</span>
          <div className="card__value">{statis.loading ? '—' : statis.data?.vm_count ?? 0}</div>
          <div className="card__hint">由主控记录汇总</div>
        </div>
        <div className="card">
          <span className="card__label">Platforms</span>
          <div className="card__value">{new Set(hostRows.map((h) => String(h.server_type ?? ''))).size || 0}</div>
          <div className="card__hint">不同服务器类型</div>
        </div>
        <div className="card">
          <span className="card__label">Tasks</span>
          <div className="card__value">{toRows(tasks.data).length}</div>
          <div className="card__hint">最近任务</div>
        </div>
      </div>

      {numericStats.length > 0 && (
        <div className="cards" style={{ gridTemplateColumns: `repeat(${Math.min(numericStats.length, 4)}, minmax(0,1fr))` }}>
          {numericStats.map(([key, value]) => (
            <div className="card" key={key}>
              <span className="card__label">{key}</span>
              <div className="card__value" style={{ fontSize: 22 }}>{String(value)}</div>
            </div>
          ))}
        </div>
      )}

      <div className="panel" style={{ marginBottom: 18 }}>
        <div className="panel__head">
          <span className="panel__title">主机分布</span>
        </div>
        <DataTable
          columns={[
            { key: 'hs_name', label: '主机' },
            { key: 'server_type', label: '类型' },
            { key: 'vm_count', label: '实例数' },
            { key: 'enable_host', label: '状态', render: (row) => <StatusPill value={row.enable_host ? 'active' : 'inactive'} /> },
          ]}
          rows={hostRows.map((h) => ({ ...h, name: pick(h, ['hs_name']) }))}
          loading={statis.loading}
          empty="还没有接入主机，去「主机」页新增"
        />
      </div>

      <div className="panel">
        <div className="panel__head">
          <span className="panel__title">最近任务</span>
        </div>
        <DataTable
          columns={[
            { key: 'id', label: 'ID', width: '90px' },
            { key: 'type', label: '类型' },
            { key: 'status', label: '状态', render: (row) => <StatusPill value={row.status} /> },
            { key: 'progress', label: '进度', render: (row) => `${row.progress ?? 0}%` },
            { key: 'message', label: '信息' },
          ]}
          rows={toRows(tasks.data)}
          loading={tasks.loading}
          empty="暂无任务"
        />
      </div>
    </>
  )
}
