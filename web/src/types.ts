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
  /** 额度查询凭证：火山方舟等厂商用它读取套餐额度 */
  quota_credential?: QuotaCredentialView;
  endpoints: InstanceEndpoint[];
  /** 该接入点支持的上游模型名，模型引用它的前提 */
  models: string[];
  /** 是否放行指向 loopback / 内网 / 云元数据地址的上游 */
  allow_private_upstream: boolean;
  /** 上游要求会话粘性时使用的请求头名（内置端点会自带） */
  session_header?: string;
  /** 除 429 / 5xx 之外，额外触发候选切换的状态码 */
  failover_statuses: number[];
  /** 可选网络代理地址（含密码时仅回显脱敏后的值） */
  proxy_url?: string;
  /** 已配置的代理是否包含认证密码 */
  proxy_has_auth?: boolean;
  max_fails: number;
  cooldown_secs: number;
  timeout_secs: number;
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
  /** 额度查询凭证：新建时可选；编辑时整组提交才替换，留空表示保留原有凭证 */
  quota_credential?: {
    access_key_id: string;
    secret_access_key: string;
  };
  /** 仅自定义端点模式提交 */
  endpoints?: InstanceEndpoint[];
  /** 必填：该接入点支持的上游模型名 */
  models: string[];
  /**
   * 是否放行指向 loopback / 内网 / 云元数据地址的上游。
   *
   * 后端按「省略即关闭」处理，所以每次保存都要显式带上当前值；
   * 不带就会把已放行的内网上游悄悄关掉。
   */
  allow_private_upstream?: boolean;
  /** 会话头名；空字符串表示按内置端点的默认值处理 */
  session_header?: string;
  /**
   * 额外触发候选切换的状态码（400-599）。
   * 后端按「追加」处理：429 与 5xx 始终可切换，这里只补厂商特有的。
   */
  failover_statuses?: number[];
  /** 可选网络代理地址；编辑时省略表示保留原有配置，空字符串表示清除 */
  proxy_url?: string;
  max_fails?: number;
  cooldown_secs?: number;
  timeout_secs?: number;
}

/** 单个协议端点的连通性探测状态 */
export type EndpointTestStatus = 'ok' | 'reachable' | 'auth_error' | 'error';

/** 单个协议端点的连通性探测结果 */
export interface EndpointTestResult {
  protocol: 'openai' | 'anthropic';
  base_url: string;
  status: EndpointTestStatus;
  ok: boolean;
  http_status?: number;
  latency_ms: number;
  model_count?: number;
  message: string;
}

/** POST /admin/api/instances/{id}/test 与 /admin/api/instances/test 的响应 */
export interface InstanceTestResponse {
  ok: boolean;
  endpoints: EndpointTestResult[];
}

/** POST /admin/api/instances/test 测试未保存草稿时提交的内容 */
export interface InstanceTestPayload {
  instance_id?: number;
  preset?: string;
  api_key?: string;
  endpoints?: InstanceEndpoint[];
  allow_private_upstream: boolean;
  proxy_url?: string;
}

/** POST /admin/api/preset-models 返回的一个上游模型候选 */
export interface PresetModelCandidate {
  id: string;
  /** 上游声明的可用协议；为空表示上游没有提供该信息 */
  protocols?: ('openai' | 'anthropic')[];
}

/** POST /admin/api/preset-models 的响应 */
export interface PresetModelCandidates {
  models: PresetModelCandidate[];
  /** 预设自带的建议模型，查询失败时作为兜底 */
  suggested: string[];
  /** 上游列表的适用范围说明，或查询失败提示 */
  warning?: string;
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
  /** 该预设的额度查询声明；没有则表示厂商不提供额度接口 */
  quota?: PresetQuota;
}

/** 内置端点的额度查询声明 */
export interface PresetQuota {
  /** 额度接口的类型：opencode-go / minimax / ark */
  kind: string;
  /** 额度接口地址；ark 由网关自己寻址，没有该字段 */
  url?: string;
}

/** 额度查询凭证的只读视图：密钥只回显掩码 */
export interface QuotaCredentialView {
  access_key_id?: string;
  secret_hint?: string;
}

/** 套餐额度的一个窗口：5 小时、每周或每月 */
export interface QuotaWindow {
  /** 窗口标识：5h / weekly / monthly */
  id: string;
  /** 窗口的显示名 */
  label: string;
  /** 已用百分比，0 到 100 */
  used_pct: number;
  /** 窗口重置时间，RFC 3339；上游不返回时为空 */
  resets_at?: string;
  /** 窗口状态：ok / exhausted */
  status?: string;
}

/** GET /admin/api/quota 中单个接入点的额度 */
export interface QuotaReport {
  instance_id: number;
  instance: string;
  preset?: string;
  /** 该接入点的内置端点是否提供额度接口 */
  supported: boolean;
  windows: QuotaWindow[];
  /** 查询上游的时间，RFC 3339 */
  fetched_at?: string;
  /** 查询失败的原因 */
  error?: string;
}

/** GET /admin/api/quota 的响应 */
export interface QuotaReports {
  reports: QuotaReport[] | null;
}

/** 候选接入点的一个时间窗口 */
export interface TimeWindow {
  /** 起始时刻，24 小时制 HH:MM */
  start: string;
  /** 结束时刻，24 小时制 HH:MM；早于 start 表示跨天 */
  end: string;
  /** 窗口内生效的优先级；为空表示不覆盖优先级 */
  priority?: number | null;
  /** 仅在窗口内可用，窗口外该候选不参与路由 */
  only?: boolean;
}

/** 模型绑定的一个候选接入点 + 该接入点上的具体模型 */
export interface ModelInstance {
  instance: string;
  /** 该候选真正发给上游的模型名，必须是接入点 models 声明里的一项 */
  upstream: string;
  priority: number;
  /** 该候选的时间窗口 */
  windows?: TimeWindow[];
}

/** 模型支持的路由调度策略 */
export type RoutingStrategy = 'failover' | 'round_robin' | 'least_used' | 'sticky';

/** 当前时刻某个候选的路由状态 */
export interface EffectiveCandidate {
  instance: string;
  /** 该候选发给上游的模型名 */
  upstream: string;
  /** 当前生效的优先级 */
  priority: number;
  /** 配置里的基础优先级 */
  base_priority: number;
  /** 当前是否可以服务该模型 */
  active: boolean;
  /** 当前命中的时间窗口 */
  window?: string;
  /** 是否因套餐额度已满/告急被自动降级到备选 */
  quota_degraded?: boolean;
  /** 已知的最高套餐窗口已用百分比（0-100） */
  quota_used_pct?: number;
}

/** GET /admin/api/models 中的单个模型 */
/** 按协议覆盖的模型元数据。留空的字段表示继承通用值。 */
export interface ModelMetadataOverride {
  context_length?: number;
  max_output_tokens?: number;
  capabilities?: string[];
}

/** 模型对外声明的可选元数据，用于 /models 接口与控制台展示。 */
export interface ModelMetadata {
  display_name?: string;
  context_length?: number;
  max_output_tokens?: number;
  capabilities?: string[];
  /** 按协议覆盖，key 为 openai / anthropic。 */
  protocols?: Record<string, ModelMetadataOverride>;
}

export interface Model {
  id: number;
  name: string;
  strategy: RoutingStrategy;
  /** 自定义元数据；为空时回退到内置预设的官方参数。 */
  metadata?: ModelMetadata;
  instances: ModelInstance[];
  /** 时间窗口使用的时区 */
  timezone: string;
  /** 计算生效顺序时的服务端时间 */
  server_time: string;
  /** 当前生效的候选顺序 */
  effective_candidates?: EffectiveCandidate[];
  created_at: string;
  updated_at: string;
}

/** 新建 / 更新模型时提交的内容 */
export interface ModelPayload {
  name: string;
  strategy: string;
  metadata?: ModelMetadata;
  instances: ModelInstance[];
}

/** GET /admin/api/keys 中的网关 Key */
export interface GatewayKey {
  id: number;
  name: string;
  prefix: string;
  enabled: boolean;
  /** 完整 Key，仅在管理台列表回显；旧版本创建的 Key 没有该字段 */
  key?: string;
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
  cache_read_tokens: number;
  cache_write_tokens: number;
}

/** GET /admin/api/usage 中按模型聚合的用量 */
export interface UsageByModel {
  model: string;
  requests: number;
}

/** GET /admin/api/usage 中按网关 Key 聚合的用量 */
export interface UsageByKey {
  key_id: number;
  name: string;
  requests: number;
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
  /** 调用令牌名称；Key 已删除时为空 */
  key_name: string;
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
  cache_read_tokens: number;
  cache_write_tokens: number;
  usage_estimated: boolean;
  error_code: string;
}
