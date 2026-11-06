import { useState } from 'react'
import type { PageProps } from '../App'
import { toRows } from '../api'
import { useLoad } from '../hooks/useLoad'
import PageHeader from '../components/PageHeader'
import DataTable from '../components/DataTable'
import { IconRefresh, IconSearch } from '../components/Icons'

export default function Logs({ api }: PageProps) {
  const [level, setLevel] = useState('')
  const [keyword, setKeyword] = useState('')
  const [query, setQuery] = useState({ level: '', keyword: '' })

  const data = useLoad<unknown>(
    () => api.get('/api/system/logger/detail', { limit: '100', level: query.level, keyword: query.keyword }),
    [api, query.level, query.keyword],
  )

  return (
    <>
      <PageHeader
        title="日志"
        desc="按级别与关键字检索主控日志"
        actions={
          <button className="btn" onClick={() => data.reload()} type="button">
            <IconRefresh size={15} /> 刷新
          </button>
        }
      />

      <div className="toolbar">
        <select className="select select--inline" value={level} onChange={(e) => setLevel(e.target.value)}>
          <option value="">全部级别</option>
          <option value="debug">DEBUG</option>
          <option value="info">INFO</option>
          <option value="warning">WARNING</option>
          <option value="error">ERROR</option>
        </select>
        <div style={{ position: 'relative' }}>
          <span style={{ position: 'absolute', left: 10, top: 9, color: 'var(--dim)' }}>
            <IconSearch size={15} />
          </span>
          <input
            className="input"
            style={{ width: 280, paddingLeft: 32 }}
            placeholder="关键字，回车搜索"
            value={keyword}
            onChange={(e) => setKeyword(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') setQuery({ level, keyword })
            }}
          />
        </div>
        <button className="btn btn--primary" type="button" onClick={() => setQuery({ level, keyword })}>
          查询
        </button>
      </div>

      <DataTable
        columns={[
          { key: 'time', label: '时间', width: '160px', render: (row) => String(row.time ?? row.created_at ?? '—') },
          { key: 'level', label: '级别', width: '88px' },
          { key: 'module', label: '模块', width: '110px' },
          { key: 'msg', label: '消息', render: (row) => String(row.msg ?? row.message ?? '') },
        ]}
        rows={toRows(data.data)}
        loading={data.loading}
        empty="没有匹配的日志"
      />
    </>
  )
}
