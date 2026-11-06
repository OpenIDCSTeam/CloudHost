type Props = { value: unknown }

const OK = new Set(['active', 'running', 'online', 'success', 'true', 'enabled', 'up'])
const WARN = new Set(['creating', 'pending', 'running_task', 'warning', 'maintenance'])
const BAD = new Set(['inactive', 'offline', 'failed', 'error', 'stopped', 'disabled', 'down'])

export default function StatusPill({ value }: Props) {
  const raw = String(value ?? '').toLowerCase()
  const cls = OK.has(raw) ? 'tag tag--on' : BAD.has(raw) ? 'tag tag--off' : WARN.has(raw) ? 'tag tag--warn' : 'tag'
  const labelMap: Record<string, string> = {
    active: '在线',
    inactive: '离线',
    running: '运行中',
    stopped: '已停止',
    success: '成功',
    failed: '失败',
    pending: '等待',
    creating: '创建中',
    canceled: '已取消',
  }
  return <span className={cls}>{labelMap[raw] || String(value ?? '—')}</span>
}
