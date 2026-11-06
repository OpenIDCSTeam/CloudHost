import { useMemo, useState } from 'react'
import type { Column, Row } from '../types'
import { IconSearch } from './Icons'

type Props = {
  columns: Column[]
  rows: Row[]
  loading?: boolean
  empty?: string
  searchable?: boolean
  onRow?: (row: Row) => void
  rowKey?: (row: Row, index: number) => string
}

export default function DataTable({ columns, rows, loading, empty, searchable = true, onRow, rowKey }: Props) {
  const [keyword, setKeyword] = useState('')

  const filtered = useMemo(() => {
    if (!keyword.trim()) return rows
    const kw = keyword.trim().toLowerCase()
    return rows.filter((row) =>
      columns.some((col) => String(row[col.key] ?? '').toLowerCase().includes(kw)),
    )
  }, [rows, keyword, columns])

  return (
    <div className="panel">
      {searchable && (
        <div className="panel__head">
          <span className="panel__title">共 {rows.length} 条</span>
          <div className="panel__tools">
            <div style={{ position: 'relative' }}>
              <span style={{ position: 'absolute', left: 9, top: 8, color: 'var(--dim)' }}>
                <IconSearch size={15} />
              </span>
              <input
                className="input"
                style={{ paddingLeft: 30, width: 220 }}
                placeholder="搜索当前列表"
                value={keyword}
                onChange={(e) => setKeyword(e.target.value)}
              />
            </div>
          </div>
        </div>
      )}

      {loading ? (
        <div className="loading">
          <span className="spin" /> 正在加载…
        </div>
      ) : filtered.length === 0 ? (
        <div className="empty">
          <strong>{searchable && keyword ? '没有匹配的结果' : '暂无数据'}</strong>
          {empty || '换一个主控或稍后刷新试试'}
        </div>
      ) : (
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                {columns.map((col) => (
                  <th key={col.key} style={col.width ? { width: col.width } : undefined}>
                    {col.label}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {filtered.map((row, index) => (
                <tr
                  key={rowKey ? rowKey(row, index) : String(row.uuid ?? row.name ?? index)}
                  onClick={() => onRow?.(row)}
                >
                  {columns.map((col) => (
                    <td key={col.key}>
                      {col.render ? col.render(row) : formatCell(row[col.key])}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function formatCell(value: unknown): string {
  if (value === undefined || value === null || value === '') return '—'
  if (typeof value === 'boolean') return value ? '是' : '否'
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}
