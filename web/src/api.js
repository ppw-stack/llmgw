export async function api(path, options = {}) {
  const res = await fetch('/admin/api' + path, {
    credentials: 'same-origin',
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...(options.headers || {}),
    },
  })

  if (res.status === 401 && path !== '/login') {
    window.dispatchEvent(new Event('llmgw-unauthorized'))
  }
  if (!res.ok) {
    let message = res.statusText
    try {
      const body = await res.json()
      message = body?.error?.message || message
    } catch {
      /* non-JSON error body */
    }
    throw new Error(message)
  }
  if (res.status === 204) return null
  return res.json()
}

export function formatNumber(n) {
  if (n === null || n === undefined) return '-'
  return new Intl.NumberFormat('zh-CN').format(n)
}

export function formatUSD(n) {
  if (!n) return '$0'
  if (n < 0.01) return '$' + n.toFixed(6)
  return '$' + n.toFixed(4)
}

export function formatTime(value) {
  if (!value) return '-'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  return d.toLocaleString('zh-CN', { hour12: false })
}

export function formatMs(v) {
  if (v === null || v === undefined) return '-'
  return v < 1000 ? `${v} ms` : `${(v / 1000).toFixed(1)} s`
}
