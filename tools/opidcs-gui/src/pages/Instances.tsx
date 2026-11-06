import { useMemo, useState } from 'react'
import type { PageProps } from '../App'
import type { Row } from '../types'
import { pick, toRows } from '../api'
import { useLoad } from '../hooks/useLoad'
import PageHeader from '../components/PageHeader'
import DataTable from '../components/DataTable'
import StatusPill from '../components/StatusPill'
import Drawer from '../components/Drawer'
import { IconRefresh } from '../components/Icons'

type Bundle = { hosts: Row[]; vms: Row[] }

export default function Instances({ api, coreUrl, notify }: PageProps) {
  const [host, setHost] = useState('')
  const [selected, setSelected] = useState<Row | null>(null)

  const data = useLoad<Bundle>(async () => {
    const hostMap = await api.get<Record<string, Row>>('/api/server/detail')
    const hosts = Object.entries(hostMap || {}).map(([name, value]) => ({ name, ...value }))
    const vms: Row[] = []
    for (const h of hosts) {
      const name = String(h.name)
      try {
        const list = await api.get<Record<string, Row>>(`/api/client/detail/${encodeURIComponent(name)}`)
        Object.entries(list || {}).forEach(([uuid, vm]) => {
          vms.push({ ...vm, uuid, host: name })
        })
      } catch (err) {
        console.error(`加载 ${name} 实例失败`, err)
      }
    }
    return { hosts, vms }
  }, [api])

  const rows = useMemo(() => {
    const all = data.data?.vms || []
    return host ? all.filter((v) => v.host === host) : all
  }, [data.data, host])

  async function power(row: Row, action: string) {
    try {
      await api.post(`/api/client/powers/${encodeURIComponent(String(row.host))}/${encodeURIComponent(String(row.uuid))}`, { action })
      notify(`${row.uuid} 已执行 ${action}`, 'ok')
      data.reload()
    } catch (err) {
      notify(err instanceof Error ? err.message : '操作失败', 'error')
    }
  }

  async function remove(row: Row) {
    if (!window.confirm(`确认删除实例 ${row.uuid}？`)) return
    try {
      await api.del(`/api/client/delete/${encodeURIComponent(String(row.host))}/${encodeURIComponent(String(row.uuid))}`)
      notify('删除任务已提交', 'ok')
      data.reload()
    } catch (err) {
      notify(err instanceof Error ? err.message : '删除失败', 'error')
    }
  }

  return (
    <>
      <PageHeader
        title="实例"
        desc="跨主机查看实例，执行电源与删除操作"
        actions={
          <>
            <select className="select select--inline" value={host} onChange={(e) => setHost(e.target.value)}>
              <option value="">全部主机</option>
              {(data.data?.hosts || []).map((h) => (
                <option key={String(h.name)} value={String(h.name)}>{String(h.name)}</option>
              ))}
            </select>
            <button className="btn" onClick={() => data.reload()} type="button">
              <IconRefresh size={15} /> 刷新
            </button>
            <button className="btn" onClick={() => window.open(`${coreUrl}/#/vms`, '_blank')} type="button">
              打开 Web 控制台
            </button>
          </>
        }
      />

      <DataTable
        columns={[
          { key: 'name', label: '名称', render: (row) => pick(row, ['name', 'vm_name', 'uuid']) },
          { key: 'host', label: '主机' },
          { key: 'uuid', label: 'UUID' },
          { key: 'status', label: '状态', render: (row) => <StatusPill value={pick(row, ['status', 'vm_flag'])} /> },
          { key: 'ip', label: 'IP', render: (row) => pick(row, ['ip', 'ip4_addr', 'lan_addr']) },
          {
            key: 'actions',
            label: '操作',
            render: (row) => (
              <div style={{ display: 'flex', gap: 6 }} onClick={(e) => e.stopPropagation()}>
                <button className="btn btn--sm" onClick={() => power(row, 'start')} type="button">开机</button>
                <button className="btn btn--sm" onClick={() => power(row, 'stop')} type="button">关机</button>
                <button className="btn btn--sm" onClick={() => power(row, 'restart')} type="button">重启</button>
                <button className="btn btn--sm btn--danger" onClick={() => remove(row)} type="button">删除</button>
              </div>
            ),
          },
        ]}
        rows={rows}
        loading={data.loading}
        onRow={setSelected}
        empty="没有实例，去 Web 控制台创建"
      />

      <Drawer open={!!selected} title="实例详情" row={selected} onClose={() => setSelected(null)} />
    </>
  )
}
