import { useState } from 'react'
import type { PageProps } from '../App'
import type { Row } from '../types'
import { toRows } from '../api'
import { useLoad } from '../hooks/useLoad'
import PageHeader from '../components/PageHeader'
import DataTable from '../components/DataTable'
import Drawer from '../components/Drawer'
import StatusPill from '../components/StatusPill'
import Modal from '../components/Modal'
import { IconRefresh } from '../components/Icons'

export default function Users({ api, notify }: PageProps) {
  const data = useLoad<unknown>(() => api.get('/api/users', { limit: '500' }), [api])
  const [selected, setSelected] = useState<Row | null>(null)
  const [target, setTarget] = useState<Row | null>(null)
  const [password, setPassword] = useState('')

  async function resetPassword() {
    if (!target) return
    if (password.length < 8) {
      notify('密码至少 8 位', 'error')
      return
    }
    try {
      await api.put(`/api/users/${String(target.id)}`, { password })
      notify('密码已重置', 'ok')
      setTarget(null)
      setPassword('')
    } catch (err) {
      notify(err instanceof Error ? err.message : '重置失败', 'error')
    }
  }

  return (
    <>
      <PageHeader
        title="用户"
        desc="账号、角色与配额概览"
        actions={
          <button className="btn" onClick={() => data.reload()} type="button">
            <IconRefresh size={15} /> 刷新
          </button>
        }
      />

      <DataTable
        columns={[
          { key: 'username', label: '用户名' },
          { key: 'email', label: '邮箱' },
          {
            key: 'is_admin',
            label: '角色',
            render: (row) => <span className="tag">{row.is_admin ? '管理员' : '用户'}</span>,
          },
          {
            key: 'status',
            label: '状态',
            render: (row) => <StatusPill value={row.status ?? (row.enable ? 'active' : 'inactive')} />,
          },
          {
            key: 'actions',
            label: '操作',
            render: (row) => (
              <button
                className="btn btn--sm"
                type="button"
                onClick={(e) => {
                  e.stopPropagation()
                  setTarget(row)
                }}
              >
                重置密码
              </button>
            ),
          },
        ]}
        rows={toRows(data.data)}
        loading={data.loading}
        onRow={setSelected}
        empty="暂无用户"
      />

      <Drawer open={!!selected} title="用户详情" row={selected} onClose={() => setSelected(null)} />

      <Modal
        open={!!target}
        title={`重置密码 · ${String(target?.username ?? '')}`}
        onClose={() => setTarget(null)}
        footer={
          <>
            <button className="btn" onClick={() => setTarget(null)} type="button">取消</button>
            <button className="btn btn--primary" onClick={resetPassword} type="button">确认重置</button>
          </>
        }
      >
        <div className="field">
          <label>新密码</label>
          <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
          <span className="hint">至少 8 位；重置后请通知用户立即修改</span>
        </div>
      </Modal>
    </>
  )
}
