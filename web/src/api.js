// 唯一的 HTTP 封装：超时、网络错误与错误 envelope 的统一处理。
const TIMEOUT_MS = 15000

export class ApiError extends Error {
  constructor(message, code) {
    super(message)
    this.code = code
  }
}

async function request(path, options = {}) {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), TIMEOUT_MS)
  let resp
  try {
    resp = await fetch(path, {
      ...options,
      signal: controller.signal,
      headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
    })
  } catch (e) {
    if (e.name === 'AbortError') throw new ApiError('请求超时，请检查网络后重试', 'timeout')
    throw new ApiError('无法连接后端服务，请确认服务已启动', 'network')
  } finally {
    clearTimeout(timer)
  }

  let body = null
  try {
    body = await resp.json()
  } catch {
    body = null
  }
  if (!resp.ok) {
    const err = body && body.error
    throw new ApiError(
      err ? err.message : `请求失败（HTTP ${resp.status}）`,
      err ? err.code : `http_${resp.status}`,
    )
  }
  return body
}

export const startScan = (payload) =>
  request('/api/scan', { method: 'POST', body: JSON.stringify(payload) })
export const stopScan = () => request('/api/scan/stop', { method: 'POST' })
export const setIP = (payload) =>
  request('/api/set-ip', { method: 'POST', body: JSON.stringify(payload) })
export const reboot = (payload) =>
  request('/api/reboot', { method: 'POST', body: JSON.stringify(payload) })

// 常驻 SSE 流：状态由服务端推送（变化才推），断线交给浏览器自动重连，
// 单条坏消息静默忽略；返回关闭函数，调用方在组件卸载时调用。
export function subscribeScan(onState) {
  const es = new EventSource('/api/events')
  es.addEventListener('scan', (ev) => {
    try {
      onState(JSON.parse(ev.data))
    } catch {
      /* 单条坏消息不能杀整条流 */
    }
  })
  es.onerror = () => {}
  return () => es.close()
}
