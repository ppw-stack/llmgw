<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue';
import { api } from '../api';
import AppIcon from '../components/AppIcon.vue';
import BaseModal from '../components/BaseModal.vue';
import CopyButton from '../components/CopyButton.vue';
import type {
  Instance,
  InstanceEndpoint,
  InstancePayload,
  InstancePreset,
  Model,
  PresetModelCandidate,
  PresetModelCandidates,
  QuotaReport,
  QuotaReports,
  QuotaWindow,
} from '../types';
import { confirmAction } from '../utils/confirm';
import { errorMessage } from '../utils/error';
import { showToast } from '../utils/toast';

type InstanceMode = 'builtin' | 'custom';

/** 额度轮询间隔与倒计时刷新间隔。 */
const QUOTA_POLL_MS = 60_000;
const QUOTA_TICK_MS = 30_000;

/** 表单里的一行端点。 */
interface EndpointRow {
  protocol: InstanceEndpoint['protocol'];
  base_url: string;
}

/** 新增 / 编辑上游接入点的表单状态。 */
interface InstanceForm {
  id: number | null;
  mode: InstanceMode;
  name: string;
  preset: string;
  apiKey: string;
  keyHint: string;
  quotaAccessKeyId: string;
  /** 存量的 Access Key ID：留空 Secret 时靠它判断用户是否改过这对凭证。 */
  quotaAccessKeyIdStored: string;
  quotaSecretKey: string;
  quotaSecretHint: string;
  endpoints: EndpointRow[];
  modelsText: string;
  /** 是否放行指向 loopback / 内网 / 云元数据地址的上游 */
  allowPrivateUpstream: boolean;
  /** 上游要求会话粘性时使用的请求头名；留空表示用内置端点的默认值 */
  sessionHeader: string;
  /** 额外触发候选切换的状态码，逗号分隔 */
  failoverStatuses: string;
  /** 可选网络代理地址；编辑含密码的存量代理时留空表示保留原值 */
  proxyUrl: string;
  /** 存量代理的脱敏展示值 */
  proxyUrlHint: string;
  /** 存量代理是否包含密码 */
  proxyHasAuth: boolean;
  /** 编辑含密码的存量代理时，勾选表示清空已有代理 */
  clearProxy: boolean;
  maxFails: string;
  cooldownSecs: string;
  timeoutSecs: string;
}

const instances = ref<Instance[]>([]);
const models = ref<Model[]>([]);
const presets = ref<InstancePreset[]>([]);
const loading = ref(true);
const error = ref('');
const editing = ref<InstanceForm | null>(null);
const saving = ref(false);
const showApiKey = ref(false);
const candidates = ref<PresetModelCandidate[]>([]);
const candidateSearch = ref('');
const candidatesLoading = ref(false);
const candidatesWarning = ref('');
const candidatesError = ref('');

/** 上游套餐额度：按接入点 id 索引。 */
const quotaReports = ref<QuotaReport[]>([]);
const quotaError = ref('');
/** 当前时间，用于倒计时。 */
const now = ref(Date.now());

let quotaTimer: number | undefined;
let tickTimer: number | undefined;

/** 当前有哪些对外模型引用了这个接入点。信息来自 /models，不额外落库。 */
function usedBy(instanceName: string): string[] {
  return models.value
    .filter((m) => m.instances.some((c) => c.instance === instanceName))
    .map((m) => m.name);
}

/** 按接入点 id 索引额度报告。 */
const quotaByID = computed<Record<number, QuotaReport>>(() => {
  const map: Record<number, QuotaReport> = {};
  for (const report of quotaReports.value) {
    map[report.instance_id] = report;
  }
  return map;
});

/** 该接入点的额度窗口；没有额度接口的接入点返回空列表。 */
function quotaWindows(inst: Instance): QuotaWindow[] {
  const report = quotaByID.value[inst.id];
  if (!report?.supported) {
    return [];
  }
  return report.windows;
}

/** 该接入点的额度查询失败原因。 */
function quotaMessage(inst: Instance): string {
  const report = quotaByID.value[inst.id];
  if (!report?.supported) {
    return '';
  }
  return report.error ?? '';
}

/** 进度条宽度，限制在 0 到 100 之间。 */
function quotaWidth(pct: number): string {
  const value = Math.min(Math.max(pct, 0), 100);
  return `${value}%`;
}

/** 进度条配色：七成以下正常，九成以上告急。 */
function quotaLevel(pct: number): string {
  if (pct >= 90) {
    return 'danger';
  }
  if (pct >= 70) {
    return 'warn';
  }
  return 'ok';
}

/** 已用百分比。 */
function quotaPercent(pct: number): string {
  return `${Math.round(pct)}%`;
}

/** 距窗口重置还有多久；上游没给重置时间时显示短横线。 */
function quotaCountdown(resetsAt?: string): string {
  if (!resetsAt) {
    return '-';
  }
  const target = new Date(resetsAt).getTime();
  if (Number.isNaN(target)) {
    return '-';
  }
  const diff = target - now.value;
  if (diff <= 0) {
    return '即将重置';
  }
  const minutes = Math.floor(diff / 60_000);
  const days = Math.floor(minutes / 1440);
  const hours = Math.floor((minutes % 1440) / 60);
  if (days > 0) {
    return `${days} 天 ${hours} 小时`;
  }
  if (hours > 0) {
    return `${hours} 小时 ${minutes % 60} 分`;
  }
  return `${minutes % 60} 分`;
}

function presetLabel(p: InstancePreset): string {
  return p.variant_name ? `${p.family_name}（${p.variant_name}）` : p.family_name;
}

function findPreset(id: string): InstancePreset | null {
  return presets.value.find((p) => p.id === id) ?? null;
}

const selectedPreset = computed<InstancePreset | null>(() => {
  const form = editing.value;
  if (form?.mode !== 'builtin') {
    return null;
  }
  return findPreset(form.preset);
});

/** 该预设需要单独配置额度查询凭证：火山方舟读套餐额度要用 Access Key 对。 */
const needsQuotaCredential = computed<boolean>(() => selectedPreset.value?.quota?.kind === 'ark');

/** 内置模式下只读展示的端点：优先取预设自带的地址。 */
const builtinEndpoints = computed<InstanceEndpoint[]>(() => {
  const form = editing.value;
  if (form?.mode !== 'builtin') {
    return [];
  }
  const preset = selectedPreset.value;
  if (!preset) {
    return form.endpoints;
  }
  const list: InstanceEndpoint[] = [];
  if (preset.openai_base_url) {
    list.push({ protocol: 'openai', base_url: preset.openai_base_url });
  }
  if (preset.anthropic_base_url) {
    list.push({ protocol: 'anthropic', base_url: preset.anthropic_base_url });
  }
  return list;
});

/** 当前已填写的模型数量。 */
const declaredModelCount = computed(() =>
  editing.value ? modelLines(editing.value.modelsText).length : 0,
);

/** 按搜索关键字过滤后的候选模型列表。 */
const filteredCandidates = computed(() => {
  const q = candidateSearch.value.trim().toLowerCase();
  if (!q) {
    return candidates.value;
  }
  return candidates.value.filter((c) => c.id.toLowerCase().includes(q));
});

/** 高级容错与会话设置中非默认配置项数量，用于折叠区摘要提示。 */
const advancedCustomCount = computed(() => {
  const f = editing.value;
  if (!f) {
    return 0;
  }
  let count = 0;
  if (f.maxFails.trim() !== '' && f.maxFails.trim() !== '3' && f.maxFails.trim() !== '0') {
    count += 1;
  }
  if (
    f.cooldownSecs.trim() !== '' &&
    f.cooldownSecs.trim() !== '30' &&
    f.cooldownSecs.trim() !== '0'
  ) {
    count += 1;
  }
  if (f.timeoutSecs.trim() !== '' && f.timeoutSecs.trim() !== '0') {
    count += 1;
  }
  if (f.sessionHeader.trim() !== '') {
    count += 1;
  }
  if (f.failoverStatuses.trim() !== '') {
    count += 1;
  }
  if (f.proxyUrl.trim() !== '' || (f.proxyHasAuth && !f.clearProxy)) {
    count += 1;
  }
  return count;
});

function newForm(): InstanceForm {
  const first = presets.value[0];
  return {
    id: null,
    mode: presets.value.length ? 'builtin' : 'custom',
    name: first ? first.default_name : '',
    preset: first ? first.id : '',
    apiKey: '',
    keyHint: '',
    quotaAccessKeyId: '',
    quotaAccessKeyIdStored: '',
    quotaSecretKey: '',
    quotaSecretHint: '',
    endpoints: [{ protocol: 'openai', base_url: '' }],
    modelsText: first ? first.models.join('\n') : '',
    allowPrivateUpstream: false,
    sessionHeader: '',
    failoverStatuses: '',
    proxyUrl: '',
    proxyUrlHint: '',
    proxyHasAuth: false,
    clearProxy: false,
    maxFails: '3',
    cooldownSecs: '30',
    timeoutSecs: '0',
  };
}

function applyPreset(form: InstanceForm, id: string, overwriteModels: boolean) {
  const prevPreset = findPreset(form.preset);
  form.preset = id;
  const preset = findPreset(id);
  if (!preset) {
    return;
  }
  if (!form.name.trim() || form.name.trim() === prevPreset?.default_name) {
    form.name = preset.default_name;
  }
  if (overwriteModels || !form.modelsText.trim()) {
    form.modelsText = preset.models.join('\n');
  }
}

function choosePreset(id: string) {
  const form = editing.value;
  if (!form) {
    return;
  }
  resetCandidates();
  applyPreset(form, id, true);
}

function onPresetChange(event: Event) {
  const target = event.target;
  if (!(target instanceof HTMLSelectElement)) {
    return;
  }
  choosePreset(target.value);
}

function setMode(mode: InstanceMode) {
  const form = editing.value;
  if (!form) {
    return;
  }
  form.mode = mode;
  resetCandidates();
  if (mode === 'builtin') {
    applyPreset(form, form.preset || presets.value[0]?.id || '', true);
    // 内置预设的地址是公网厂商地址，放行内网对它没有意义；切回内置时
    // 顺手清掉，免得留下一个看不见的开关。
    form.allowPrivateUpstream = false;
  }
}

function addEndpoint(form: InstanceForm) {
  // 目前只有 openai / anthropic 两种协议，每种仅能配置一个地址。
  if (form.endpoints.length >= 2) {
    return;
  }
  const used = new Set(form.endpoints.map((ep) => ep.protocol));
  const nextProtocol: InstanceEndpoint['protocol'] = used.has('openai') ? 'anthropic' : 'openai';
  form.endpoints.push({ protocol: nextProtocol, base_url: '' });
}

function removeEndpoint(form: InstanceForm, index: number) {
  if (form.endpoints.length <= 1) {
    return;
  }
  form.endpoints.splice(index, 1);
}

/** 把多行文本切成非空的模型名列表。 */
function modelLines(text: string): string[] {
  return text
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean);
}

/** 清空上一次查询留下的候选模型。 */
function resetCandidates() {
  candidates.value = [];
  candidateSearch.value = '';
  candidatesLoading.value = false;
  candidatesWarning.value = '';
  candidatesError.value = '';
}

/** 查询内置端点上游的模型列表，作为待选建议。 */
async function queryCandidates() {
  const form = editing.value;
  if (form?.mode !== 'builtin' || form.id !== null) {
    return;
  }

  const apiKey = form.apiKey.trim();
  if (!apiKey) {
    candidatesError.value = '请先填写上游 API Key';
    return;
  }

  candidatesLoading.value = true;
  candidatesWarning.value = '';
  candidatesError.value = '';
  try {
    const reqBody: { preset: string; api_key: string; proxy_url?: string } = {
      preset: form.preset,
      api_key: apiKey,
    };
    const proxyUrl = form.proxyUrl.trim();
    if (proxyUrl) {
      reqBody.proxy_url = proxyUrl;
    }
    const result = await api<PresetModelCandidates>('/preset-models', {
      method: 'POST',
      body: JSON.stringify(reqBody),
    });
    candidates.value = result.models;
    candidateSearch.value = '';
    candidatesWarning.value = result.warning ?? '';
    if (!result.models.length && !result.warning) {
      candidatesError.value = '上游没有返回可用的聊天模型';
    }
  } catch (e) {
    candidates.value = [];
    candidatesError.value = errorMessage(e);
  } finally {
    candidatesLoading.value = false;
  }
}

/** 判断一个候选是否已经写进模型文本框。 */
function candidateAdded(id: string): boolean {
  const form = editing.value;
  if (!form) {
    return false;
  }
  return modelLines(form.modelsText).includes(id);
}

/** 点击候选时在模型文本框里添加或移除该模型。 */
function toggleCandidate(id: string) {
  const form = editing.value;
  if (!form) {
    return;
  }
  const current = modelLines(form.modelsText);
  const next = current.includes(id) ? current.filter((name) => name !== id) : [...current, id];
  form.modelsText = next.join('\n');
}

/** 把当前筛选结果里尚未添加的模型一次性写入文本框。 */
function addAllCandidates() {
  const form = editing.value;
  if (!form) {
    return;
  }
  const next = modelLines(form.modelsText);
  const seen = new Set(next);
  for (const candidate of filteredCandidates.value) {
    if (seen.has(candidate.id)) {
      continue;
    }
    seen.add(candidate.id);
    next.push(candidate.id);
  }
  form.modelsText = next.join('\n');
}

/** 解析表单里的非负整数，非法时返回 null。 */
function parseCount(raw: string): number | null {
  const text = raw.trim();
  if (text === '') {
    return null;
  }
  const value = Number(text);
  if (!Number.isInteger(value) || value < 0) {
    return null;
  }
  return value;
}

/**
 * 解析「额外可切换状态码」。空输入返回空数组（合法）；
 * 出现 400-599 之外的整数时返回 null，由调用方提示错误。
 */
function parseStatusCodes(raw: string): number[] | null {
  const out: number[] = [];
  for (const part of raw.split(/[,，\s]+/)) {
    const text = part.trim();
    if (!text) {
      continue;
    }
    const value = Number(text);
    if (!Number.isInteger(value) || value < 400 || value > 599) {
      return null;
    }
    if (!out.includes(value)) {
      out.push(value);
    }
  }
  return out.sort((a, b) => a - b);
}

/** 收集自定义模式下的端点列表，非法时写好错误信息并返回 null。 */
function collectEndpoints(form: InstanceForm): InstanceEndpoint[] | null {
  const list: InstanceEndpoint[] = [];
  const seen = new Set<string>();
  for (const row of form.endpoints) {
    const base = row.base_url.trim();
    if (!base) {
      error.value = '请填写每个端点的 base URL，或删掉多余的行';
      return null;
    }
    if (seen.has(row.protocol)) {
      error.value = `协议 ${row.protocol} 重复了，同一个接入点每种协议只能有一个地址`;
      return null;
    }
    seen.add(row.protocol);
    list.push({ protocol: row.protocol, base_url: base });
  }
  if (!list.length) {
    error.value = '至少需要一个端点';
    return null;
  }
  return list;
}

async function loadPresets() {
  try {
    presets.value = await api<InstancePreset[]>('/provider-presets');
  } catch {
    presets.value = [];
  }
}

/** 拉取各接入点的套餐额度。额度查询失败不影响接入点列表。 */
async function loadQuota() {
  try {
    const res = await api<QuotaReports>('/quota');
    quotaReports.value = res.reports ?? [];
    quotaError.value = '';
  } catch (e) {
    quotaError.value = errorMessage(e);
  }
}

async function load() {
  loading.value = true;
  error.value = '';
  try {
    const [instanceList, modelList] = await Promise.all([
      api<Instance[]>('/instances'),
      api<Model[]>('/models'),
    ]);
    instances.value = instanceList;
    models.value = modelList;
  } catch (e) {
    error.value = errorMessage(e);
  } finally {
    loading.value = false;
  }
}

function openCreate() {
  error.value = '';
  showApiKey.value = false;
  resetCandidates();
  editing.value = newForm();
}

function openEdit(inst: Instance) {
  error.value = '';
  showApiKey.value = false;
  resetCandidates();
  editing.value = {
    id: inst.id,
    mode: inst.source === 'builtin' ? 'builtin' : 'custom',
    name: inst.name,
    preset: inst.preset ?? '',
    apiKey: '',
    keyHint: inst.key_hint,
    quotaAccessKeyId: inst.quota_credential?.access_key_id ?? '',
    quotaAccessKeyIdStored: inst.quota_credential?.access_key_id ?? '',
    quotaSecretKey: '',
    quotaSecretHint: inst.quota_credential?.secret_hint ?? '',
    endpoints: inst.endpoints.map((ep) => ({
      protocol: ep.protocol,
      base_url: ep.base_url,
    })),
    modelsText: inst.models.join('\n'),
    allowPrivateUpstream: inst.allow_private_upstream,
    sessionHeader: inst.session_header ?? '',
    failoverStatuses: inst.failover_statuses.join(', '),
    proxyUrl: inst.proxy_has_auth ? '' : (inst.proxy_url ?? ''),
    proxyUrlHint: inst.proxy_url ?? '',
    proxyHasAuth: Boolean(inst.proxy_has_auth),
    clearProxy: false,
    maxFails: String(inst.max_fails),
    cooldownSecs: String(inst.cooldown_secs),
    timeoutSecs: String(inst.timeout_secs),
  };
}

function closeModal() {
  editing.value = null;
  resetCandidates();
}

async function save() {
  const f = editing.value;
  if (!f) {
    return;
  }

  const name = f.name.trim();
  if (!name) {
    error.value = '请填写接入点名称';
    return;
  }
  if (f.id === null && !f.apiKey.trim()) {
    error.value = '请填写上游 API Key';
    return;
  }

  const supported = modelLines(f.modelsText);
  if (!supported.length) {
    error.value = '请填写该接入点支持的上游模型名（每行一个）';
    return;
  }

  const maxFails = parseCount(f.maxFails);
  const cooldownSecs = parseCount(f.cooldownSecs);
  const timeoutSecs = parseCount(f.timeoutSecs);
  if (maxFails === null || cooldownSecs === null || timeoutSecs === null) {
    error.value = '熔断阈值、冷却时间与超时都必须是 0 或正整数';
    return;
  }
  const failoverStatuses = parseStatusCodes(f.failoverStatuses);
  if (failoverStatuses === null) {
    error.value = '额外可切换状态码必须是 400-599 的整数，用逗号分隔';
    return;
  }

  const payload: InstancePayload = { name, models: supported };
  if (f.mode === 'builtin') {
    // preset 一旦创建不可更改：编辑已有接入点时不提交该字段。
    if (f.id === null) {
      payload.preset = f.preset;
    }
  } else {
    const endpoints = collectEndpoints(f);
    if (!endpoints) {
      return;
    }
    payload.endpoints = endpoints;
  }
  if (f.id === null || f.apiKey.trim()) {
    payload.api_key = f.apiKey.trim();
  }

  const quotaAccessKeyId = f.quotaAccessKeyId.trim();
  const quotaSecretKey = f.quotaSecretKey.trim();
  const storedAccessKeyId = f.quotaAccessKeyIdStored.trim();
  if (quotaSecretKey) {
    if (!quotaAccessKeyId) {
      error.value = '额度查询凭证需要同时填写 Access Key ID 与 Secret Access Key';
      return;
    }
    payload.quota_credential = {
      access_key_id: quotaAccessKeyId,
      secret_access_key: quotaSecretKey,
    };
  } else if (quotaAccessKeyId && quotaAccessKeyId !== storedAccessKeyId) {
    error.value =
      f.id === null
        ? '额度查询凭证需要同时填写 Access Key ID 与 Secret Access Key'
        : 'Access Key ID 改了，必须同时填写新的 Secret Access Key';
    return;
  }

  payload.max_fails = maxFails;
  payload.cooldown_secs = cooldownSecs;
  payload.timeout_secs = timeoutSecs;
  payload.allow_private_upstream = f.allowPrivateUpstream;
  payload.failover_statuses = failoverStatuses;
  payload.session_header = f.sessionHeader.trim();
  if (f.proxyHasAuth) {
    if (f.clearProxy) {
      payload.proxy_url = '';
    } else if (f.proxyUrl.trim()) {
      payload.proxy_url = f.proxyUrl.trim();
    }
  } else {
    payload.proxy_url = f.proxyUrl.trim();
  }

  saving.value = true;
  error.value = '';
  const isCreate = f.id === null;
  try {
    if (isCreate) {
      await api<Instance>('/instances', { method: 'POST', body: JSON.stringify(payload) });
    } else {
      await api<Instance>(`/instances/${f.id}`, {
        method: 'PUT',
        body: JSON.stringify(payload),
      });
    }
    closeModal();
    await load();
    void loadQuota();
    showToast(isCreate ? `已新增接入点「${name}」` : `已保存接入点「${name}」`);
  } catch (e) {
    error.value = errorMessage(e);
  } finally {
    saving.value = false;
  }
}

async function remove(inst: Instance) {
  const refs = usedBy(inst.name);
  const used = refs.length ? `它仍被模型 ${refs.join('、')} 引用，需要先从这些模型里移除。` : '';
  if (!confirmAction(`删除上游接入点「${inst.name}」？${used}`)) {
    return;
  }
  error.value = '';
  try {
    await api<null>(`/instances/${inst.id}`, { method: 'DELETE' });
    await load();
    showToast(`已删除接入点「${inst.name}」`);
  } catch (e) {
    error.value = errorMessage(e);
  }
}

/** 刷新列表与额度。 */
async function refresh() {
  await load();
  await loadQuota();
}

/** 页面重新可见时立刻刷新额度。 */
function onVisibilityChange() {
  if (document.visibilityState === 'visible') {
    void loadQuota();
  }
}

onMounted(async () => {
  await loadPresets();
  await load();
  await loadQuota();
  quotaTimer = window.setInterval(() => {
    // 页面在后台时不打扰上游。
    if (document.visibilityState === 'visible') {
      void loadQuota();
    }
  }, QUOTA_POLL_MS);
  tickTimer = window.setInterval(() => {
    now.value = Date.now();
  }, QUOTA_TICK_MS);
  document.addEventListener('visibilitychange', onVisibilityChange);
});

onUnmounted(() => {
  if (quotaTimer !== undefined) {
    window.clearInterval(quotaTimer);
  }
  if (tickTimer !== undefined) {
    window.clearInterval(tickTimer);
  }
  document.removeEventListener('visibilitychange', onVisibilityChange);
});
</script>

<template>
  <section class="page">
    <div class="page-head">
      <div class="page-title-group">
        <h2>上游接入点</h2>
        <p class="page-desc">
          管理物理上游（厂商端点 + API Key）与套餐额度，在「模型」页引用接入点组成主备链路。
        </p>
      </div>
      <div class="row">
        <button type="button" class="ghost" :disabled="loading" @click="refresh">
          <AppIcon name="refresh" :size="14" />
          <span>刷新</span>
        </button>
        <button type="button" class="primary" @click="openCreate">
          <AppIcon name="plus" :size="14" />
          <span>新增接入点</span>
        </button>
      </div>
    </div>

    <p v-if="error && !editing" class="error">{{ error }}</p>
    <p v-if="quotaError" class="alert warn">额度查询失败：{{ quotaError }}</p>

    <div v-if="loading" class="card stack">
      <div class="skeleton-line" style="width: 36%" />
      <div class="skeleton-line" style="width: 72%" />
      <div class="skeleton-line" style="width: 54%" />
    </div>

    <div v-else-if="!instances.length" class="card empty-state">
      <span class="empty-icon"><AppIcon name="instances" :size="18" /></span>
      <p>还没有配置上游接入点</p>
      <button type="button" class="primary sm" @click="openCreate">
        <AppIcon name="plus" :size="14" />
        <span>新增第一个接入点</span>
      </button>
    </div>

    <div v-for="inst in instances" :key="inst.id" class="card entity-card">
      <div class="entity-head">
        <div class="row">
          <h3>{{ inst.name }}</h3>
          <span class="badge" :class="{ builtin: inst.source === 'builtin' }">
            {{ inst.source === 'builtin' ? '内置端点' : '自定义' }}
          </span>
          <span v-if="inst.preset_name" class="badge">{{ inst.preset_name }}</span>
          <span v-if="inst.allow_private_upstream" class="badge danger">允许内网</span>
          <code v-if="inst.key_hint" class="small">{{ inst.key_hint }}</code>
        </div>
        <div class="row">
          <a
            v-if="inst.docs_url"
            class="btn ghost sm"
            :href="inst.docs_url"
            target="_blank"
            rel="noreferrer"
          >
            <AppIcon name="external" :size="13" />
            <span>文档</span>
          </a>
          <button type="button" class="ghost sm" @click="openEdit(inst)">
            <AppIcon name="edit" :size="13" />
            <span>编辑</span>
          </button>
          <button type="button" class="ghost danger sm" @click="remove(inst)">
            <AppIcon name="trash" :size="13" />
            <span>删除</span>
          </button>
        </div>
      </div>

      <div class="endpoint-list">
        <div
          v-for="ep in inst.endpoints"
          :key="`${ep.protocol}:${ep.base_url}`"
          class="endpoint-line"
        >
          <span class="badge" :class="ep.protocol">{{ ep.protocol }}</span>
          <code class="endpoint-url">{{ ep.base_url }}</code>
          <CopyButton :text="ep.base_url" icon-only toast-message="Base URL 已复制" />
        </div>
      </div>

      <div class="entity-meta-bar">
        <span>熔断：连续失败 {{ inst.max_fails }} 次冷却 {{ inst.cooldown_secs }}s</span>
        <span>超时：{{ inst.timeout_secs > 0 ? `${inst.timeout_secs}s` : '全局默认' }}</span>
        <span v-if="inst.session_header">
          会话头：<code>{{ inst.session_header }}</code>
        </span>
        <span v-if="inst.failover_statuses.length">
          额外切换：{{ inst.failover_statuses.join(', ') }}
        </span>
        <span v-if="inst.proxy_url">
          代理：<code>{{ inst.proxy_url }}</code>
        </span>
      </div>

      <div class="tag-list">
        <span v-if="!inst.models.length" class="muted small">未声明支持的模型</span>
        <span v-for="model in inst.models" :key="model" class="tag">{{ model }}</span>
      </div>

      <div v-if="quotaWindows(inst).length || quotaMessage(inst)" class="quota-box">
        <span class="quota-title">套餐额度</span>
        <p v-if="quotaMessage(inst)" class="small status-bad">{{ quotaMessage(inst) }}</p>
        <div v-for="w in quotaWindows(inst)" :key="w.id" class="quota-row">
          <span class="quota-label">{{ w.label }}</span>
          <span class="quota-track">
            <span
              class="quota-fill"
              :class="quotaLevel(w.used_pct)"
              :style="{ width: quotaWidth(w.used_pct) }"
            />
          </span>
          <span class="quota-value">
            已用 {{ quotaPercent(w.used_pct) }}
            <span class="muted">· {{ quotaCountdown(w.resets_at) }}</span>
          </span>
        </div>
      </div>

      <div v-if="usedBy(inst.name).length" class="row small muted">
        <span>已被模型引用：</span>
        <code v-for="mName in usedBy(inst.name)" :key="mName">{{ mName }}</code>
      </div>
    </div>

    <BaseModal
      v-if="editing"
      :title="editing.id === null ? '新增上游接入点' : '编辑上游接入点'"
      wide
      @close="closeModal"
    >
      <form id="instance-form" class="stack" @submit.prevent="save">
        <div v-if="editing.id === null" class="row space-between">
          <div class="segmented" role="group" aria-label="端点来源">
            <button
              type="button"
              :class="{ active: editing.mode === 'builtin' }"
              :disabled="!presets.length"
              @click="setMode('builtin')"
            >
              内置端点预设
            </button>
            <button
              type="button"
              :class="{ active: editing.mode === 'custom' }"
              @click="setMode('custom')"
            >
              自定义端点
            </button>
          </div>
          <span class="muted small">创建后来源不可切换</span>
        </div>
        <p v-else class="field-hint">
          当前为{{
            editing.mode === 'builtin' ? '内置端点预设' : '自定义端点'
          }}，创建后不可切换来源。
        </p>

        <!-- 1. 内置预设前置：先选厂商，自动带出名称与建议模型 -->
        <div v-if="editing.mode === 'builtin'" class="form-section">
          <div class="field">
            <label for="instance-preset">内置端点预设</label>
            <select
              id="instance-preset"
              :value="editing.preset"
              :disabled="editing.id !== null"
              @change="onPresetChange"
            >
              <option v-for="preset in presets" :key="preset.id" :value="preset.id">
                {{ presetLabel(preset) }}
              </option>
            </select>
            <p v-if="selectedPreset" class="field-hint">
              Base URL 由程序内置维护，随版本升级自动更新。
              <a :href="selectedPreset.docs_url" target="_blank" rel="noreferrer">查看官方文档</a>
            </p>
          </div>

          <div class="endpoint-list">
            <span class="field-caption">预设协议地址（只读）</span>
            <div v-for="ep in builtinEndpoints" :key="ep.protocol" class="endpoint-line">
              <span class="badge" :class="ep.protocol">{{ ep.protocol }}</span>
              <code class="endpoint-url">{{ ep.base_url }}</code>
            </div>
          </div>
        </div>

        <!-- 2. 基础信息与凭证 -->
        <div class="form-grid">
          <div class="field">
            <label for="instance-name">接入点名称</label>
            <input id="instance-name" v-model="editing.name" placeholder="例如 minimax" />
          </div>

          <div class="field">
            <label for="instance-api-key">
              上游 API Key
              <span v-if="editing.id !== null && editing.keyHint" class="muted">
                （留空保留 {{ editing.keyHint }}）
              </span>
            </label>
            <div class="input-wrap">
              <input
                id="instance-api-key"
                v-model="editing.apiKey"
                :type="showApiKey ? 'text' : 'password'"
                autocomplete="off"
                :placeholder="editing.id === null ? 'sk-...' : '留空则保持不变'"
              />
              <span class="input-suffix">
                <button
                  type="button"
                  class="icon-btn"
                  :aria-label="showApiKey ? '隐藏 API Key' : '显示 API Key'"
                  @click="showApiKey = !showApiKey"
                >
                  <AppIcon :name="showApiKey ? 'eye-off' : 'eye'" :size="15" />
                </button>
              </span>
            </div>
          </div>
        </div>

        <!-- 火山方舟额度凭证 -->
        <div v-if="needsQuotaCredential" class="form-section">
          <span class="form-section-title">套餐额度查询凭证（可选）</span>
          <p class="field-hint">
            火山方舟的套餐额度位于账号管控面，需配置火山引擎 Access Key 对查询；推理 API Key
            无法读取额度。
          </p>
          <div class="form-grid">
            <div class="field">
              <label for="instance-quota-ak">
                Access Key ID
                <span v-if="editing.quotaAccessKeyIdStored" class="muted">（留空保留）</span>
              </label>
              <input
                id="instance-quota-ak"
                v-model="editing.quotaAccessKeyId"
                autocomplete="off"
                placeholder="AKLT..."
              />
            </div>
            <div class="field">
              <label for="instance-quota-sk">
                Secret Access Key
                <span v-if="editing.id !== null && editing.quotaSecretHint" class="muted">
                  （留空保留 {{ editing.quotaSecretHint }}）
                </span>
              </label>
              <input
                id="instance-quota-sk"
                v-model="editing.quotaSecretKey"
                type="password"
                autocomplete="off"
                :placeholder="editing.id === null ? '填写后即可显示额度' : '留空则不修改'"
              />
            </div>
          </div>
        </div>

        <!-- 3. 自定义端点配置 -->
        <div v-if="editing.mode === 'custom'" class="form-section">
          <div class="row space-between">
            <span class="form-section-title">自定义协议端点</span>
            <button
              type="button"
              class="ghost sm"
              :disabled="editing.endpoints.length >= 2"
              @click="addEndpoint(editing)"
            >
              <AppIcon name="plus" :size="13" />
              <span>添加端点</span>
            </button>
          </div>
          <p class="field-hint">至少配置一个端点，同一接入点下每种协议仅支持一个地址。</p>

          <div v-for="(row, index) in editing.endpoints" :key="index" class="row">
            <label class="visually-hidden" :for="`instance-endpoint-protocol-${index}`">协议</label>
            <select
              :id="`instance-endpoint-protocol-${index}`"
              v-model="row.protocol"
              style="width: 132px"
            >
              <option value="openai">openai</option>
              <option value="anthropic">anthropic</option>
            </select>
            <label class="visually-hidden" :for="`instance-endpoint-base-${index}`">
              Base URL
            </label>
            <input
              :id="`instance-endpoint-base-${index}`"
              v-model="row.base_url"
              style="flex: 1"
              placeholder="https://api.example.com/v1"
            />
            <button
              type="button"
              class="ghost danger sm"
              :disabled="editing.endpoints.length <= 1"
              @click="removeEndpoint(editing, index)"
            >
              删除
            </button>
          </div>

          <div class="checkbox-card" :class="{ 'danger-active': editing.allowPrivateUpstream }">
            <input
              id="instance-allow-private"
              v-model="editing.allowPrivateUpstream"
              type="checkbox"
            />
            <div class="stack" style="gap: 4px">
              <label for="instance-allow-private" style="font-weight: 600; cursor: pointer">
                允许指向内网 / 本机地址
              </label>
              <p class="field-hint">
                默认拒绝 loopback、内网网段与云元数据地址（169.254.169.254）。仅在本地 mock
                或可信内网服务时勾选。
              </p>
            </div>
          </div>
        </div>

        <!-- 4. 支持的模型 + 候选查询 -->
        <div class="form-section">
          <div class="row space-between">
            <label for="instance-models" class="form-section-title">
              支持的上游模型（每行一个，必填）
            </label>
            <span class="badge">{{ declaredModelCount }} 个模型</span>
          </div>

          <textarea
            id="instance-models"
            v-model="editing.modelsText"
            rows="4"
            placeholder="例如 MiniMax-M3.1-Flash"
          />

          <p class="field-hint">模型页绑定该接入点时，仅允许从这里声明的上游模型名中选择。</p>

          <div
            v-if="editing.mode === 'builtin' && editing.id === null"
            class="stack"
            style="padding-top: 6px; border-top: 1px solid var(--border-subtle)"
          >
            <div class="row space-between">
              <div class="row">
                <button
                  type="button"
                  class="ghost sm"
                  :disabled="candidatesLoading || !editing.apiKey.trim()"
                  @click="queryCandidates"
                >
                  <AppIcon name="search" :size="13" />
                  <span>{{ candidatesLoading ? '查询中…' : '从上游拉取可用模型' }}</span>
                </button>
                <button
                  v-if="filteredCandidates.length"
                  type="button"
                  class="ghost sm"
                  @click="addAllCandidates"
                >
                  全部添加（{{ filteredCandidates.length }}）
                </button>
              </div>
              <span v-if="!editing.apiKey.trim()" class="field-hint">请先填写上方 API Key</span>
            </div>

            <div v-if="candidates.length" class="input-wrap" style="max-width: 300px">
              <label class="visually-hidden" for="candidate-search">筛选候选模型</label>
              <input
                id="candidate-search"
                v-model="candidateSearch"
                placeholder="筛选上游返回的模型…"
              />
              <span v-if="candidateSearch" class="input-suffix">
                <button
                  type="button"
                  class="icon-btn"
                  aria-label="清空模型筛选"
                  @click="candidateSearch = ''"
                >
                  <AppIcon name="close" :size="13" />
                </button>
              </span>
            </div>

            <p v-if="candidatesWarning" class="field-hint">{{ candidatesWarning }}</p>
            <p v-if="candidatesError" class="error">{{ candidatesError }}</p>

            <div v-if="filteredCandidates.length" class="tag-list">
              <button
                v-for="candidate in filteredCandidates"
                :key="candidate.id"
                type="button"
                class="chip-btn"
                :class="{ active: candidateAdded(candidate.id) }"
                @click="toggleCandidate(candidate.id)"
              >
                <span class="mono">{{ candidate.id }}</span>
                <span
                  v-for="protocol in candidate.protocols ?? []"
                  :key="protocol"
                  class="badge"
                  :class="protocol"
                >
                  {{ protocol }}
                </span>
              </button>
            </div>
          </div>
        </div>

        <!-- 5. 高级容错与会话设置（折叠减负） -->
        <details class="form-collapse" :open="advancedCustomCount > 0">
          <summary>
            <span>高级容错与会话设置</span>
            <span class="badge" :class="advancedCustomCount > 0 ? 'accent' : 'off'">
              {{ advancedCustomCount > 0 ? `已自定义 ${advancedCustomCount} 项` : '使用默认配置' }}
            </span>
          </summary>
          <div class="collapse-body">
            <div class="form-grid">
              <div class="field">
                <label for="instance-max-fails">熔断阈值（连续失败次数）</label>
                <input
                  id="instance-max-fails"
                  v-model="editing.maxFails"
                  type="text"
                  inputmode="numeric"
                  placeholder="0 表示用默认 3"
                />
              </div>
              <div class="field">
                <label for="instance-cooldown">冷却时间（秒）</label>
                <input
                  id="instance-cooldown"
                  v-model="editing.cooldownSecs"
                  type="text"
                  inputmode="numeric"
                  placeholder="0 表示用默认 30"
                />
              </div>
              <div class="field">
                <label for="instance-timeout">请求超时（秒）</label>
                <input
                  id="instance-timeout"
                  v-model="editing.timeoutSecs"
                  type="text"
                  inputmode="numeric"
                  placeholder="0 表示用全局默认"
                />
              </div>
              <div class="field">
                <label for="instance-failover-statuses">额外可切换状态码（可选）</label>
                <input
                  id="instance-failover-statuses"
                  v-model="editing.failoverStatuses"
                  type="text"
                  placeholder="例如 402, 400"
                />
                <p class="field-hint">
                  429 与 5xx 始终切换；这里仅追加厂商特有状态码（如 402 套餐耗尽）。
                </p>
              </div>
              <div class="field span-2">
                <label for="instance-session-header">会话粘性请求头（可选）</label>
                <input
                  id="instance-session-header"
                  v-model="editing.sessionHeader"
                  type="text"
                  placeholder="例如 x-opencode-session（内置端点留空用默认值）"
                />
                <p class="field-hint">
                  客户端未携带该头时，网关按「系统提示 + 首条用户消息」派生稳定值以提升 Prompt
                  缓存命中率。
                </p>
              </div>
              <div class="field span-2">
                <label for="instance-proxy-url">
                  网络代理（可选）
                  <span v-if="editing.proxyHasAuth && editing.proxyUrlHint" class="muted">
                    （当前 {{ editing.proxyUrlHint }}，留空保持不变）
                  </span>
                </label>
                <input
                  id="instance-proxy-url"
                  v-model="editing.proxyUrl"
                  type="text"
                  autocomplete="off"
                  :disabled="editing.proxyHasAuth && editing.clearProxy"
                  :placeholder="
                    editing.proxyHasAuth
                      ? '留空保留原有代理，填写新地址则覆盖'
                      : '例如 http://127.0.0.1:7890 或 socks5://user:pass@127.0.0.1:1080'
                  "
                />
                <p class="field-hint">
                  支持 http / https / socks5 /
                  socks5h，作用于推理转发、套餐额度查询与模型拉取；留空则沿用进程环境变量或直连。
                </p>
                <div v-if="editing.proxyHasAuth" class="row" style="margin-top: 6px; gap: 6px">
                  <input id="instance-clear-proxy" v-model="editing.clearProxy" type="checkbox" />
                  <label for="instance-clear-proxy" class="small" style="cursor: pointer">
                    清除已有代理配置
                  </label>
                </div>
              </div>
            </div>
          </div>
        </details>
      </form>

      <template #footer>
        <p v-if="error" class="error">{{ error }}</p>
        <div class="row space-between">
          <button type="button" class="ghost" @click="closeModal">取消</button>
          <button type="submit" form="instance-form" class="primary" :disabled="saving">
            {{ saving ? '保存中…' : '保存接入点' }}
          </button>
        </div>
      </template>
    </BaseModal>
  </section>
</template>
