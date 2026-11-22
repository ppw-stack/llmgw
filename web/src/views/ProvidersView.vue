<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { api } from '../api';
import type { Provider, ProviderPayload, ProviderPreset } from '../types';
import { confirmAction } from '../utils/confirm';
import { errorMessage } from '../utils/error';

type ProviderMode = 'builtin' | 'custom';

interface ProviderForm {
  id: number | null;
  mode: ProviderMode;
  name: string;
  preset: string;
  apiKey: string;
  keyHint: string;
  modelsText: string;
  openaiBaseURL: string;
  anthropicBaseURL: string;
}

const providers = ref<Provider[]>([]);
const presets = ref<ProviderPreset[]>([]);
const loading = ref(true);
const error = ref('');
const editing = ref<ProviderForm | null>(null);
const saving = ref(false);

function presetLabel(p: ProviderPreset): string {
  return p.variant_name ? `${p.family_name}（${p.variant_name}）` : p.family_name;
}

function findPreset(id: string): ProviderPreset | null {
  return presets.value.find((p) => p.id === id) ?? null;
}

const selectedPreset = computed<ProviderPreset | null>(() => {
  const form = editing.value;
  if (form?.mode !== 'builtin') return null;
  return findPreset(form.preset);
});

function modelsPlaceholder(): string {
  const preset = selectedPreset.value;
  if (editing.value?.mode === 'builtin' && preset) {
    return preset.models.join('\n');
  }
  return 'glm-4.6\nclaude-sonnet-4-5=glm-4.6';
}

function newForm(): ProviderForm {
  const first = presets.value[0];
  const form: ProviderForm = {
    id: null,
    mode: presets.value.length ? 'builtin' : 'custom',
    name: '',
    preset: first ? first.id : '',
    apiKey: '',
    keyHint: '',
    modelsText: '',
    openaiBaseURL: '',
    anthropicBaseURL: '',
  };
  applyPreset(form, form.preset);
  return form;
}

function applyPreset(form: ProviderForm, id: string) {
  form.preset = id;
  const preset = findPreset(id);
  if (!preset) return;
  if (!form.name.trim()) form.name = preset.default_name;
  if (!form.modelsText.trim()) form.modelsText = preset.models.join('\n');
}

function choosePreset(id: string) {
  const form = editing.value;
  if (!form) return;
  form.modelsText = '';
  applyPreset(form, id);
}

function onPresetChange(event: Event) {
  const target = event.target;
  if (!(target instanceof HTMLSelectElement)) return;
  choosePreset(target.value);
}

function setMode(mode: ProviderMode) {
  const form = editing.value;
  if (!form) return;
  form.mode = mode;
  if (mode === 'builtin') {
    applyPreset(form, form.preset || presets.value[0]?.id || '');
  }
}

async function loadPresets() {
  try {
    presets.value = await api<ProviderPreset[]>('/provider-presets');
  } catch {
    presets.value = [];
  }
}

async function load() {
  loading.value = true;
  error.value = '';
  try {
    providers.value = await api<Provider[]>('/providers');
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

function openEdit(p: Provider) {
  error.value = '';
  editing.value = {
    id: p.id,
    mode: p.source === 'builtin' ? 'builtin' : 'custom',
    name: p.name,
    preset: p.preset ?? '',
    apiKey: '',
    keyHint: p.key_hint,
    modelsText: p.models.join('\n'),
    openaiBaseURL: p.openai_base_url,
    anthropicBaseURL: p.anthropic_base_url,
  };
}

function modelLines(text: string): string[] {
  return text
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean);
}

async function save() {
  const f = editing.value;
  if (!f) return;
  const models = modelLines(f.modelsText);
  if (!f.name.trim()) {
    error.value = '请填写供应商名称';
    return;
  }
  if (f.mode === 'custom') {
    if (!f.openaiBaseURL.trim() && !f.anthropicBaseURL.trim()) {
      error.value = '至少填写一个 base URL（OpenAI 或 Anthropic）';
      return;
    }
    if (!models.length) {
      error.value = '请至少填写一个模型';
      return;
    }
  }
  if (f.id === null && !f.apiKey.trim()) {
    error.value = '请填写上游 API Key';
    return;
  }

  const payload: ProviderPayload = {
    name: f.name.trim(),
    models,
    preset: '',
  };
  if (f.mode === 'builtin') {
    payload.preset = f.preset;
  } else {
    payload.openai_base_url = f.openaiBaseURL.trim();
    payload.anthropic_base_url = f.anthropicBaseURL.trim();
  }
  if (f.id === null || f.apiKey.trim()) {
    payload.api_key = f.apiKey.trim();
  }

  saving.value = true;
  error.value = '';
  try {
    if (f.id === null) {
      await api<Provider>('/providers', { method: 'POST', body: JSON.stringify(payload) });
    } else {
      await api<Provider>(`/providers/${f.id}`, {
        method: 'PUT',
        body: JSON.stringify(payload),
      });
    }
    editing.value = null;
    await load();
  } catch (e) {
    error.value = errorMessage(e);
  } finally {
    saving.value = false;
  }
}

async function remove(p: Provider) {
  if (!confirmAction(`删除供应商「${p.name}」？它的模型会立即从路由中移除。`)) return;
  error.value = '';
  try {
    await api<null>(`/providers/${p.id}`, { method: 'DELETE' });
    await load();
  } catch (e) {
    error.value = errorMessage(e);
  }
}

onMounted(async () => {
  await loadPresets();
  await load();
});
</script>

<template>
  <section>
    <div class="section-head">
      <h2>供应商</h2>
      <div class="row">
        <button class="ghost" @click="load">刷新</button>
        <button class="primary" @click="openCreate">新增供应商</button>
      </div>
    </div>
    <p class="muted">
      内置端点只需填 API Key；自定义端点手填两个 base URL。上游 Key 加密存放，保存后立即生效。
    </p>
    <p v-if="error" class="error">{{ error }}</p>
    <p v-if="loading" class="muted">加载中…</p>

    <div v-else-if="!providers.length" class="card muted">
      还没有供应商。点击「新增供应商」开始配置。
    </div>

    <div v-for="p in providers" :key="p.id" class="card provider">
      <div class="provider-head">
        <div class="row">
          <h3>{{ p.name }}</h3>
          <span class="badge" :class="{ builtin: p.source === 'builtin' }">
            {{ p.source === 'builtin' ? '内置端点' : '自定义' }}
          </span>
          <span v-if="p.preset_name" class="badge">{{ p.preset_name }}</span>
        </div>
        <div class="row">
          <a
            v-if="p.docs_url"
            class="ghost link"
            :href="p.docs_url"
            target="_blank"
            rel="noreferrer"
            >文档</a
          >
          <button class="ghost" @click="openEdit(p)">编辑</button>
          <button class="ghost danger" @click="remove(p)">删除</button>
        </div>
      </div>

      <div v-if="p.key_hint" class="key-hints">
        <code>{{ p.key_hint }}</code>
      </div>

      <div class="endpoint-list">
        <div v-if="p.openai_base_url" class="endpoint-line">
          <span class="badge openai">openai</span>
          <code class="base">{{ p.openai_base_url }}</code>
        </div>
        <div v-if="p.anthropic_base_url" class="endpoint-line">
          <span class="badge anthropic">anthropic</span>
          <code class="base">{{ p.anthropic_base_url }}</code>
        </div>
      </div>

      <div class="models">
        <span v-for="model in p.models" :key="model" class="tag">{{ model }}</span>
      </div>
    </div>

    <div v-if="editing" class="modal-backdrop" role="presentation" @click.self="editing = null">
      <div class="card modal wide">
        <div class="row space-between">
          <h3>{{ editing.id === null ? '新增供应商' : '编辑供应商' }}</h3>
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

        <div class="form-grid">
          <div v-if="editing.mode === 'builtin'" class="field span-2">
            <label for="provider-preset">内置端点</label>
            <select
              id="provider-preset"
              :value="editing.preset"
              :disabled="editing.id !== null"
              @change="onPresetChange"
            >
              <option v-for="preset in presets" :key="preset.id" :value="preset.id">
                {{ presetLabel(preset) }}
              </option>
            </select>
            <p v-if="selectedPreset" class="muted small">
              两个 base URL 由程序内置，随版本升级自动更新。
              <a :href="selectedPreset.docs_url" target="_blank" rel="noreferrer">官方文档</a>
            </p>
          </div>

          <div class="field">
            <label for="provider-name">名称</label>
            <input id="provider-name" v-model="editing.name" placeholder="例如 minimax" />
          </div>
          <div class="field">
            <label for="provider-api-key">
              上游 API Key
              <span v-if="editing.id !== null && editing.keyHint" class="muted small">
                留空保留现有 {{ editing.keyHint }}
              </span>
            </label>
            <input
              id="provider-api-key"
              v-model="editing.apiKey"
              type="password"
              autocomplete="off"
              :placeholder="editing.id === null ? 'sk-...' : '留空则不修改'"
            />
          </div>

          <div v-if="editing.mode === 'builtin' && selectedPreset" class="field span-2 readonly">
            <span class="field-caption">Base URL（只读）</span>
            <div class="endpoint-line">
              <span class="badge openai">openai</span>
              <code class="base">{{ selectedPreset.openai_base_url }}</code>
            </div>
            <div class="endpoint-line">
              <span class="badge anthropic">anthropic</span>
              <code class="base">{{ selectedPreset.anthropic_base_url }}</code>
            </div>
          </div>

          <template v-if="editing.mode === 'custom'">
            <div class="field span-2">
              <label for="provider-openai-base"
                >OpenAI Base URL（通常以 <code>/v1</code> 结尾）</label
              >
              <input
                id="provider-openai-base"
                v-model="editing.openaiBaseURL"
                placeholder="https://api.example.com/v1"
              />
            </div>
            <div class="field span-2">
              <label for="provider-anthropic-base"
                >Anthropic Base URL（不带 <code>/v1</code>）</label
              >
              <input
                id="provider-anthropic-base"
                v-model="editing.anthropicBaseURL"
                placeholder="https://api.example.com/anthropic"
              />
            </div>
          </template>

          <div class="field span-2">
            <label for="provider-models">模型（每行一个，可用 <code>对外名=上游名</code>）</label>
            <textarea
              id="provider-models"
              v-model="editing.modelsText"
              rows="3"
              :placeholder="modelsPlaceholder()"
            ></textarea>
            <p v-if="editing.mode === 'builtin' && selectedPreset" class="muted small">
              留空或删除此行时使用内置默认模型；也能添加
              <code>claude-sonnet-4-5={{ selectedPreset.models[0] ?? '' }}</code> 之类的别名。
            </p>
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
