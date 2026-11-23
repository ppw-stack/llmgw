<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue';
import { api } from '../api';
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
const candidates = ref<PresetModelCandidate[]>([]);
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
  for (const report of quotaReports.value) map[report.instance_id] = report;
  return map;
});

/** 该接入点的额度窗口；没有额度接口的接入点返回空列表。 */
function quotaWindows(inst: Instance): QuotaWindow[] {
  const report = quotaByID.value[inst.id];
  if (!report?.supported) return [];
  return report.windows;
}

/** 该接入点的额度查询失败原因。 */
function quotaMessage(inst: Instance): string {
  const report = quotaByID.value[inst.id];
  if (!report?.supported) return '';
  return report.error ?? '';
}

/** 进度条宽度，限制在 0 到 100 之间。 */
function quotaWidth(pct: number): string {
  const value = Math.min(Math.max(pct, 0), 100);
  return `${value}%`;
}

/** 进度条配色：七成以下正常，九成以上告急。 */
function quotaLevel(pct: number): string {
  if (pct >= 90) return 'danger';
  if (pct >= 70) return 'warn';
  return 'ok';
}

/** 已用百分比。 */
function quotaPercent(pct: number): string {
  return `${Math.round(pct)}%`;
}

/** 距窗口重置还有多久；上游没给重置时间时显示短横线。 */
function quotaCountdown(resetsAt?: string): string {
  if (!resetsAt) return '-';
  const target = new Date(resetsAt).getTime();
  if (Number.isNaN(target)) return '-';
  const diff = target - now.value;
  if (diff <= 0) return '即将重置';
  const minutes = Math.floor(diff / 60_000);
  const days = Math.floor(minutes / 1440);
  const hours = Math.floor((minutes % 1440) / 60);
  if (days > 0) return `${days} 天 ${hours} 小时`;
  if (hours > 0) return `${hours} 小时 ${minutes % 60} 分`;
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
  if (form?.mode !== 'builtin') return null;
  return findPreset(form.preset);
});

/** 该预设需要单独配置额度查询凭证：火山方舟读套餐额度要用 Access Key 对。 */
const needsQuotaCredential = computed<boolean>(() => selectedPreset.value?.quota?.kind === 'ark');

/** 内置模式下只读展示的端点：优先取预设自带的地址。 */
const builtinEndpoints = computed<InstanceEndpoint[]>(() => {
  const form = editing.value;
  if (form?.mode !== 'builtin') return [];
  const preset = selectedPreset.value;
  if (!preset) return form.endpoints;
  const list: InstanceEndpoint[] = [];
  if (preset.openai_base_url) list.push({ protocol: 'openai', base_url: preset.openai_base_url });
  if (preset.anthropic_base_url) {
    list.push({ protocol: 'anthropic', base_url: preset.anthropic_base_url });
  }
  return list;
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
    modelsText: '',
    allowPrivateUpstream: false,
    maxFails: '3',
    cooldownSecs: '30',
    timeoutSecs: '0',
  };
}

function applyPreset(form: InstanceForm, id: string, overwriteModels: boolean) {
  form.preset = id;
  const preset = findPreset(id);
  if (!preset) return;
  if (!form.name.trim()) form.name = preset.default_name;
  if (overwriteModels || !form.modelsText.trim()) {
    form.modelsText = preset.models.join('\n');
  }
}

function choosePreset(id: string) {
  const form = editing.value;
  if (!form) return;
  resetCandidates();
  applyPreset(form, id, true);
}

function onPresetChange(event: Event) {
  const target = event.target;
  if (!(target instanceof HTMLSelectElement)) return;
  choosePreset(target.value);
}

function setMode(mode: InstanceMode) {
  const form = editing.value;
  if (!form) return;
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
  form.endpoints.push({ protocol: 'openai', base_url: '' });
}

function removeEndpoint(form: InstanceForm, index: number) {
  if (form.endpoints.length <= 1) return;
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
  candidatesLoading.value = false;
  candidatesWarning.value = '';
  candidatesError.value = '';
}

/** 查询内置端点上游的模型列表，作为待选建议。 */
async function queryCandidates() {
  const form = editing.value;
  if (form?.mode !== 'builtin' || form.id !== null) return;

  const apiKey = form.apiKey.trim();
  if (!apiKey) {
    candidatesError.value = '请先填写上游 API Key';
    return;
  }

  candidatesLoading.value = true;
  candidatesWarning.value = '';
  candidatesError.value = '';
  try {
    const result = await api<PresetModelCandidates>('/preset-models', {
      method: 'POST',
      body: JSON.stringify({ preset: form.preset, api_key: apiKey }),
    });
    candidates.value = result.models;
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
  if (!form) return false;
  return modelLines(form.modelsText).includes(id);
}

/** 点击候选时在模型文本框里添加或移除该模型。 */
function toggleCandidate(id: string) {
  const form = editing.value;
  if (!form) return;
  const current = modelLines(form.modelsText);
  const next = current.includes(id) ? current.filter((name) => name !== id) : [...current, id];
  form.modelsText = next.join('\n');
}

/** 把当前查询结果里尚未添加的模型一次性写入文本框。 */
function addAllCandidates() {
  const form = editing.value;
  if (!form) return;
  const next = modelLines(form.modelsText);
  const seen = new Set(next);
  for (const candidate of candidates.value) {
    if (seen.has(candidate.id)) continue;
    seen.add(candidate.id);
    next.push(candidate.id);
  }
  form.modelsText = next.join('\n');
}

/** 解析表单里的非负整数，非法时返回 null。 */
function parseCount(raw: string): number | null {
  const text = raw.trim();
  if (text === '') return null;
  const value = Number(text);
  if (!Number.isInteger(value) || value < 0) return null;
  return value;
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
  resetCandidates();
  editing.value = newForm();
}

function openEdit(inst: Instance) {
  error.value = '';
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
    maxFails: String(inst.max_fails),
    cooldownSecs: String(inst.cooldown_secs),
    timeoutSecs: String(inst.timeout_secs),
  };
}

async function save() {
  const f = editing.value;
  if (!f) return;

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

  const payload: InstancePayload = { name, models: supported };
  if (f.mode === 'builtin') {
    // preset 一旦创建不可更改：编辑已有接入点时不提交该字段。
    if (f.id === null) payload.preset = f.preset;
  } else {
    const endpoints = collectEndpoints(f);
    if (!endpoints) return;
    payload.endpoints = endpoints;
  }
  if (f.id === null || f.apiKey.trim()) {
    payload.api_key = f.apiKey.trim();
  }
  // The quota credential is only submitted when the operator filled one in;
  // an untouched field keeps the stored credential.
  //
  // The access key id is prefilled when editing, so a blank secret must not
  // be read as "clear the credential": it means "keep the stored one". Only
  // a changed access key id needs a new secret, because the stored secret
  // belongs to the old pair.
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
  // The private-upstream opt-in is always sent, even when the operator did
  // not touch it: the backend reads an omitted field as "off", so leaving it
  // out would silently close the gate of an instance that already has it.
  payload.allow_private_upstream = f.allowPrivateUpstream;

  saving.value = true;
  error.value = '';
  try {
    if (f.id === null) {
      await api<Instance>('/instances', { method: 'POST', body: JSON.stringify(payload) });
    } else {
      await api<Instance>(`/instances/${f.id}`, {
        method: 'PUT',
        body: JSON.stringify(payload),
      });
    }
    editing.value = null;
    resetCandidates();
    await load();
  } catch (e) {
    error.value = errorMessage(e);
  } finally {
    saving.value = false;
  }
}

async function remove(inst: Instance) {
  const used = inst.models.length
    ? `它仍被模型 ${inst.models.join('、')} 引用，需要先从这些模型里移除。`
    : '';
  if (!confirmAction(`删除上游接入点「${inst.name}」？${used}`)) return;
  error.value = '';
  try {
    await api<null>(`/instances/${inst.id}`, { method: 'DELETE' });
    await load();
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
  if (document.visibilityState === 'visible') void loadQuota();
}

onMounted(async () => {
  await loadPresets();
  await load();
  await loadQuota();
  quotaTimer = window.setInterval(() => {
    // 页面在后台时不打扰上游。
    if (document.visibilityState === 'visible') void loadQuota();
  }, QUOTA_POLL_MS);
  tickTimer = window.setInterval(() => {
    now.value = Date.now();
  }, QUOTA_TICK_MS);
  document.addEventListener('visibilitychange', onVisibilityChange);
});

onUnmounted(() => {
  if (quotaTimer !== undefined) window.clearInterval(quotaTimer);
  if (tickTimer !== undefined) window.clearInterval(tickTimer);
  document.removeEventListener('visibilitychange', onVisibilityChange);
});
</script>

<template>
  <section>
    <div class="section-head">
      <h2>上游接入点</h2>
      <div class="row">
        <button class="ghost" @click="refresh">刷新</button>
        <button class="primary" @click="openCreate">新增接入点</button>
      </div>
    </div>
    <p class="muted">
      上游接入点代表一个物理上游（厂商地址 + API Key）。模型在「模型」页引用接入点组成主备链路。
    </p>
    <p v-if="error && !editing" class="error">{{ error }}</p>
    <p v-if="quotaError" class="error">额度查询失败：{{ quotaError }}</p>
    <p v-if="loading" class="muted">加载中…</p>

    <div v-else-if="!instances.length" class="card muted">
      还没有上游接入点。点击「新增接入点」开始配置。
    </div>

    <div v-for="inst in instances" :key="inst.id" class="card provider">
      <div class="provider-head">
        <div class="row">
          <h3>{{ inst.name }}</h3>
          <span class="badge" :class="{ builtin: inst.source === 'builtin' }">
            {{ inst.source === 'builtin' ? '内置端点' : '自定义' }}
          </span>
          <span v-if="inst.preset_name" class="badge">{{ inst.preset_name }}</span>
          <span v-if="inst.allow_private_upstream" class="badge danger">允许内网</span>
        </div>
        <div class="row">
          <a
            v-if="inst.docs_url"
            class="ghost link"
            :href="inst.docs_url"
            target="_blank"
            rel="noreferrer"
            >文档</a
          >
          <button class="ghost" @click="openEdit(inst)">编辑</button>
          <button class="ghost danger" @click="remove(inst)">删除</button>
        </div>
      </div>

      <div v-if="inst.key_hint" class="key-hints">
        <code>{{ inst.key_hint }}</code>
      </div>

      <div class="endpoint-list">
        <div
          v-for="ep in inst.endpoints"
          :key="`${ep.protocol}:${ep.base_url}`"
          class="endpoint-line"
        >
          <span class="badge" :class="ep.protocol">{{ ep.protocol }}</span>
          <code class="base">{{ ep.base_url }}</code>
        </div>
      </div>

      <div class="row small muted">
        <span>熔断：连续失败 {{ inst.max_fails }} 次后冷却 {{ inst.cooldown_secs }} 秒</span>
        <span>超时：{{ inst.timeout_secs > 0 ? inst.timeout_secs + ' 秒' : '默认' }}</span>
      </div>

      <div class="models">
        <span v-if="!inst.models.length" class="muted small">未声明支持的模型</span>
        <span v-for="model in inst.models" :key="model" class="tag">{{ model }}</span>
      </div>

      <div v-if="quotaWindows(inst).length || quotaMessage(inst)" class="quota">
        <p v-if="quotaMessage(inst)" class="quota-error">{{ quotaMessage(inst) }}</p>
        <div v-for="w in quotaWindows(inst)" :key="w.id" class="quota-row">
          <span class="quota-label">{{ w.label }}</span>
          <span class="quota-track">
            <span
              class="quota-fill"
              :class="quotaLevel(w.used_pct)"
              :style="{ width: quotaWidth(w.used_pct) }"
            ></span>
          </span>
          <span class="quota-value">
            已用 {{ quotaPercent(w.used_pct) }}
            <span class="muted">{{ quotaCountdown(w.resets_at) }}</span>
          </span>
        </div>
      </div>

      <div v-if="usedBy(inst.name).length" class="row small muted">
        <span>已被模型引用：{{ usedBy(inst.name).join('、') }}</span>
      </div>
    </div>

    <div v-if="editing" class="modal-backdrop" role="presentation" @click.self="editing = null">
      <div class="card modal wide">
        <div class="row space-between">
          <h3>{{ editing.id === null ? '新增上游接入点' : '编辑上游接入点' }}</h3>
          <button class="ghost" @click="editing = null">关闭</button>
        </div>

        <div v-if="editing.id === null" class="mode-switch">
          <button
            class="ghost"
            :class="{ active: editing.mode === 'builtin' }"
            :disabled="!presets.length"
            @click="setMode('builtin')"
          >
            内置端点
          </button>
          <button
            class="ghost"
            :class="{ active: editing.mode === 'custom' }"
            @click="setMode('custom')"
          >
            自定义端点
          </button>
        </div>
        <p v-else class="muted small">
          端点来源在创建后不可切换：内置端点由预设提供，自定义端点由下面的地址列表提供。
        </p>

        <div class="form-grid">
          <div class="field">
            <label for="instance-name">名称</label>
            <input id="instance-name" v-model="editing.name" placeholder="例如 minimax" />
          </div>
          <div class="field">
            <label for="instance-api-key">
              上游 API Key
              <span v-if="editing.id !== null && editing.keyHint" class="muted small">
                留空保留现有 {{ editing.keyHint }}
              </span>
            </label>
            <input
              id="instance-api-key"
              v-model="editing.apiKey"
              type="password"
              autocomplete="off"
              :placeholder="editing.id === null ? 'sk-...' : '留空则不修改'"
            />
          </div>

          <div v-if="editing.mode === 'builtin'" class="field span-2">
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
            <p v-if="selectedPreset" class="muted small">
              base URL 由程序内置，随版本升级自动更新。
              <a :href="selectedPreset.docs_url" target="_blank" rel="noreferrer">官方文档</a>
            </p>
          </div>

          <template v-if="needsQuotaCredential">
            <div class="field span-2">
              <label for="instance-quota-ak">
                额度查询凭证 · Access Key ID
                <span v-if="editing.quotaAccessKeyIdStored" class="muted small">
                  留空保留现有
                </span>
              </label>
              <input
                id="instance-quota-ak"
                v-model="editing.quotaAccessKeyId"
                autocomplete="off"
                placeholder="AKLT..."
              />
              <p class="muted small">
                火山方舟的套餐额度只在账号管控面上，需要火山引擎的 Access Key 对才能查询； 推理用的
                API Key 不能读额度。
              </p>
            </div>
            <div class="field span-2">
              <label for="instance-quota-sk">
                额度查询凭证 · Secret Access Key
                <span v-if="editing.id !== null && editing.quotaSecretHint" class="muted small">
                  留空保留现有 {{ editing.quotaSecretHint }}
                </span>
              </label>
              <input
                id="instance-quota-sk"
                v-model="editing.quotaSecretKey"
                type="password"
                autocomplete="off"
                :placeholder="editing.id === null ? '填写后即可显示套餐额度' : '留空则不修改'"
              />
            </div>
          </template>

          <div v-if="editing.mode === 'builtin'" class="field span-2 readonly">
            <span class="field-caption">Base URL（只读）</span>
            <div v-for="ep in builtinEndpoints" :key="ep.protocol" class="endpoint-line">
              <span class="badge" :class="ep.protocol">{{ ep.protocol }}</span>
              <code class="base">{{ ep.base_url }}</code>
            </div>
            <p v-if="selectedPreset && selectedPreset.models.length" class="muted small">
              建议模型：{{ selectedPreset.models.join('、') }}
            </p>
          </div>

          <div v-if="editing.mode === 'custom'" class="field span-2">
            <span class="field-caption">端点（至少一个，同一协议只能有一个地址）</span>
            <div v-for="(row, index) in editing.endpoints" :key="index" class="row">
              <label class="visually-hidden" :for="`instance-endpoint-protocol-${index}`">
                协议
              </label>
              <select
                :id="`instance-endpoint-protocol-${index}`"
                v-model="row.protocol"
                class="input-fixed"
              >
                <option value="openai">openai</option>
                <option value="anthropic">anthropic</option>
              </select>
              <label class="visually-hidden" :for="`instance-endpoint-base-${index}`"
                >Base URL</label
              >
              <input
                :id="`instance-endpoint-base-${index}`"
                v-model="row.base_url"
                placeholder="https://api.example.com/v1"
              />
              <button
                class="ghost danger"
                :disabled="editing.endpoints.length <= 1"
                @click="removeEndpoint(editing, index)"
              >
                删除
              </button>
            </div>
            <div class="row">
              <button class="ghost" @click="addEndpoint(editing)">添加端点</button>
            </div>
            <div class="row">
              <label for="instance-allow-private">允许内网地址</label>
              <input
                id="instance-allow-private"
                v-model="editing.allowPrivateUpstream"
                type="checkbox"
              />
            </div>
            <p class="muted small">
              默认拒绝指向 loopback、内网网段与云元数据地址（169.254.169.254）的上游，
              要指向这类地址必须先勾选本项。打开后网关可以拨号到 127.0.0.1、10.0.0.0/8
              这类地址：仅在本地 mock 或内网自建上游时开启，控制台被拿下时它等于把网关
              变成内网代理。
            </p>
            <p v-if="editing.allowPrivateUpstream" class="error">
              该接入点已放行内网地址，请确认上游确实是可信的内网服务。
            </p>
          </div>

          <div class="field span-2">
            <label for="instance-models">支持的模型（每行一个上游模型名，必填）</label>
            <textarea
              id="instance-models"
              v-model="editing.modelsText"
              rows="3"
              placeholder="MiniMax-M3.1-Flash"
            ></textarea>
            <p class="muted small">
              模型页把模型挂到这个接入点前，会校验模型的上游名是否在此列表内。
            </p>
          </div>

          <div v-if="editing.mode === 'builtin' && editing.id === null" class="field span-2">
            <span class="field-caption">上游可用模型</span>
            <div class="row">
              <button
                type="button"
                class="ghost"
                :disabled="candidatesLoading || !editing.apiKey.trim()"
                @click="queryCandidates"
              >
                {{ candidatesLoading ? '查询中…' : '查询上游可用模型' }}
              </button>
              <button
                v-if="candidates.length"
                type="button"
                class="ghost"
                @click="addAllCandidates"
              >
                全部添加
              </button>
              <span v-if="!editing.apiKey.trim()" class="muted small">请先填写上方 API Key</span>
            </div>
            <p v-if="candidatesWarning" class="muted small">{{ candidatesWarning }}</p>
            <p v-if="candidatesError" class="error">{{ candidatesError }}</p>
            <div v-if="candidates.length" class="candidate-list">
              <button
                v-for="candidate in candidates"
                :key="candidate.id"
                type="button"
                class="candidate"
                :class="{ added: candidateAdded(candidate.id) }"
                @click="toggleCandidate(candidate.id)"
              >
                <span class="candidate-id">{{ candidate.id }}</span>
                <span
                  v-for="protocol in candidate.protocols ?? []"
                  :key="protocol"
                  class="badge"
                  :class="protocol"
                  >{{ protocol }}</span
                >
              </button>
            </div>
          </div>

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
        </div>

        <p v-if="error" class="error">{{ error }}</p>
        <div class="row">
          <button class="primary" :disabled="saving" @click="save">
            {{ saving ? '保存中…' : '保存' }}
          </button>
          <button class="ghost" @click="editing = null">取消</button>
        </div>
      </div>
    </div>
  </section>
</template>
