// 所有请求走这里，好处是 401 处理、错误文案、token 注入只有一份实现。

const TOKEN_KEY = 'ace_token'

export const getToken = () => localStorage.getItem(TOKEN_KEY) || ''

export function setToken(t) {
  if (t) localStorage.setItem(TOKEN_KEY, t)
  else localStorage.removeItem(TOKEN_KEY)
}

export class ApiError extends Error {
  constructor(message, status, data) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.data = data
  }
}

export async function api(path, opts = {}) {
  const headers = { ...(opts.headers || {}) }
  const token = getToken()
  if (token) headers.Authorization = `Bearer ${token}`

  // 只有字符串 body 才设 JSON；FormData 必须让浏览器自己带 multipart 边界
  if (typeof opts.body === 'string' && !headers['Content-Type']) {
    headers['Content-Type'] = 'application/json'
  }

  let res
  try {
    res = await fetch(path, { ...opts, headers })
  } catch {
    // fetch 抛错 = 网络层就没通（服务没起 / 断网），跟接口返回 4xx 不是一回事，
    // 分开报错，用户才知道该去重启服务而不是改输入
    throw new ApiError('连不上服务，请确认后端已启动', 0)
  }

  const text = await res.text()
  let data = null
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = { raw: text }
    }
  }

  if (!res.ok) {
    const reason = data?.reason || data?.error || `HTTP ${res.status}`
    throw new ApiError(reason, res.status, data)
  }
  return data
}
