import { useState } from 'react'
import type { Core, CoresFile } from '../types'
import { testCore } from '../api'
import { upsertCore, removeCore } from '../store/cores'
import Modal from './Modal'
import { IconPlus, IconTrash } from './Icons'

type Props = {
  file: CoresFile
  active: string
  online: boolean | null
  onSelect: (name: string) => void
  onSave: (file: CoresFile) => Promise<void>
  notify: (text: string, kind?: 'ok' | 'error' | 'info') => void
}

const empty: Core = { name: '', url: 'http://127.0.0.1:1881', token: '' }

export default function CoreSwitcher({ file, active, online, onSelect, onSave, notify }: Props) {
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState<Core>(empty)
  const [testing, setTesting] = useState(false)

  function openAdd() {
    setDraft(empty)
    setOpen(true)
  }

  function openEdit() {
    const current = file.cores.find((c) => c.name === active)
    setDraft(current ? { ...current } : empty)
    setOpen(true)
  }

  async function handleTest() {
    setTesting(true)
    try {
      await testCore(draft)
      notify('连接成功', 'ok')
    } catch (err) {
      notify(err instanceof Error ? err.message : '连接失败', 'error')
    } finally {
      setTesting(false)
    }
  }

  async function handleSave() {
    if (!draft.name.trim() || !draft.url.trim()) {
      notify('名称与地址不能为空', 'error')
      return
    }
    const next = upsertCore(file, { ...draft, name: draft.name.trim(), url: draft.url.trim().replace(/\/$/, '') })
    if (!next.default) next.default = draft.name.trim()
    await onSave(next)
    onSelect(draft.name.trim())
    setOpen(false)
    notify('已保存主控', 'ok')
  }

  async function handleDelete() {
    if (!active) return
    if (!window.confirm(`确认移除主控 ${active}？（不会影响主控本身）`)) return
    await onSave(removeCore(file, active))
    setOpen(false)
    notify('已移除主控', 'ok')
  }

  return (
    <>
      <span className={`conn${online === true ? ' is-online' : online === false ? ' is-offline' : ''}`}>
        <span className="conn__dot" />
        {online === true ? '在线' : online === false ? '离线' : '未连接'}
      </span>

      {file.cores.length > 0 && (
        <select className="select select--inline" value={active} onChange={(e) => onSelect(e.target.value)}>
          {file.cores.map((c) => (
            <option key={c.name} value={c.name}>
              {c.name}
            </option>
          ))}
        </select>
      )}

      <button className="btn btn--sm" type="button" onClick={openAdd} title="新增主控">
        <IconPlus size={14} /> 主控
      </button>
      {file.cores.length > 0 && (
        <button className="btn btn--sm btn--ghost" type="button" onClick={openEdit}>
          编辑
        </button>
      )}

      <Modal
        open={open}
        title={file.cores.some((c) => c.name === draft.name) ? '编辑主控' : '新增主控'}
        onClose={() => setOpen(false)}
        footer={
          <>
            {file.cores.some((c) => c.name === draft.name) && (
              <button className="btn btn--danger" type="button" onClick={handleDelete}>
                <IconTrash size={14} /> 移除
              </button>
            )}
            <button className="btn" type="button" onClick={() => setOpen(false)}>
              取消
            </button>
            <button className="btn" type="button" onClick={handleTest} disabled={testing}>
              {testing ? '测试中…' : '测试连接'}
            </button>
            <button className="btn btn--primary" type="button" onClick={handleSave}>
              保存
            </button>
          </>
        }
      >
        <div className="field">
          <label htmlFor="core-name">名称</label>
          <input
            id="core-name"
            className="input"
            value={draft.name}
            placeholder="local / prod"
            onChange={(e) => setDraft({ ...draft, name: e.target.value })}
          />
        </div>
        <div className="field">
          <label htmlFor="core-url">主控地址</label>
          <input
            id="core-url"
            className="input"
            value={draft.url}
            placeholder="https://idc.example.com"
            onChange={(e) => setDraft({ ...draft, url: e.target.value })}
          />
          <span className="hint">包含协议与端口，例如 http://127.0.0.1:1881</span>
        </div>
        <div className="field">
          <label htmlFor="core-token">Bearer Token</label>
          <input
            id="core-token"
            className="input"
            type="password"
            value={draft.token}
            placeholder="系统设置 → 令牌"
            onChange={(e) => setDraft({ ...draft, token: e.target.value })}
          />
          <span className="hint">保存在本机配置文件（0600），不会上传</span>
        </div>
      </Modal>
    </>
  )
}
