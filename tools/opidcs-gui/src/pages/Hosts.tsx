import { useState } from 'react'
import type { PageProps } from '../App'
import type { Row } from '../types'
import { toRows } from '../api'
import { useLoad } from '../hooks/useLoad'
import PageHeader from '../components/PageHeader'
import DataTable from '../components/DataTable'
import StatusPill from '../components/StatusPill'
import Drawer from '../components/Drawer'
import Modal from '../components/Modal'
import { IconPlus, IconRefresh } from '../components/Icons'

type NewHost = { name: string; type: string; addr: string; user: string; pass: string }

const empty: NewHost = { name: '', type: 'PromoxSetup', addr: '', user: 'root', pass: '' }

export default function Hosts({ api, notify }: PageProps) {
  const hosts = useLoad<unknown>(() => api.get('/api/server/detail'), [api])
  const [selected, setSelected] = useState<Row | null>(null)
  const [adding, setAdding] = useState(false)
  const [draft, setDraft] = useState<NewHost>(empty)

  const rows = toRows(hosts.data)

  async function toggle(row: Row) {
    const name = String(row.name ?? '')
    const enable = row.enable_host !== true
    try {
      await api.put(`/api/server/update/${encodeURIComponent(name)}`, { enable_host: enable })
      notify(`${name} 已${enable ? '启用' : '禁用'}`, 'ok')
      hosts.reload()
    } catch (err) {
      notify(err instanceof Error ? err.message : '操作失败', 'error')
    }
  }

  async function scan(row: Row) {
    const name = String(row.name ?? '')
    try {
      await api.post(`/api/client/scaner/${encodeURIComponent(name)}`)
      notify(`${name} 已开始扫描`, 'ok')
      hosts.reload()
    } catch (err) {
      notify(err instanceof Error ? err.message : '扫描失败', 'error')
    }
  }

  async function remove(row: Row) {
    const name = String(row.name ?? '')
    if (!window.confirm(`确认删除主机 ${name}？`)) return
    try {
      await api.del(`/api/server/delete/${encodeURIComponent(name)}`)
      notify('主机已删除', 'ok')
      hosts.reload()
    } catch (err) {
      notify(err instanceof Error ? err.message : '删除失败', 'error')
    }
  }

  async function create() {
    if (!draft.name || !draft.addr) {
      notify('名称与地址不能为空', 'error')
      return
    }
    try {
      await api.post('/api/server/create', {
        name: draft.name,
        type: draft.type,
        server_name: draft.name,
        server_type: draft.type,
        server_addr: draft.addr,
        server_user: draft.user,
        server_pass: draft.pass,
        server_port: 22,
      })
      notify('主机已创建', 'ok')
      setAdding(false)
      setDraft(empty)
      hosts.reload()
    } catch (err) {
      notify(err instanceof Error ? err.message : '创建失败', 'error')
    }
  }

  return (
    <>
      <PageHeader
        title="主机"
        desc="接入虚拟化平台，查看连接状态与实例数量"
        actions={
          <>
            <button className="btn" onClick={() => hosts.reload()} type="button">
              <IconRefresh size={15} /> 刷新
            </button>
            <button className="btn btn--primary" onClick={() => setAdding(true)} type="button">
              <IconPlus size={15} /> 新增主机
            </button>
          </>
        }
      />

      <DataTable
        columns={[
          { key: 'name', label: '名称' },
          { key: 'type', label: '类型' },
          { key: 'addr', label: '地址' },
          { key: 'status', label: '状态', render: (row) => <StatusPill value={row.status} /> },
          { key: 'vm_count', label: '实例' },
          {
            key: 'actions',
            label: '操作',
            render: (row) => (
              <div style={{ display: 'flex', gap: 6 }} onClick={(e) => e.stopPropagation()}>
                <button className="btn btn--sm" onClick={() => toggle(row)} type="button">
                  {row.enable_host ? '禁用' : '启用'}
                </button>
                <button className="btn btn--sm" onClick={() => scan(row)} type="button">
                  扫描
                </button>
                <button className="btn btn--sm btn--danger" onClick={() => remove(row)} type="button">
                  删除
                </button>
              </div>
            ),
          },
        ]}
        rows={rows}
        loading={hosts.loading}
        onRow={setSelected}
        empty="还没有主机，点击右上角新增"
      />

      <Drawer open={!!selected} title="主机详情" row={selected} onClose={() => setSelected(null)} />

      <Modal
        open={adding}
        title="新增主机"
        onClose={() => setAdding(false)}
        footer={
          <>
            <button className="btn" onClick={() => setAdding(false)} type="button">取消</button>
            <button className="btn btn--primary" onClick={create} type="button">创建</button>
          </>
        }
      >
        <div className="field">
          <label>名称</label>
          <input className="input" value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} placeholder="pve-lab" />
        </div>
        <div className="field">
          <label>服务器类型</label>
          <select className="select" value={draft.type} onChange={(e) => setDraft({ ...draft, type: e.target.value })}>
            {['PromoxSetup', 'OCInterface', 'LxContainer', 'vSphereESXi', 'HyperVSetup', 'QEMUServer', 'VirtualBoxs', 'VMWareSetup', 'SmolVM', 'MemuAndroid'].map((t) => (
              <option key={t} value={t}>{t}</option>
            ))}
          </select>
        </div>
        <div className="field">
          <label>地址</label>
          <input className="input" value={draft.addr} onChange={(e) => setDraft({ ...draft, addr: e.target.value })} placeholder="10.0.0.21:8006" />
        </div>
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
          <div className="field">
            <label>账号</label>
            <input className="input" value={draft.user} onChange={(e) => setDraft({ ...draft, user: e.target.value })} />
          </div>
          <div className="field">
            <label>密码</label>
            <input className="input" type="password" value={draft.pass} onChange={(e) => setDraft({ ...draft, pass: e.target.value })} />
          </div>
        </div>
        <p className="hint">存储路径等高级字段请在 Web 控制台或 opidcs host add 中配置。</p>
      </Modal>
    </>
  )
}
