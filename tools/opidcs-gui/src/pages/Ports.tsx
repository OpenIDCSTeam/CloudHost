import { useState } from 'react'
import type { PageProps } from '../App'
import type { Row } from '../types'
import { useLoad } from '../hooks/useLoad'
import PageHeader from '../components/PageHeader'
import DataTable from '../components/DataTable'
import { IconRefresh } from '../components/Icons'

type Rule = Row & { host: string; uuid: string; index: number }

export default function Ports({ api, notify }: PageProps) {
  const [host, setHost] = useState('')
  const [vm, setVm] = useState('')

  const hosts = useLoad<Row[]>(async () => {
    const map = await api.get<Record<string, Row>>('/api/server/detail')
    return Object.entries(map || {}).map(([name, value]) => ({ name, ...value }))
  }, [api])

  const rules = useLoad<Rule[]>(async () => {
    if (!host) return []
    const list = await api.get<Record<string, Row>>(`/api/client/detail/${encodeURIComponent(host)}`)
    const uuids = vm ? [vm] : Object.keys(list || {})
    const out: Rule[] = []
    for (const uuid of uuids) {
      try {
        const data = await api.get<unknown>(`/api/client/natget/${encodeURIComponent(host)}/${encodeURIComponent(uuid)}`)
        const arr = Array.isArray(data) ? (data as Row[]) : []
        arr.forEach((rule, index) => out.push({ ...rule, host, uuid, index }))
      } catch (err) {
        console.error(`加载 ${uuid} 端口映射失败`, err)
      }
    }
    return out
  }, [api, host, vm])

  async function remove(row: Rule) {
    if (!window.confirm(`确认删除 ${row.uuid} 的规则 #${row.index}？`)) return
    try {
      await api.del(`/api/client/natdel/${encodeURIComponent(row.host)}/${encodeURIComponent(row.uuid)}/${row.index}`)
      notify('规则已删除', 'ok')
      rules.reload()
    } catch (err) {
      notify(err instanceof Error ? err.message : '删除失败', 'error')
    }
  }

  return (
    <>
      <PageHeader
        title="端口转发"
        desc="按主机与实例查看 TCP 端口映射"
        actions={
          <>
            <select
              className="select select--inline"
              value={host}
              onChange={(e) => {
                setHost(e.target.value)
                setVm('')
              }}
            >
              <option value="">选择主机</option>
              {(hosts.data || []).map((h) => (
                <option key={String(h.name)} value={String(h.name)}>{String(h.name)}</option>
              ))}
            </select>
            <button className="btn" onClick={() => rules.reload()} disabled={!host} type="button">
              <IconRefresh size={15} /> 刷新
            </button>
          </>
        }
      />

      {!host ? (
        <div className="panel">
          <div className="empty">
            <strong>先选择主机</strong>
            端口映射按实例维护，需要先确定主机范围
          </div>
        </div>
      ) : (
        <DataTable
          columns={[
            { key: 'uuid', label: '实例' },
            { key: 'wan_port', label: '公网端口' },
            { key: 'lan_port', label: '内网端口' },
            { key: 'lan_addr', label: '内网地址' },
            { key: 'nat_tips', label: '备注' },
            {
              key: 'actions',
              label: '操作',
              render: (row) => (
                <button className="btn btn--sm btn--danger" type="button" onClick={(e) => { e.stopPropagation(); remove(row as Rule) }}>
                  删除
                </button>
              ),
            },
          ]}
          rows={rules.data || []}
          loading={rules.loading}
          empty="该主机下还没有端口映射"
        />
      )}
      <p className="hint" style={{ marginTop: 12 }}>
        新增规则请在实例详情或 <code className="mono">opidcs port add</code> 中完成，避免误占公网端口。
      </p>
    </>
  )
}
