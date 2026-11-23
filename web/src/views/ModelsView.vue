<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue';
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

/** 表单里的一行候选接入点。 */
interface CandidateRow {
  instance: string;
  priority: string;
  windows: WindowRow[];
}

/** 新增 / 编辑模型的表单状态。 */
interface ModelForm {
  id: number | null;
  name: string;
  upstream: string;
  candidates: CandidateRow[];
}

const models = ref<Model[]>([]);
const instances = ref<Instance[]>([]);
const loading = ref(true);
const error = ref('');
const refreshError = ref('');
const editing = ref<ModelForm | null>(null);
const saving = ref(false);

/** 当前模型真正发给上游的名字：留空时与对外名相同。 */
const currentUpstream = computed<string>(() => {
  const form = editing.value;
  if (!form) return '';
  return form.upstream.trim() || form.name.trim();
});

/** 接入点是否声明支持该上游模型名。未填上游名时不限制。 */
function supports(inst: Instance, upstream: string): boolean {
  return upstream === '' || inst.models.includes(upstream);
}

/**
 * 当前表单是否无解：没有接入点，或没有任何接入点声明支持这个上游名。
 * 这两种情况下候选下拉框会整体不可选，必须给出明确提示。
 */
const missingSupport = computed<boolean>(() => {
  if (!instances.value.length) return true;
  const upstream = currentUpstream.value;
  if (!upstream) return false;
  return !instances.value.some((inst) => inst.models.includes(upstream));
});

function instanceByName(name: string): Instance | null {
  return instances.value.find((i) => i.name === name) ?? null;
}

/** 该候选接入点是否与当前上游名不匹配。 */
function candidateUnsupported(name: string): boolean {
  const inst = instanceByName(name);
  if (!inst) return false;
  return !supports(inst, currentUpstream.value);
}

/** 列表页用：某个候选接入点是否声明支持该模型的上游名。 */
function modelCandidateUnsupported(m: Model, instanceName: string): boolean {
  const inst = instanceByName(instanceName);
  if (!inst) return true;
  return !inst.models.includes(m.upstream);
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
    return `${c.instance}${hit}`;
  });
  for (const c of idle) {
    parts.push(`${c.instance}（当前不在时段内）`);
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
    upstream: '',
    candidates: [{ instance: '', priority: '0', windows: [] }],
  };
}

function addCandidate(form: ModelForm) {
  form.candidates.push({ instance: '', priority: '0', windows: [] });
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
    priority: String(mi.priority),
    windows: (mi.windows ?? []).map((w) => ({
      start: w.start,
      end: w.end,
      priority: w.priority === null || w.priority === undefined ? '' : String(w.priority),
      only: Boolean(w.only),
    })),
  }));
  editing.value = {
    id: m.id,
    name: m.name,
    upstream: m.upstream,
    candidates: rows.length ? rows : [{ instance: '', priority: '0', windows: [] }],
  };
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
    list.push({ instance: target, priority, windows });
  }
  if (!list.length) {
    error.value = '至少需要一个候选接入点';
    return;
  }

  const payload: ModelPayload = {
    name,
    upstream: f.upstream.trim() || name,
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
      模型是对外暴露的名字，由一个或多个上游接入点组成候选链；主接入点失败后自动切换到下一个。
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

      <div class="row small muted">
        <span>上游模型名：{{ m.upstream }}</span>
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
          <span class="muted small">优先级 {{ c.priority }}</span>
          <span v-if="windowSummary(c)" class="muted small"> 时段：{{ windowSummary(c) }} </span>
          <span v-if="modelCandidateUnsupported(m, c.instance)" class="badge off">
            未声明支持 {{ m.upstream }}
          </span>
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
          <div class="field">
            <label for="model-name">对外模型名</label>
            <input id="model-name" v-model="editing.name" placeholder="例如 claude-sonnet-4-5" />
          </div>
          <div class="field">
            <label for="model-upstream">上游模型名</label>
            <input
              id="model-upstream"
              v-model="editing.upstream"
              placeholder="留空则与对外模型名相同"
            />
            <p class="muted small">
              这里填的是真正发给上游的模型名，候选接入点的「支持的模型」必须包含它。
            </p>
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
            <p v-if="missingSupport" class="error small">
              <template v-if="!instances.length">
                还没有上游接入点，请先到「上游接入点」页创建一个。
              </template>
              <template v-else>
                没有接入点声明支持
                <code>{{
                  currentUpstream
                }}</code>，请先到「上游接入点」页补充它的「支持的模型」，或把上游模型名改成已声明的名字。
              </template>
            </p>
            <div v-for="(row, index) in editing.candidates" :key="index">
              <div class="row">
                <label class="visually-hidden" :for="`model-candidate-instance-${index}`">
                  上游接入点
                </label>
                <select
                  :id="`model-candidate-instance-${index}`"
                  v-model="row.instance"
                  class="input-grow"
                >
                  <option value="" disabled>请选择接入点</option>
                  <option
                    v-for="inst in instances"
                    :key="inst.name"
                    :value="inst.name"
                    :disabled="!supports(inst, currentUpstream) && row.instance !== inst.name"
                  >
                    {{ optionLabel(inst) }}
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
                />
                <button
                  class="ghost danger"
                  :disabled="editing.candidates.length <= 1"
                  @click="removeCandidate(editing, index)"
                >
                  删除
                </button>
              </div>
              <p v-if="row.instance && candidateUnsupported(row.instance)" class="error small">
                该接入点未声明支持
                {{ currentUpstream }}，请先到「上游接入点」页补充声明，或换一个候选。
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
