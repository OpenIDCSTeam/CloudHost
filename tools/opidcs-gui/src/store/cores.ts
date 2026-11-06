import { invoke } from '@tauri-apps/api/core'
import type { Core, CoresFile } from '../types'

const LOCAL_KEY = 'opidcs.cores'

function isTauri(): boolean {
  return typeof window !== 'undefined' && '__TAURI_INTERNALS__' in window
}

export async function loadCores(): Promise<CoresFile> {
  if (isTauri()) {
    try {
      const data = await invoke<CoresFile>('load_cores')
      if (data && Array.isArray(data.cores)) return data
    } catch (err) {
      console.error('加载主控配置失败', err)
    }
  }
  try {
    const raw = localStorage.getItem(LOCAL_KEY)
    if (raw) return JSON.parse(raw) as CoresFile
  } catch (err) {
    console.error('解析本地配置失败', err)
  }
  return { default: '', cores: [] }
}

export async function saveCores(data: CoresFile): Promise<void> {
  if (isTauri()) {
    try {
      await invoke('save_cores', { data })
      return
    } catch (err) {
      console.error('保存主控配置失败', err)
    }
  }
  localStorage.setItem(LOCAL_KEY, JSON.stringify(data))
}

export function upsertCore(file: CoresFile, core: Core): CoresFile {
  const cores = [...file.cores]
  const index = cores.findIndex((c) => c.name === core.name)
  if (index >= 0) cores[index] = core
  else cores.push(core)
  return { default: file.default || core.name, cores }
}

export function removeCore(file: CoresFile, name: string): CoresFile {
  const cores = file.cores.filter((c) => c.name !== name)
  return { default: file.default === name ? cores[0]?.name ?? '' : file.default, cores }
}
