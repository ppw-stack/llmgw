<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue';
import { api } from '../api';
import AppIcon from '../components/AppIcon.vue';
import BaseModal from '../components/BaseModal.vue';
import type {
  EffectiveCandidate,
  Instance,
  Model,
  ModelInstance,
  ModelMetadata,
  ModelMetadataOverride,
  ModelPayload,
  TimeWindow,
} from '../types';
import { confirmAction } from '../utils/confirm';
import { errorMessage } from '../utils/error';
import { showToast } from '../utils/toast';

/** 表单里的一行时间窗口。优先级留空表示窗口不覆盖优先级。 */
interface WindowRow {
  start: string;
  end: string;
  priority: string;
  only: boolean;
}

/** 表单里的一行候选接入点：接入点 + 该接入点上的具体模型。 */
interface CandidateRow {
  instance: string;
  upstream: string;
  priority: string;
  windows: WindowRow[];
}

/** 支持按协议覆盖元数据的协议名。 */
type ProtocolName = 'openai' | 'anthropic';

const protocolNames: ProtocolName[] = ['openai', 'anthropic'];
const protocolLabels: Record<ProtocolName, string> = {
  openai: 'OpenAI',
  anthropic: 'Anthropic',
};

const RECOMMENDED_CAPABILITIES = [
  'tools',
  'vision',
  'video',
  'audio',
  'reasoning',
  'json',
  'cache',
  'search',
  'doc',
] as const;

/** 表单里某个协议的元数据覆盖，空字符串表示继承通用值。 */
interface ProtocolMetadataForm {
  contextLength: string;
  maxOutputTokens: string;
  capabilities: string;
}

/** 表单里的模型元数据。 */
interface MetadataForm {
  displayName: string;
  contextLength: string;
  maxOutputTokens: string;
  capabilities: string;
  protocols: Record<ProtocolName, ProtocolMetadataForm>;
}

/** 新增 / 编辑模型的表单状态。 */
interface ModelForm {
  id: number | null;
  name: string;
  metadata: MetadataForm;
  candidates: CandidateRow[];
}

const models = ref<Model[]>([]);
const instances = ref<Instance[]>([]);
const loading = ref(true);
const error = ref('');
const refreshError = ref('');
const editing = ref<ModelForm | null>(null);
const saving = ref(false);

function instanceByName(name: string): Instance | null {
  return instances.value.find((i) => i.name === name) ?? null;
}

/** 某个接入点声明的上游模型名。 */
function instanceModels(name: string): string[] {
  return instanceByName(name)?.models ?? [];
}

/** 切换接入点时，把候选模型收敛到该接入点声明里的第一项。 */
function onInstanceChange(row: CandidateRow) {
  const declared = instanceModels(row.instance);
  if (!declared.includes(row.upstream)) {
    row.upstream = declared[0] ?? '';
  }
}

/** 下拉框里的接入点标签，精简展示模型数量避免撑爆选项宽度。 */
function optionLabel(inst: Instance): string {
  if (!inst.models.length) {
    return `${inst.name}（未声明模型）`;
  }
  return `${inst.name}（${inst.models.length} 个模型）`;
}

/** 按优先级排序的候选链，排在最前面的是主接入点。 */
function sortedCandidates(m: Model): ModelInstance[] {
  return [...m.instances].sort((a, b) => a.priority - b.priority);
}

/** 当前在线的生效候选，按尝试顺序排列。 */
function activeCandidates(m: Model): EffectiveCandidate[] {
  return (m.effective_candidates ?? []).filter((c) => c.active);
}

/** 当前不在时间窗口内的候选。 */
function idleCandidates(m: Model): EffectiveCandidate[] {
  return (m.effective_candidates ?? []).filter((c) => !c.active);
}

/** 候选上的时间窗口摘要，用于列表展示。 */
function windowSummary(candidate: ModelInstance): string {
  if (!candidate.windows?.length) {
    return '';
  }
  return candidate.windows
    .map((w) => {
      const only = w.only ? '仅此时段可用' : `优先级 ${w.priority ?? '不变'}`;
      return `${w.start}-${w.end} ${only}`;
    })
    .join('；');
}

function newForm(): ModelForm {
  return {
    id: null,
    name: '',
    metadata: newMetadataForm(),
    candidates: [{ instance: '', upstream: '', priority: '0', windows: [] }],
  };
}

function emptyProtocolMetadata(): ProtocolMetadataForm {
  return { contextLength: '', maxOutputTokens: '', capabilities: '' };
}

function newMetadataForm(): MetadataForm {
  return {
    displayName: '',
    contextLength: '',
    maxOutputTokens: '',
    capabilities: '',
    protocols: {
      openai: emptyProtocolMetadata(),
      anthropic: emptyProtocolMetadata(),
    },
  };
}

/** 后端数字字段到表单文本，0 / 未设置都显示为空。 */
function countText(value: number | undefined): string {
  return value ? String(value) : '';
}

function capabilitiesText(values: string[] | undefined): string {
  return (values ?? []).join(', ');
}

function toMetadataForm(meta: ModelMetadata | undefined): MetadataForm {
  const form = newMetadataForm();
  if (!meta) {
    return form;
  }
  form.displayName = meta.display_name ?? '';
  form.contextLength = countText(meta.context_length);
  form.maxOutputTokens = countText(meta.max_output_tokens);
  form.capabilities = capabilitiesText(meta.capabilities);
  for (const protocol of protocolNames) {
    const override = meta.protocols?.[protocol];
    if (!override) {
      continue;
    }
    const row = form.protocols[protocol];
    row.contextLength = countText(override.context_length);
    row.maxOutputTokens = countText(override.max_output_tokens);
    row.capabilities = capabilitiesText(override.capabilities);
  }
  return form;
}

/** 逗号分隔的能力标签，去空、去重并统一小写。 */
function parseCapabilities(raw: string): string[] {
  const out: string[] = [];
  for (const part of raw.split(',')) {
    const value = part.trim().toLowerCase();
    if (value && !out.includes(value)) {
      out.push(value);
    }
  }
  return out;
}

function hasCapability(raw: string, cap: string): boolean {
  return parseCapabilities(raw).includes(cap);
}

function toggleCapability(cap: string) {
  const form = editing.value;
  if (!form) {
    return;
  }
  const list = parseCapabilities(form.metadata.capabilities);
  const next = list.includes(cap) ? list.filter((item) => item !== cap) : [...list, cap];
  form.metadata.capabilities = next.join(', ');
}

/** 表单中是否配置了自定义元数据，用于控制折叠块默认状态与徽章。 */
const hasCustomMetadata = computed(() => {
  const m = editing.value?.metadata;
  if (!m) {
    return false;
  }
  if (
    m.displayName.trim() ||
    m.contextLength.trim() ||
    m.maxOutputTokens.trim() ||
    m.capabilities.trim()
  ) {
    return true;
  }
  return protocolNames.some((p) => {
    const row = m.protocols[p];
    return Boolean(
      row.contextLength.trim() || row.maxOutputTokens.trim() || row.capabilities.trim(),
    );
  });
});

/**
 * 解析可选的非负整数。undefined 表示留空；null 表示格式错误，并在页面上
 * 留下错误提示。
 */
function optionalCount(raw: string, label: string): number | null | undefined {
  const text = raw.trim();
  if (!text) {
    return undefined;
  }
  const value = Number(text);
  if (!Number.isInteger(value) || value < 0) {
    error.value = `${label}必须是非负整数`;
    return null;
  }
  return value;
}

/** 把表单转换成接口要的元数据；没有填写时返回空对象。 */
function buildMetadata(form: MetadataForm): ModelMetadata | null {
  const meta: ModelMetadata = {};
  const displayName = form.displayName.trim();
  if (displayName) {
    meta.display_name = displayName;
  }

  const contextLength = optionalCount(form.contextLength, '上下文长度');
  if (contextLength === null) {
    return null;
  }
  if (contextLength !== undefined) {
    meta.context_length = contextLength;
  }

  const maxOutputTokens = optionalCount(form.maxOutputTokens, '最大输出');
  if (maxOutputTokens === null) {
    return null;
  }
  if (maxOutputTokens !== undefined) {
    meta.max_output_tokens = maxOutputTokens;
  }

  const capabilities = parseCapabilities(form.capabilities);
  if (capabilities.length) {
    meta.capabilities = capabilities;
  }

  const protocols: Record<string, ModelMetadataOverride> = {};
  for (const protocol of protocolNames) {
    const row = form.protocols[protocol];
    const label = protocolLabels[protocol];
    const override: ModelMetadataOverride = {};

    const context = optionalCount(row.contextLength, `${label} 上下文长度`);
    if (context === null) {
      return null;
    }
    if (context !== undefined) {
      override.context_length = context;
    }

    const output = optionalCount(row.maxOutputTokens, `${label} 最大输出`);
    if (output === null) {
      return null;
    }
    if (output !== undefined) {
      override.max_output_tokens = output;
    }

    const caps = parseCapabilities(row.capabilities);
    if (caps.length) {
      override.capabilities = caps;
    }

    if (Object.keys(override).length) {
      protocols[protocol] = override;
    }
  }
  if (Object.keys(protocols).length) {
    meta.protocols = protocols;
  }
  return meta;
}

/** 模型卡片上的元数据条目。 */
function metadataBadges(m: Model): string[] {
  const meta = m.metadata;
  if (!meta) {
    return [];
  }
  const parts: string[] = [];
  if (meta.display_name) {
    parts.push(meta.display_name);
  }
  if (meta.context_length) {
    parts.push(`上下文 ${meta.context_length.toLocaleString('en-US')}`);
  }
  if (meta.max_output_tokens) {
    parts.push(`输出 ${meta.max_output_tokens.toLocaleString('en-US')}`);
  }
  if (meta.capabilities?.length) {
    parts.push(...meta.capabilities);
  }
  if (meta.protocols && Object.keys(meta.protocols).length) {
    parts.push('含按协议覆盖');
  }
  return parts;
}

function addCandidate(form: ModelForm) {
  const nextPriority = form.candidates.length;
  form.candidates.push({
    instance: '',
    upstream: '',
    priority: String(nextPriority),
    windows: [],
  });
}

/**
 * 按优先级重排表单里的候选，和实际路由顺序保持一致。
 *
 * 只在打开弹窗和优先级输入失焦后调用：输入过程中不移动行，避免光标跳走。
 */
function sortCandidates(form: ModelForm) {
  const rank = (row: CandidateRow): number => parseCount(row.priority) ?? Number.MAX_SAFE_INTEGER;
  form.candidates.sort((a, b) => rank(a) - rank(b));
}

/** 优先级输入完成后的重排入口，供模板使用。 */
function onPriorityChange() {
  if (editing.value) {
    sortCandidates(editing.value);
  }
}

function removeCandidate(form: ModelForm, index: number) {
  if (form.candidates.length <= 1) {
    return;
  }
  form.candidates.splice(index, 1);
}

function addWindow(row: CandidateRow) {
  row.windows.push({ start: '00:00', end: '08:00', priority: '0', only: false });
}

function removeWindow(row: CandidateRow, index: number) {
  row.windows.splice(index, 1);
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

/** 时间窗口的起止时间格式：24 小时制 HH:MM。 */
const clockPattern = /^\d{2}:\d{2}$/;

async function load() {
  loading.value = true;
  error.value = '';
  try {
    const [modelList, instanceList] = await Promise.all([
      api<Model[]>('/models'),
      api<Instance[]>('/instances'),
    ]);
    models.value = modelList;
    instances.value = instanceList;
  } catch (e) {
    error.value = errorMessage(e);
  } finally {
    loading.value = false;
  }
}

/**
 * 静默刷新：只换掉数据，不动 loading 状态。
 * 「当前生效顺序」随时段变化，定时刷新让页面跟上时间。
 */
async function refresh() {
  try {
    const [modelList, instanceList] = await Promise.all([
      api<Model[]>('/models'),
      api<Instance[]>('/instances'),
    ]);
    models.value = modelList;
    instances.value = instanceList;
    refreshError.value = '';
  } catch (e) {
    refreshError.value = errorMessage(e);
  }
}

function openCreate() {
  error.value = '';
  editing.value = newForm();
}

function openEdit(m: Model) {
  error.value = '';
  const rows = m.instances.map((mi) => ({
    instance: mi.instance,
    upstream: mi.upstream,
    priority: String(mi.priority),
    windows: (mi.windows ?? []).map((w) => ({
      start: w.start,
      end: w.end,
      priority: w.priority === null || w.priority === undefined ? '' : String(w.priority),
      only: Boolean(w.only),
    })),
  }));
  const form: ModelForm = {
    id: m.id,
    name: m.name,
    metadata: toMetadataForm(m.metadata),
    candidates: rows.length ? rows : [{ instance: '', upstream: '', priority: '0', windows: [] }],
  };
  sortCandidates(form);
  editing.value = form;
}

async function save() {
  const f = editing.value;
  if (!f) {
    return;
  }

  const name = f.name.trim();
  if (!name) {
    error.value = '请填写对外模型名';
    return;
  }
  if (!instances.value.length) {
    error.value = '还没有上游接入点，请先在「上游接入点」页面创建';
    return;
  }

  const list: ModelInstance[] = [];
  const seen = new Set<string>();
  for (const row of f.candidates) {
    const target = row.instance.trim();
    if (!target) {
      error.value = '请为每一行选择一个上游接入点';
      return;
    }
    if (seen.has(target)) {
      error.value = `上游接入点「${target}」被重复引用`;
      return;
    }
    seen.add(target);
    const upstream = row.upstream.trim();
    if (!upstream) {
      error.value = `请为接入点「${target}」选择一个模型`;
      return;
    }
    if (!instanceModels(target).includes(upstream)) {
      error.value = `接入点「${target}」没有声明模型「${upstream}」`;
      return;
    }
    const priority = parseCount(row.priority);
    if (priority === null) {
      error.value = '优先级必须是非负整数';
      return;
    }
    const windows: TimeWindow[] = [];
    for (const w of row.windows) {
      const start = w.start.trim();
      const end = w.end.trim();
      if (!start && !end && !w.priority.trim() && !w.only) {
        continue;
      }
      if (!clockPattern.test(start) || !clockPattern.test(end)) {
        error.value = '时间窗口的起止时间必须是 HH:MM 格式';
        return;
      }
      const hasPriority = w.priority.trim() !== '';
      const windowPriority = hasPriority ? parseCount(w.priority) : null;
      if (hasPriority && windowPriority === null) {
        error.value = '窗口内优先级必须是非负整数';
        return;
      }
      if (!hasPriority && !w.only) {
        error.value = '时间窗口需要填写窗口内优先级，或勾选「仅窗口内可用」';
        return;
      }
      windows.push({ start, end, priority: windowPriority, only: w.only });
    }
    list.push({ instance: target, upstream, priority, windows });
  }
  if (!list.length) {
    error.value = '至少需要一个候选接入点';
    return;
  }

  const payload: ModelPayload = {
    name,
    strategy: 'failover',
    instances: list,
  };
  const metadata = buildMetadata(f.metadata);
  if (metadata === null) {
    return;
  }
  if (Object.keys(metadata).length) {
    payload.metadata = metadata;
  }

  saving.value = true;
  error.value = '';
  const isCreate = f.id === null;
  try {
    if (isCreate) {
      await api<Model>('/models', { method: 'POST', body: JSON.stringify(payload) });
    } else {
      await api<Model>(`/models/${f.id}`, { method: 'PUT', body: JSON.stringify(payload) });
    }
    editing.value = null;
    await load();
    showToast(isCreate ? `已新增模型「${name}」` : `已保存模型「${name}」`);
  } catch (e) {
    error.value = errorMessage(e);
  } finally {
    saving.value = false;
  }
}

async function remove(m: Model) {
  if (!confirmAction(`删除模型「${m.name}」？使用该模型的客户端请求会立即失败。`)) {
    return;
  }
  error.value = '';
  try {
    await api<null>(`/models/${m.id}`, { method: 'DELETE' });
    await load();
    showToast(`已删除模型「${m.name}」`);
  } catch (e) {
    error.value = errorMessage(e);
  }
}

/** 刷新间隔：让「当前生效顺序」跟着时段变化。 */
const refreshIntervalMs = 30000;
let refreshTimer: number | undefined;

onMounted(() => {
  void load();
  refreshTimer = window.setInterval(() => {
    void refresh();
  }, refreshIntervalMs);
});

onUnmounted(() => {
  if (refreshTimer !== undefined) {
    window.clearInterval(refreshTimer);
    refreshTimer = undefined;
  }
});
</script>

<template>
  <section class="page">
    <div class="page-head">
      <div class="page-title-group">
        <h2>模型路由</h2>
        <p class="page-desc">
          配置对外暴露的模型名称与多上游主备链路；主接入点异常时按优先级自动故障转移。
        </p>
      </div>
      <div class="row">
        <button type="button" class="ghost" :disabled="loading" @click="load">
          <AppIcon name="refresh" :size="14" />
          <span>刷新</span>
        </button>
        <button type="button" class="primary" @click="openCreate">
          <AppIcon name="plus" :size="14" />
          <span>新增模型</span>
        </button>
      </div>
    </div>

    <p v-if="!loading && !instances.length" class="alert warn">
      还没有上游接入点，请先在「上游接入点」页面创建后再配置模型。
    </p>
    <p v-if="error && !editing" class="error">{{ error }}</p>
    <p v-if="refreshError" class="alert warn">
      自动刷新失败：{{ refreshError }}（当前仍显示上一次的数据）
    </p>

    <div v-if="loading" class="card stack">
      <div class="skeleton-line" style="width: 34%" />
      <div class="skeleton-line" style="width: 78%" />
      <div class="skeleton-line" style="width: 58%" />
    </div>

    <div v-else-if="!models.length" class="card empty-state">
      <span class="empty-icon"><AppIcon name="models" :size="18" /></span>
      <p>还没有配置模型</p>
      <button type="button" class="primary sm" :disabled="!instances.length" @click="openCreate">
        <AppIcon name="plus" :size="14" />
        <span>新增第一个模型</span>
      </button>
    </div>

    <div v-for="m in models" :key="m.id" class="card entity-card">
      <div class="entity-head">
        <div class="row">
          <h3 class="mono">{{ m.name }}</h3>
          <span class="badge accent">{{ m.strategy }}</span>
          <span class="muted small">时区 {{ m.timezone }}</span>
        </div>
        <div class="row">
          <button type="button" class="ghost sm" @click="openEdit(m)">
            <AppIcon name="edit" :size="13" />
            <span>编辑</span>
          </button>
          <button type="button" class="ghost danger sm" @click="remove(m)">
            <AppIcon name="trash" :size="13" />
            <span>删除</span>
          </button>
        </div>
      </div>

      <div v-if="metadataBadges(m).length" class="tag-list">
        <span v-for="badge in metadataBadges(m)" :key="badge" class="badge">{{ badge }}</span>
      </div>

      <!-- 当前生效链路可视化 -->
      <div v-if="m.effective_candidates?.length" class="stack" style="gap: 6px">
        <span class="muted small">当前生效链路</span>
        <div class="route-flow">
          <template
            v-for="(c, idx) in activeCandidates(m)"
            :key="`active-${c.instance}-${c.upstream}`"
          >
            <span v-if="idx > 0" class="route-arrow" aria-hidden="true">→</span>
            <div class="route-node" :class="{ 'primary-node': idx === 0 }">
              <span class="badge" :class="idx === 0 ? 'ok' : 'off'">
                {{ idx === 0 ? '主' : `备${idx}` }}
              </span>
              <span class="mono">{{ c.instance }}</span>
              <span class="muted">/</span>
              <span class="mono">{{ c.upstream }}</span>
              <span v-if="c.window" class="badge accent">{{ c.window }}</span>
            </div>
          </template>
          <div
            v-for="c in idleCandidates(m)"
            :key="`idle-${c.instance}-${c.upstream}`"
            class="route-node idle-node"
          >
            <span class="badge off">时段外</span>
            <span class="mono">{{ c.instance }} / {{ c.upstream }}</span>
          </div>
        </div>
      </div>

      <!-- 基础配置候选列表 -->
      <div class="endpoint-list">
        <div v-for="(c, index) in sortedCandidates(m)" :key="c.instance" class="endpoint-line">
          <span class="badge" :class="index === 0 ? 'ok' : 'off'">P{{ c.priority }}</span>
          <code class="endpoint-url">{{ c.instance }}</code>
          <span class="muted small">/</span>
          <code class="endpoint-url">{{ c.upstream }}</code>
          <span v-if="windowSummary(c)" class="badge">{{ windowSummary(c) }}</span>
        </div>
      </div>
    </div>

    <BaseModal
      v-if="editing"
      :title="editing.id === null ? '新增模型' : '编辑模型'"
      wide
      @close="editing = null"
    >
      <form id="model-form" class="stack" @submit.prevent="save">
        <p v-if="!instances.length" class="alert warn">
          当前没有任何上游接入点，请先前往「上游接入点」创建后再保存模型。
        </p>

        <!-- 1. 基本信息 -->
        <div class="form-grid">
          <div class="field">
            <label for="model-name">对外模型名</label>
            <input id="model-name" v-model="editing.name" placeholder="例如 claude-sonnet-4-5" />
          </div>
          <div class="field">
            <span class="field-caption">调度策略</span>
            <div class="row" style="min-height: 36px">
              <span class="badge ok">failover</span>
              <span class="field-hint">主节点失败自动切到下一候选</span>
            </div>
          </div>
        </div>

        <!-- 2. 候选路由链（核心必填前置） -->
        <div class="form-section">
          <div class="row space-between">
            <div>
              <span class="form-section-title">候选接入点链路</span>
              <p class="field-hint">优先级数字越小越优先，失焦后按生效顺序自动排列。</p>
            </div>
            <button type="button" class="ghost sm" @click="addCandidate(editing)">
              <AppIcon name="plus" :size="13" />
              <span>添加候选</span>
            </button>
          </div>

          <div v-for="(row, index) in editing.candidates" :key="index" class="subcard">
            <div class="row space-between">
              <div class="row">
                <span class="badge" :class="index === 0 ? 'ok' : 'off'">
                  {{ index === 0 ? '主节点' : `备用 #${index}` }}
                </span>
                <span v-if="row.windows.length" class="badge accent">
                  {{ row.windows.length }} 条时段规则
                </span>
              </div>
              <button
                type="button"
                class="ghost danger sm"
                :disabled="editing.candidates.length <= 1"
                @click="removeCandidate(editing, index)"
              >
                移除
              </button>
            </div>

            <div class="form-grid" style="grid-template-columns: 1fr 1fr 110px">
              <div class="field">
                <label :for="`model-candidate-instance-${index}`">上游接入点</label>
                <select
                  :id="`model-candidate-instance-${index}`"
                  v-model="row.instance"
                  @change="onInstanceChange(row)"
                >
                  <option value="" disabled>请选择接入点</option>
                  <option v-for="inst in instances" :key="inst.name" :value="inst.name">
                    {{ optionLabel(inst) }}
                  </option>
                </select>
              </div>

              <div class="field">
                <label :for="`model-candidate-upstream-${index}`">上游模型名</label>
                <select
                  :id="`model-candidate-upstream-${index}`"
                  v-model="row.upstream"
                  :disabled="!row.instance"
                >
                  <option value="" disabled>请选择模型</option>
                  <option v-for="name in instanceModels(row.instance)" :key="name" :value="name">
                    {{ name }}
                  </option>
                </select>
              </div>

              <div class="field">
                <label :for="`model-candidate-priority-${index}`">基础优先级</label>
                <input
                  :id="`model-candidate-priority-${index}`"
                  v-model="row.priority"
                  type="text"
                  inputmode="numeric"
                  placeholder="0"
                  @change="onPriorityChange"
                />
              </div>
            </div>

            <p v-if="row.instance && !instanceModels(row.instance).length" class="error">
              接入点「{{ row.instance }}」尚未声明任何模型，请先到「上游接入点」页补充。
            </p>

            <!-- 分时段规则：无规则时不占大块虚线框，按需展开 -->
            <div v-if="row.windows.length" class="stack" style="gap: 8px; padding-top: 6px">
              <span class="field-hint">
                分时段规则（24 小时制 HH:MM，起始晚于结束视为跨天；按 config.yaml schedule.timezone
                判定）
              </span>
              <div v-for="(w, wi) in row.windows" :key="wi" class="row">
                <label class="visually-hidden" :for="`model-window-start-${index}-${wi}`">
                  窗口开始时间
                </label>
                <input
                  :id="`model-window-start-${index}-${wi}`"
                  v-model="w.start"
                  type="time"
                  style="width: 112px"
                />
                <span class="muted">至</span>
                <label class="visually-hidden" :for="`model-window-end-${index}-${wi}`">
                  窗口结束时间
                </label>
                <input
                  :id="`model-window-end-${index}-${wi}`"
                  v-model="w.end"
                  type="time"
                  style="width: 112px"
                />
                <label class="visually-hidden" :for="`model-window-priority-${index}-${wi}`">
                  窗口内优先级
                </label>
                <input
                  :id="`model-window-priority-${index}-${wi}`"
                  v-model="w.priority"
                  type="text"
                  inputmode="numeric"
                  placeholder="窗口优先级"
                  style="width: 108px"
                />
                <label class="row small nowrap" :for="`model-window-only-${index}-${wi}`">
                  <input
                    :id="`model-window-only-${index}-${wi}`"
                    v-model="w.only"
                    type="checkbox"
                  />
                  <span>仅此时段可用</span>
                </label>
                <button type="button" class="ghost danger sm" @click="removeWindow(row, wi)">
                  删除
                </button>
              </div>
            </div>

            <div class="row">
              <button type="button" class="subtle sm" @click="addWindow(row)">
                <AppIcon name="plus" :size="13" />
                <span>添加分时段规则</span>
              </button>
            </div>
          </div>
        </div>

        <!-- 3. 模型元数据（可选，默认折叠） -->
        <details class="form-collapse" :open="hasCustomMetadata">
          <summary>
            <span>模型元数据（可选）</span>
            <span class="badge" :class="hasCustomMetadata ? 'accent' : 'off'">
              {{ hasCustomMetadata ? '已自定义' : '留空回退内置预设' }}
            </span>
          </summary>
          <div class="collapse-body">
            <p class="field-hint">
              通过 <code>/openai/v1/models</code> 与 <code>/anthropic/v1/models</code>
              返回给客户端；留空字段自动回退到内置官方参数。
            </p>

            <div class="form-grid">
              <div class="field">
                <label for="model-metadata-display-name">显示名称</label>
                <input
                  id="model-metadata-display-name"
                  v-model="editing.metadata.displayName"
                  placeholder="例如 GLM-5.3"
                />
              </div>

              <div class="field">
                <label for="model-metadata-context">上下文长度（tokens）</label>
                <input
                  id="model-metadata-context"
                  v-model="editing.metadata.contextLength"
                  type="text"
                  inputmode="numeric"
                  placeholder="例如 200000"
                />
              </div>

              <div class="field">
                <label for="model-metadata-max-output">最大输出（tokens）</label>
                <input
                  id="model-metadata-max-output"
                  v-model="editing.metadata.maxOutputTokens"
                  type="text"
                  inputmode="numeric"
                  placeholder="例如 64000"
                />
              </div>

              <div class="field span-2">
                <label for="model-metadata-capabilities">能力标签（逗号分隔或点击下方快选）</label>
                <input
                  id="model-metadata-capabilities"
                  v-model="editing.metadata.capabilities"
                  placeholder="tools, reasoning, vision"
                />
                <div class="tag-list" style="margin-top: 4px">
                  <button
                    v-for="cap in RECOMMENDED_CAPABILITIES"
                    :key="cap"
                    type="button"
                    class="chip-btn"
                    :class="{ active: hasCapability(editing.metadata.capabilities, cap) }"
                    @click="toggleCapability(cap)"
                  >
                    {{ cap }}
                  </button>
                </div>
              </div>
            </div>

            <div class="stack" style="gap: 8px">
              <span class="field-caption">按协议覆盖（留空继承通用值）</span>
              <details v-for="protocol in protocolNames" :key="protocol" class="form-collapse">
                <summary>
                  <span>{{ protocolLabels[protocol] }} 协议覆盖</span>
                  <span class="badge" :class="protocol">{{ protocol }}</span>
                </summary>
                <div class="collapse-body">
                  <div class="form-grid">
                    <div class="field">
                      <label :for="`model-metadata-${protocol}-context`"
                        >上下文长度（tokens）</label
                      >
                      <input
                        :id="`model-metadata-${protocol}-context`"
                        v-model="editing.metadata.protocols[protocol].contextLength"
                        type="text"
                        inputmode="numeric"
                        placeholder="继承"
                      />
                    </div>
                    <div class="field">
                      <label :for="`model-metadata-${protocol}-max-output`"
                        >最大输出（tokens）</label
                      >
                      <input
                        :id="`model-metadata-${protocol}-max-output`"
                        v-model="editing.metadata.protocols[protocol].maxOutputTokens"
                        type="text"
                        inputmode="numeric"
                        placeholder="继承"
                      />
                    </div>
                    <div class="field span-2">
                      <label :for="`model-metadata-${protocol}-capabilities`">能力标签</label>
                      <input
                        :id="`model-metadata-${protocol}-capabilities`"
                        v-model="editing.metadata.protocols[protocol].capabilities"
                        placeholder="继承"
                      />
                    </div>
                  </div>
                </div>
              </details>
            </div>
          </div>
        </details>
      </form>

      <template #footer>
        <p v-if="error" class="error">{{ error }}</p>
        <div class="row space-between">
          <button type="button" class="ghost" @click="editing = null">取消</button>
          <button type="submit" form="model-form" class="primary" :disabled="saving">
            {{ saving ? '保存中…' : '保存模型' }}
          </button>
        </div>
      </template>
    </BaseModal>
  </section>
</template>
