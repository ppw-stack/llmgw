<script setup>
import { onMounted, ref } from 'vue'
import { api } from '../api'

const providers = ref([])
const loading = ref(true)
const error = ref('')
const editing = ref(null)
const saving = ref(false)

function emptyEndpoint() {
  return { name: '', protocol: 'openai', base_url: '', version: '', modelsText: '' }
}

function newForm() {
  return {
    id: null,
    name: '',
    proxy: '',
    timeoutSec: 120,
    keysText: '',
    keyCount: 0,
    endpoints: [emptyEndpoint()],
  }
}

function parseModels(text) {
  const models = {}
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (!line) continue
    const idx = line.indexOf('=')
    if (idx < 0) {
      models[line] = line
    } else {
      models[line.slice(0, idx).trim()] = line.slice(idx + 1).trim()
    }
  }
  return models
}

function modelsToText(models) {
  return Object.entries(models || {})
    .map(([alias, upstream]) => (alias === upstream ? alias : `${alias}=${upstream}`))
    .join('\n')
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
  editing.value = newForm()
}

function openEdit(p) {
  editing.value = {
    id: p.id,
    name: p.name,
    proxy: p.proxy,
    timeoutSec: Math.round((p.timeout_ms || 120000) / 1000),
    keysText: '',
    keyCount: p.key_count,
    endpoints: (p.endpoints || []).map((ep) => ({
      name: ep.name,
      protocol: ep.protocol,
      base_url: ep.base_url,
      version: ep.version || '',
      modelsText: modelsToText(ep.models),
    })),
  }
}

function addEndpoint() {
  editing.value.endpoints.push(emptyEndpoint())
}

function removeEndpoint(index) {
  editing.value.endpoints.splice(index, 1)
}

async function save() {
  const f = editing.value
  if (!f.name.trim()) {
    error.value = '请填写供应商名称'
    return
  }
  const endpoints = f.endpoints.map((ep) => ({
    name: ep.name.trim(),
    protocol: ep.protocol,
    base_url: ep.base_url.trim(),
    version: ep.version.trim(),
    models: parseModels(ep.modelsText),
  }))

  const payload = {
    name: f.name.trim(),
    proxy: f.proxy.trim(),
    timeout_ms: Math.max(1, Number(f.timeoutSec) || 120) * 1000,
    endpoints,
  }
  const keys = f.keysText
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean)
  // On edit, an empty key box means "keep the stored keys".
  if (f.id === null || keys.length > 0) {
    payload.keys = keys
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

onMounted(load)
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
      供应商保存在数据库中，上游 Key 加密存放。保存后立即生效，无需重启。
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
          <span class="badge">{{ p.key_count }} 个上游 Key</span>
          <span v-if="p.proxy" class="badge">proxy</span>
        </div>
        <div class="row">
          <button class="ghost" @click="openEdit(p)">编辑</button>
          <button class="ghost danger" @click="remove(p)">删除</button>
        </div>
      </div>

      <div v-if="p.key_hints?.length" class="key-hints">
        <code v-for="(hint, i) in p.key_hints" :key="i">{{ hint }}</code>
      </div>

      <div v-for="ep in p.endpoints" :key="ep.name" class="endpoint">
        <div class="endpoint-head">
          <span class="badge" :class="ep.protocol">{{ ep.protocol }}</span>
          <strong>{{ ep.name }}</strong>
          <code class="base">{{ ep.base_url }}</code>
        </div>
        <div class="models">
          <span v-for="(upstream, alias) in ep.models" :key="alias" class="tag">
            {{ alias }}<template v-if="alias !== upstream"> → {{ upstream }}</template>
          </span>
        </div>
      </div>
    </div>

    <div v-if="editing" class="modal-backdrop" @click.self="editing = null">
      <div class="card modal wide">
        <div class="row space-between">
          <h3>{{ editing.id === null ? '新增供应商' : '编辑供应商' }}</h3>
          <button class="ghost" @click="editing = null">关闭</button>
        </div>

        <div class="form-grid">
          <div class="field">
            <label>名称</label>
            <input v-model="editing.name" placeholder="例如 glm" />
          </div>
          <div class="field">
            <label>超时（秒）</label>
            <input v-model.number="editing.timeoutSec" type="number" min="1" />
          </div>
          <div class="field span-2">
            <label>HTTP 代理（可选）</label>
            <input v-model="editing.proxy" placeholder="http://127.0.0.1:7890" />
          </div>
          <div class="field span-2">
            <label>
              上游 Key（每行一个）
              <span v-if="editing.id !== null" class="muted small">
                留空表示保留现有 {{ editing.keyCount }} 个 Key
              </span>
            </label>
            <textarea
              v-model="editing.keysText"
              rows="3"
              :placeholder="editing.id === null ? 'sk-...' : '留空则不修改'"
            ></textarea>
          </div>
        </div>

        <h4 class="subhead">端点</h4>
        <div v-for="(ep, index) in editing.endpoints" :key="index" class="endpoint-editor">
          <div class="row space-between">
            <strong class="muted small">端点 {{ index + 1 }}</strong>
            <button
              class="ghost danger"
              :disabled="editing.endpoints.length === 1"
              @click="removeEndpoint(index)"
            >
              移除
            </button>
          </div>
          <div class="form-grid">
            <div class="field">
              <label>名称</label>
              <input v-model="ep.name" placeholder="openai" />
            </div>
            <div class="field">
              <label>协议</label>
              <select v-model="ep.protocol">
                <option value="openai">openai</option>
                <option value="anthropic">anthropic</option>
              </select>
            </div>
            <div class="field span-2">
              <label>Base URL</label>
              <input v-model="ep.base_url" placeholder="https://api.example.com/v1" />
            </div>
            <div v-if="ep.protocol === 'anthropic'" class="field">
              <label>anthropic-version</label>
              <input v-model="ep.version" placeholder="2023-06-01" />
            </div>
            <div class="field" :class="{ 'span-2': ep.protocol !== 'anthropic' }">
              <label>模型映射（每行 <code>对外名=上游名</code>，同名可只写一个）</label>
              <textarea v-model="ep.modelsText" rows="3" placeholder="glm-4.6=glm-4.6"></textarea>
            </div>
          </div>
        </div>
        <div class="row">
          <button class="ghost" @click="addEndpoint">添加端点</button>
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
