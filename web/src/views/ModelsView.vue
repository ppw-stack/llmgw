<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue';
import { api } from '../api';
import type { Instance, Model, ModelInstance, ModelPayload, TimeWindow } from '../types';
import { confirmAction } from '../utils/confirm';
import { errorMessage } from '../utils/error';

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

/** 新增 / 编辑模型的表单状态。 */
interface ModelForm {
  id: number | null;
  name: string;
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

/** 下拉框里的接入点标签，顺带展示它声明的模型。 */
function optionLabel(inst: Instance): string {
  if (!inst.models.length) return `${inst.name}（未声明模型）`;
  return `${inst.name}（支持：${inst.models.join('、')}）`;
}

/** 按优先级排序的候选链，排在最前面的是主接入点。 */
function sortedCandidates(m: Model): ModelInstance[] {
  return [...m.instances].sort((a, b) => a.priority - b.priority);
}

/**
 * 当前生效的候选顺序。服务端已经算好，这里只负责展示：
 * 在线的候选按尝试顺序排列，不在时段内的候选排在最后。
 */
function effectiveSummary(m: Model): string {
  const list = m.effective_candidates ?? [];
  if (!list.length) return '';
  const active = list.filter((c) => c.active);
  const idle = list.filter((c) => !c.active);
  const parts = active.map((c) => {
    const hit = c.window ? `（${c.window} 时段内，优先级 ${c.priority}）` : '';
    return `${c.instance} / ${c.upstream}${hit}`;
  });
  for (const c of idle) {
    parts.push(`${c.instance} / ${c.upstream}（当前不在时段内）`);
  }
  return parts.join(' → ');
}

/** 候选上的时间窗口摘要，用于列表展示。 */
function windowSummary(candidate: ModelInstance): string {
  if (!candidate.windows?.length) return '';
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
    candidates: [{ instance: '', upstream: '', priority: '0', windows: [] }],
  };
}

function addCandidate(form: ModelForm) {
  form.candidates.push({ instance: '', upstream: '', priority: '0', windows: [] });
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
  if (editing.value) sortCandidates(editing.value);
}

function removeCandidate(form: ModelForm, index: number) {
  if (form.candidates.length <= 1) return;
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
  if (text === '') return null;
  const value = Number(text);
  if (!Number.isInteger(value) || value < 0) return null;
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
    // 刷新失败时保留旧数据，不打断正在查看的内容；但要在页面上留一条提示，
    // 否则时段切换导致的展示异常无从排查。
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
    candidates: rows.length ? rows : [{ instance: '', upstream: '', priority: '0', windows: [] }],
  };
  sortCandidates(form);
  editing.value = form;
}

async function save() {
  const f = editing.value;
  if (!f) return;

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
      if (!start && !end && !w.priority.trim() && !w.only) continue;
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

  saving.value = true;
  error.value = '';
  try {
    if (f.id === null) {
      await api<Model>('/models', { method: 'POST', body: JSON.stringify(payload) });
    } else {
      await api<Model>(`/models/${f.id}`, { method: 'PUT', body: JSON.stringify(payload) });
    }
    editing.value = null;
    await load();
  } catch (e) {
    error.value = errorMessage(e);
  } finally {
    saving.value = false;
  }
}

async function remove(m: Model) {
  if (!confirmAction(`删除模型「${m.name}」？使用该模型的客户端请求会立即失败。`)) return;
  error.value = '';
  try {
    await api<null>(`/models/${m.id}`, { method: 'DELETE' });
    await load();
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
  <section>
    <div class="section-head">
      <h2>模型</h2>
      <div class="row">
        <button class="ghost" @click="load">刷新</button>
        <button class="primary" @click="openCreate">新增模型</button>
      </div>
    </div>
    <p class="muted">
      模型是对外暴露的名字，由一个或多个「接入点 +
      该接入点中的具体模型」组成候选链；主接入点失败后自动切换到下一个。
    </p>
    <p v-if="!instances.length" class="muted small">
      还没有上游接入点，请先在「上游接入点」页面创建一个，再来配置模型。
    </p>
    <p v-if="error" class="error">{{ error }}</p>
    <p v-if="refreshError" class="muted small">
      自动刷新失败：{{ refreshError }}（仍显示上一次的数据）
    </p>
    <p v-if="loading" class="muted">加载中…</p>

    <div v-else-if="!models.length" class="card muted">还没有模型。点击「新增模型」开始配置。</div>

    <div v-for="m in models" :key="m.id" class="card provider">
      <div class="provider-head">
        <div class="row">
          <h3>{{ m.name }}</h3>
          <span class="badge">{{ m.strategy }}</span>
        </div>
        <div class="row">
          <button class="ghost" @click="openEdit(m)">编辑</button>
          <button class="ghost danger" @click="remove(m)">删除</button>
        </div>
      </div>

      <p v-if="effectiveSummary(m)" class="small">
        <span class="muted">当前顺序（{{ m.timezone }}）：</span>
        <span>{{ effectiveSummary(m) }}</span>
      </p>

      <div class="endpoint-list">
        <div v-for="(c, index) in sortedCandidates(m)" :key="c.instance" class="endpoint-line">
          <span class="badge" :class="index === 0 ? 'ok' : 'off'">
            {{ index === 0 ? '主' : '备' }}
          </span>
          <code class="base">{{ c.instance }}</code>
          <span class="muted small">/</span>
          <code class="base">{{ c.upstream }}</code>
          <span class="muted small">优先级 {{ c.priority }}</span>
          <span v-if="windowSummary(c)" class="muted small"> 时段：{{ windowSummary(c) }} </span>
        </div>
      </div>
    </div>

    <div v-if="editing" class="modal-backdrop" role="presentation" @click.self="editing = null">
      <div class="card modal wide">
        <div class="row space-between">
          <h3>{{ editing.id === null ? '新增模型' : '编辑模型' }}</h3>
          <button class="ghost" @click="editing = null">关闭</button>
        </div>

        <div class="form-grid">
          <div class="field span-2">
            <label for="model-name">对外模型名</label>
            <input id="model-name" v-model="editing.name" placeholder="例如 claude-sonnet-4-5" />
          </div>

          <div class="field span-2 readonly">
            <span class="field-caption">调度策略</span>
            <div class="row">
              <span class="badge ok">failover</span>
              <span class="muted small">
                当前版本只支持 failover：主接入点失败后自动切换到下一个候选接入点。
              </span>
            </div>
            <p class="muted small">
              时间窗口按 config.yaml 里的 schedule.timezone 判定（默认 Asia/Shanghai）。
            </p>
          </div>

          <div class="field span-2">
            <span class="field-caption">
              候选接入点（优先级数字越小越优先，主接入点失败后自动切换到下一个）
            </span>
            <div v-for="(row, index) in editing.candidates" :key="index">
              <div class="row">
                <label class="visually-hidden" :for="`model-candidate-instance-${index}`">
                  上游接入点
                </label>
                <select
                  :id="`model-candidate-instance-${index}`"
                  v-model="row.instance"
                  class="input-grow"
                  @change="onInstanceChange(row)"
                >
                  <option value="" disabled>请选择接入点</option>
                  <option v-for="inst in instances" :key="inst.name" :value="inst.name">
                    {{ optionLabel(inst) }}
                  </option>
                </select>
                <label class="visually-hidden" :for="`model-candidate-upstream-${index}`">
                  接入点中的模型
                </label>
                <select
                  :id="`model-candidate-upstream-${index}`"
                  v-model="row.upstream"
                  class="input-grow"
                  :disabled="!row.instance"
                >
                  <option value="" disabled>请选择模型</option>
                  <option v-for="name in instanceModels(row.instance)" :key="name" :value="name">
                    {{ name }}
                  </option>
                </select>
                <label class="visually-hidden" :for="`model-candidate-priority-${index}`"
                  >优先级</label
                >
                <input
                  :id="`model-candidate-priority-${index}`"
                  v-model="row.priority"
                  class="input-num"
                  type="text"
                  inputmode="numeric"
                  placeholder="优先级"
                  @change="onPriorityChange"
                />
                <button
                  class="ghost danger"
                  :disabled="editing.candidates.length <= 1"
                  @click="removeCandidate(editing, index)"
                >
                  删除
                </button>
              </div>
              <p v-if="row.instance && !instanceModels(row.instance).length" class="error small">
                接入点「{{
                  row.instance
                }}」还没有声明任何模型，请先到「上游接入点」页补充「支持的模型」。
              </p>

              <div class="window-editor">
                <p class="muted small">
                  时间窗口：命中时该候选按窗口内优先级插队，或（勾选「仅窗口内可用」）只在窗口内参与路由。
                  时段按调度时区判定，起止时间用 24 小时制 HH:MM，起晚于止视为跨天。
                </p>
                <div v-for="(w, wi) in row.windows" :key="wi" class="row">
                  <label class="visually-hidden" :for="`model-window-start-${index}-${wi}`">
                    窗口开始时间
                  </label>
                  <input
                    :id="`model-window-start-${index}-${wi}`"
                    v-model="w.start"
                    class="input-num"
                    type="time"
                  />
                  <label class="visually-hidden" :for="`model-window-end-${index}-${wi}`">
                    窗口结束时间
                  </label>
                  <input
                    :id="`model-window-end-${index}-${wi}`"
                    v-model="w.end"
                    class="input-num"
                    type="time"
                  />
                  <label class="visually-hidden" :for="`model-window-priority-${index}-${wi}`">
                    窗口内优先级
                  </label>
                  <input
                    :id="`model-window-priority-${index}-${wi}`"
                    v-model="w.priority"
                    class="input-num"
                    type="text"
                    inputmode="numeric"
                    placeholder="窗口内优先级"
                  />
                  <label class="nowrap" :for="`model-window-only-${index}-${wi}`">
                    仅窗口内可用
                  </label>
                  <input
                    :id="`model-window-only-${index}-${wi}`"
                    v-model="w.only"
                    type="checkbox"
                  />
                  <button class="ghost danger" @click="removeWindow(row, wi)">删除</button>
                </div>
                <div class="row">
                  <button class="ghost" @click="addWindow(row)">添加时间窗口</button>
                </div>
              </div>
            </div>
            <div class="row">
              <button class="ghost" @click="addCandidate(editing)">添加候选</button>
            </div>
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
