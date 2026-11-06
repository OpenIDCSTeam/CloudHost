import type { PageProps } from '../App'
import { toRows } from '../api'
import { useLoad } from '../hooks/useLoad'
import PageHeader from '../components/PageHeader'
import DataTable from '../components/DataTable'
import StatusPill from '../components/StatusPill'
import { IconRefresh } from '../components/Icons'

export default function Tasks({ api, notify }: PageProps) {
  const data = useLoad<unknown>(() => api.get('/api/tasks', { limit: '50' }), [api])

  async function cancel(row: Record<string, unknown>) {
    const id = String(row.id ?? row.task_id ?? '')
    if (!id) return
    try {
      await api.post(`/api/tasks/${encodeURIComponent(id)}/cancel`)
      notify('已请求取消', 'ok')
      data.reload()
    } catch (err) {
      notify(err instanceof Error ? err.message : '取消失败', 'error')
    }
  }

  return (
    <>
      <PageHeader
        title="任务"
        desc="异步任务的进度、状态与错误"
        actions={
          <button className="btn" onClick={() => data.reload()} type="button">
            <IconRefresh size={15} /> 刷新
          </button>
        }
      />

      <DataTable
        columns={[
          { key: 'id', label: 'ID', width: '90px' },
          { key: 'type', label: '类型' },
          { key: 'status', label: '状态', render: (row) => <StatusPill value={row.status} /> },
          { key: 'progress', label: '进度', render: (row) => `${row.progress ?? 0}%` },
          { key: 'hs_name', label: '主机' },
          { key: 'message', label: '信息' },
          {
            key: 'actions',
            label: '操作',
            render: (row) => (
              <button className="btn btn--sm" type="button" onClick={(e) => { e.stopPropagation(); cancel(row) }}>
                取消
              </button>
            ),
          },
        ]}
        rows={toRows(data.data)}
        loading={data.loading}
        empty="暂无任务"
      />
    </>
  )
}
