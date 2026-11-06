import type { Row } from '../types'
import { IconClose } from './Icons'

type Props = {
  open: boolean
  title: string
  row: Row | null
  onClose: () => void
}

export default function Drawer({ open, title, row, onClose }: Props) {
  if (!open || !row) return null
  const entries = Object.entries(row).filter(([key]) => key !== 'config')
  return (
    <aside className="drawer">
      <div className="drawer__head">
        <h3>{title}</h3>
        <button className="icon-btn" style={{ marginLeft: 'auto' }} onClick={onClose} aria-label="关闭">
          <IconClose size={16} />
        </button>
      </div>
      <div className="drawer__body">
        <dl className="kv">
          {entries.map(([key, value]) => (
            <div key={key} style={{ display: 'contents' }}>
              <dt>{key}</dt>
              <dd>{format(value)}</dd>
            </div>
          ))}
        </dl>
        {typeof row.config === 'object' && row.config !== null && (
          <>
            <p className="dim" style={{ margin: '18px 0 8px', fontSize: 12 }}>config</p>
            <pre
              className="mono"
              style={{
                margin: 0,
                padding: 14,
                borderRadius: 12,
                background: 'var(--panel-2)',
                overflow: 'auto',
                maxHeight: 320,
              }}
            >
              {JSON.stringify(row.config, null, 2)}
            </pre>
          </>
        )}
      </div>
    </aside>
  )
}

function format(value: unknown): string {
  if (value === undefined || value === null || value === '') return '—'
  if (typeof value === 'boolean') return value ? '是' : '否'
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}
