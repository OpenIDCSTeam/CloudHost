import type { PageProps } from '../App'
import { toRows } from '../api'
import { useLoad } from '../hooks/useLoad'
import PageHeader from '../components/PageHeader'
import DataTable from '../components/DataTable'
import StatusPill from '../components/StatusPill'
import { IconRefresh } from '../components/Icons'

export default function Proxies({ api }: PageProps) {
  const data = useLoad<unknown>(() => api.get('/api/client/proxys/list'), [api])

  return (
    <>
      <PageHeader
        title="反向代理"
        desc="域名、上游与证书状态（Caddy 托管）"
        actions={
          <button className="btn" onClick={() => data.reload()} type="button">
            <IconRefresh size={15} /> 刷新
          </button>
        }
      />

      <DataTable
        columns={[
          { key: 'web_addr', label: '域名' },
          { key: 'lan_addr', label: '内网地址' },
          { key: 'lan_port', label: '上游端口' },
          { key: 'host', label: '主机' },
          { key: 'is_https', label: 'HTTPS', render: (row) => <StatusPill value={row.is_https ? 'active' : 'inactive'} /> },
          { key: 'web_tips', label: '备注' },
        ]}
        rows={toRows(data.data)}
        loading={data.loading}
        empty="还没有反向代理规则"
      />

      {data.error && <p style={{ color: 'var(--red)', marginTop: 12 }}>{data.error}</p>}
      <p className="hint" style={{ marginTop: 12 }}>
        域名需先解析到入口地址；证书由 Caddy 自动签发，80/443 必须可达。
      </p>
    </>
  )
}
