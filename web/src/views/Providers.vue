<script setup>
import { computed, onMounted, ref } from 'vue'
import { api } from '../api'

const providers = ref([])
const presets = ref([])
const loading = ref(true)
const error = ref('')
const editing = ref(null)
const saving = ref(false)

function presetLabel(p) {
  return p.variant_name ? `${p.family_name}（${p.variant_name}）` : p.family_name
}

function findPreset(id) {
  return presets.value.find((p) => p.id === id) || null
}

const selectedPreset = computed(() => {
  if (!editing.value || editing.value.mode !== 'builtin') return null
  return findPreset(editing.value.preset)
})

function modelsPlaceholder() {
  if (editing.value?.mode === 'builtin' && selectedPreset.value) {
    return selectedPreset.value.models.join('\n')
  }
  return 'glm-4.6\nclaude-sonnet-4-5=glm-4.6'
}

function newForm() {
  const form = {
    id: null,
    mode: presets.value.length ? 'builtin' : 'custom',
    name: '',
    preset: presets.value.length ? presets.value[0].id : '',
    apiKey: '',
    keyHint: '',
    modelsText: '',
    openaiBaseURL: '',
    anthropicBaseURL: '',
  }
  applyPreset(form, form.preset)
  return form
}

function applyPreset(form, id) {
  form.preset = id
  const preset = findPreset(id)
  if (!preset) return
  if (!form.name.trim()) form.name = preset.default_name
  if (!form.modelsText.trim()) form.modelsText = preset.models.join('\n')
}

function choosePreset(id) {
  editing.value.modelsText = ''
  applyPreset(editing.value, id)
}

function setMode(mode) {
  editing.value.mode = mode
  if (mode === 'builtin') {
    applyPreset(editing.value, editing.value.preset || presets.value[0]?.id || '')
  }
}

async function loadPresets() {
  try {
    presets.value = await api('/provider-presets')
  } catch {
    presets.value = []
  }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    providers.value = await api('/providers')
  } catch (e) {
    error.value = e.message
  } finally {
    loading.value = false
  }
}

function openCreate() {
  error.value = ''
  editing.value = newForm()
}

function openEdit(p) {
  error.value = ''
  editing.value = {
    id: p.id,
    mode: p.source === 'builtin' ? 'builtin' : 'custom',
    name: p.name,
    preset: p.preset || '',
    apiKey: '',
    keyHint: p.key_hint || '',
    modelsText: (p.models || []).join('\n'),
    openaiBaseURL: p.openai_base_url || '',
    anthropicBaseURL: p.anthropic_base_url || '',
  }
}

function modelLines(text) {
  return text
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean)
}

async function save() {
  const f = editing.value
  const models = modelLines(f.modelsText)
  if (!f.name.trim()) {
    error.value = '请填写供应商名称'
    return
  }
  if (f.mode === 'custom') {
    if (!f.openaiBaseURL.trim() && !f.anthropicBaseURL.trim()) {
      error.value = '至少填写一个 base URL（OpenAI 或 Anthropic）'
      return
    }
    if (!models.length) {
      error.value = '请至少填写一个模型'
      return
    }
  }
  if (f.id === null && !f.apiKey.trim()) {
    error.value = '请填写上游 API Key'
    return
  }

  const payload = {
    name: f.name.trim(),
    models,
  }
  if (f.mode === 'builtin') {
    payload.preset = f.preset
  } else {
    payload.preset = ''
    payload.openai_base_url = f.openaiBaseURL.trim()
    payload.anthropic_base_url = f.anthropicBaseURL.trim()
  }
  if (f.id === null || f.apiKey.trim()) {
    payload.api_key = f.apiKey.trim()
  }

  saving.value = true
  error.value = ''
  try {
    if (f.id === null) {
      await api('/providers', { method: 'POST', body: JSON.stringify(payload) })
    } else {
      await api(`/providers/${f.id}`, { method: 'PUT', body: JSON.stringify(payload) })
    }
    editing.value = null
    await load()
  } catch (e) {
    error.value = e.message
  } finally {
    saving.value = false
  }
}

async function remove(p) {
  if (!confirm(`删除供应商「${p.name}」？它的模型会立即从路由中移除。`)) return
  error.value = ''
  try {
    await api(`/providers/${p.id}`, { method: 'DELETE' })
    await load()
  } catch (e) {
    error.value = e.message
  }
}

onMounted(async () => {
  await loadPresets()
  await load()
})
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
          <a v-if="p.docs_url" class="ghost link" :href="p.docs_url" target="_blank" rel="noreferrer">文档</a>
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

    <div v-if="editing" class="modal-backdrop" @click.self="editing = null">
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
            <label>内置端点</label>
            <select
              :value="editing.preset"
              :disabled="editing.id !== null"
              @change="choosePreset($event.target.value)"
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
            <label>名称</label>
            <input v-model="editing.name" placeholder="例如 minimax" />
          </div>
          <div class="field">
            <label>
              上游 API Key
              <span v-if="editing.id !== null && editing.keyHint" class="muted small">
                留空保留现有 {{ editing.keyHint }}
              </span>
            </label>
            <input
              v-model="editing.apiKey"
              type="password"
              autocomplete="off"
              :placeholder="editing.id === null ? 'sk-...' : '留空则不修改'"
            />
          </div>

          <div v-if="editing.mode === 'builtin' && selectedPreset" class="field span-2 readonly">
            <label>Base URL（只读）</label>
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
              <label>OpenAI Base URL（通常以 <code>/v1</code> 结尾）</label>
              <input v-model="editing.openaiBaseURL" placeholder="https://api.example.com/v1" />
            </div>
            <div class="field span-2">
              <label>Anthropic Base URL（不带 <code>/v1</code>）</label>
              <input v-model="editing.anthropicBaseURL" placeholder="https://api.example.com/anthropic" />
            </div>
          </template>

          <div class="field span-2">
            <label>模型（每行一个，可用 <code>对外名=上游名</code>）</label>
            <textarea
              v-model="editing.modelsText"
              rows="3"
              :placeholder="modelsPlaceholder()"
            ></textarea>
            <p v-if="editing.mode === 'builtin' && selectedPreset" class="muted small">
              留空或删除此行时使用内置默认模型；也能添加 <code>claude-sonnet-4-5={{ selectedPreset.models[0] }}</code> 之类的别名。
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
