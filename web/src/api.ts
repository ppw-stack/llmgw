/**
 * 统一的 API 客户端与展示格式化函数。
 */

/** 调用管理台接口时可传的选项。 */
export interface ApiOptions {
  method?: string;
  body?: string;
  headers?: Record<string, string>;
}

/**
 * 从错误响应体中取出可展示的错误信息。
 *
 * 响应体可能不是 JSON（反向代理返回的 HTML、空响应体等），此时返回 null。
 */
function parseErrorMessage(body: unknown): string | null {
  if (typeof body !== 'object' || body === null || !('error' in body)) return null;
  const error = body.error;
  if (typeof error !== 'object' || error === null || !('message' in error)) return null;
  const message = error.message;
  return typeof message === 'string' && message !== '' ? message : null;
}

/**
 * 调用管理台接口。
 *
 * @param path 相对 `/admin/api` 的路径，例如 `/instances`
 * @param options 请求选项
 * @returns 解析后的 JSON；后端返回 204 时为 null
 */
export async function api<T>(path: string, options: ApiOptions = {}): Promise<T> {
  const res = await fetch('/admin/api' + path, {
    credentials: 'same-origin',
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...options.headers,
    },
  });

  if (res.status === 401 && path !== '/login') {
    window.dispatchEvent(new Event('llmgw-unauthorized'));
  }
  if (!res.ok) {
    // 错误体不一定是 JSON，解析失败时回退到 statusText。
    const body: unknown = await res.json().catch(() => null);
    throw new Error(parseErrorMessage(body) || res.statusText);
  }
  if (res.status === 204) return null as T;
  return (await res.json()) as T;
}

/** 格式化数字，空值显示为短横线。 */
export function formatNumber(n: number | null | undefined): string {
  if (n === null || n === undefined) return '-';
  return new Intl.NumberFormat('zh-CN').format(n);
}

/** 格式化美元金额。 */
export function formatUSD(n: number | null | undefined): string {
  if (!n) return '$0';
  if (n < 0.01) return '$' + n.toFixed(6);
  return '$' + n.toFixed(4);
}

/** 格式化时间，非法输入原样返回。 */
export function formatTime(value: string | null | undefined): string {
  if (!value) return '-';
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value;
  return d.toLocaleString('zh-CN', { hour12: false });
}

/** 格式化耗时（毫秒）。 */
export function formatMs(v: number | null | undefined): string {
  if (v === null || v === undefined) return '-';
  return v < 1000 ? `${v} ms` : `${(v / 1000).toFixed(1)} s`;
}
