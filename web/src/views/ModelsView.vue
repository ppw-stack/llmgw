<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { api } from '../api';
import type { Instance, Model, ModelInstance, ModelPayload } from '../types';
import { confirmAction } from '../utils/confirm';
import { errorMessage } from '../utils/error';

/** 表单里的一行候选接入点。 */
interface CandidateRow {
  instance: string;
  priority: string;
  weight: string;
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
const editing = ref<ModelForm | null>(null);
const saving = ref(false);

/** 可以被模型引用的上游接入点名称。 */
const instanceNames = computed<string[]>(() => instances.value.map((i) => i.name));

/** 按优先级排序的候选链，排在最前面的是主接入点。 */
function sortedCandidates(m: Model): ModelInstance[] {
  return [...m.instances].sort((a, b) => a.priority - b.priority);
}

function newForm(): ModelForm {
  const first = instances.value[0];
  return {
    id: null,
    name: '',
    upstream: '',
    candidates: [{ instance: first ? first.name : '', priority: '0', weight: '100' }],
  };
}

function addCandidate(form: ModelForm) {
  form.candidates.push({ instance: '', priority: '0', weight: '100' });
}

function removeCandidate(form: ModelForm, index: number) {
  if (form.candidates.length <= 1) return;
  form.candidates.splice(index, 1);
}

/** 解析表单里的非负整数，非法时返回 null。 */
function parseCount(raw: string): number | null {
  const text = raw.trim();
  if (text === '') return null;
  const value = Number(text);
  if (!Number.isInteger(value) || value < 0) return null;
  return value;
}

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

function openCreate() {
  error.value = '';
  editing.value = newForm();
}

function openEdit(m: Model) {
  error.value = '';
  const rows = m.instances.map((mi) => ({
    instance: mi.instance,
    priority: String(mi.priority),
    weight: String(mi.weight),
  }));
  editing.value = {
    id: m.id,
    name: m.name,
    upstream: m.upstream,
    candidates: rows.length ? rows : [{ instance: '', priority: '0', weight: '100' }],
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
    const weight = parseCount(row.weight);
    if (priority === null) {
      error.value = '优先级必须是非负整数';
      return;
    }
    if (weight === null || weight === 0) {
      error.value = '权重必须是正整数';
      return;
    }
    list.push({ instance: target, priority, weight });
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

onMounted(() => {
  void load();
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

      <div class="endpoint-list">
        <div v-for="(c, index) in sortedCandidates(m)" :key="c.instance" class="endpoint-line">
          <span class="badge" :class="index === 0 ? 'ok' : 'off'">
            {{ index === 0 ? '主' : '备' }}
          </span>
          <code class="base">{{ c.instance }}</code>
          <span class="muted small">优先级 {{ c.priority }} · 权重 {{ c.weight }}</span>
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
          </div>

          <div class="field span-2 readonly">
            <span class="field-caption">调度策略</span>
            <div class="row">
              <span class="badge ok">failover</span>
              <span class="muted small">
                当前版本只支持 failover：主接入点失败后自动切换到下一个候选接入点。
              </span>
            </div>
          </div>

          <div class="field span-2">
            <span class="field-caption">
              候选接入点（优先级数字越小越优先，主接入点失败后自动切换到下一个）
            </span>
            <div v-for="(row, index) in editing.candidates" :key="index" class="row">
              <label class="visually-hidden" :for="`model-candidate-instance-${index}`">
                上游接入点
              </label>
              <select
                :id="`model-candidate-instance-${index}`"
                v-model="row.instance"
                class="input-grow"
              >
                <option value="" disabled>请选择接入点</option>
                <option v-for="option in instanceNames" :key="option" :value="option">
                  {{ option }}
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
              <label class="visually-hidden" :for="`model-candidate-weight-${index}`">权重</label>
              <input
                :id="`model-candidate-weight-${index}`"
                v-model="row.weight"
                class="input-num"
                type="text"
                inputmode="numeric"
                placeholder="权重"
              />
              <button
                class="ghost danger"
                :disabled="editing.candidates.length <= 1"
                @click="removeCandidate(editing, index)"
              >
                删除
              </button>
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
