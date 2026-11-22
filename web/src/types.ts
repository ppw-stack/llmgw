/**
 * 管理台接口的数据结构，与 Go 端（internal/server）的 JSON 字段一一对应。
 *
 * 约定：后端 omitempty 的字段用可选属性表示；其余字段一律显式声明，
 * 避免用 any 绕过类型检查。
 */

/** GET /admin/api/me */
export interface Me {
  username: string;
  api_token: string;
}

/** 上游接入点的一个协议入口 */
export interface InstanceEndpoint {
  protocol: 'openai' | 'anthropic';
  base_url: string;
}

/** GET /admin/api/instances 中的单个上游接入点 */
export interface Instance {
  id: number;
  name: string;
  preset?: string;
  preset_name?: string;
  source: 'builtin' | 'custom';
  docs_url?: string;
  key_hint: string;
  endpoints: InstanceEndpoint[];
  max_fails: number;
  cooldown_secs: number;
  timeout_secs: number;
  models: string[];
  created_at: string;
  updated_at: string;
}

/** 新建 / 更新上游接入点时提交的内容 */
export interface InstancePayload {
  name: string;
  /** 仅内置端点模式提交，且创建后不可更改 */
  preset?: string;
  /** 新建必填；编辑时留空表示保留原有 Key */
  api_key?: string;
  /** 仅自定义端点模式提交 */
  endpoints?: InstanceEndpoint[];
  max_fails?: number;
  cooldown_secs?: number;
  timeout_secs?: number;
}

/** GET /admin/api/provider-presets 中的内置端点预设 */
export interface InstancePreset {
  id: string;
  family: string;
  family_name: string;
  variant_name: string;
  default_name: string;
  docs_url: string;
  openai_base_url: string;
  anthropic_base_url: string;
  models: string[];
}

/** 模型绑定的一个候选上游接入点 */
export interface ModelInstance {
  instance: string;
  priority: number;
  weight: number;
}

/** GET /admin/api/models 中的单个模型 */
export interface Model {
  id: number;
  name: string;
  upstream: string;
  strategy: 'failover';
  instances: ModelInstance[];
  created_at: string;
  updated_at: string;
}

/** 新建 / 更新模型时提交的内容 */
export interface ModelPayload {
  name: string;
  upstream: string;
  strategy: string;
  instances: ModelInstance[];
}

/** GET /admin/api/keys 中的网关 Key */
export interface GatewayKey {
  id: number;
  name: string;
  prefix: string;
  enabled: boolean;
  created_at: string;
  last_used_at?: string;
}

/** POST /admin/api/keys 的响应，明文 Key 只在创建时返回一次 */
export interface CreatedKey {
  id: number;
  name: string;
  key: string;
  prefix: string;
  enabled: boolean;
  created_at: string;
}

/** GET /admin/api/usage 中按天聚合的用量 */
export interface UsageDay {
  day: string;
  requests: number;
  prompt_tokens: number;
  completion_tokens: number;
  cost_usd: number;
}

/** GET /admin/api/usage 中按模型聚合的用量 */
export interface UsageByModel {
  model: string;
  requests: number;
  cost_usd: number;
}

/** GET /admin/api/usage 中按网关 Key 聚合的用量 */
export interface UsageByKey {
  key_id: number;
  name: string;
  requests: number;
  cost_usd: number;
}

/**
 * GET /admin/api/usage 的整体结构。
 *
 * 后端在统计区间内没有流量时返回 null 切片（而非空数组），故三个数组均为可空。
 */
export interface UsageReport {
  daily: UsageDay[] | null;
  by_model: UsageByModel[] | null;
  by_key: UsageByKey[] | null;
}

/** GET /admin/api/logs 中的一条请求日志 */
export interface LogEntry {
  id: number;
  ts: string;
  request_id: string;
  api_key_id: number;
  ingress_protocol: string;
  model: string;
  provider: string;
  endpoint: string;
  upstream_model: string;
  status: number;
  latency_ms: number;
  ttft_ms: number;
  prompt_tokens: number;
  completion_tokens: number;
  cost_usd: number;
  usage_estimated: boolean;
  error_code: string;
}
