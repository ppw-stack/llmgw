<script setup lang="ts">
import { onMounted, ref } from 'vue';
import { api, formatTime } from '../api';
import AppIcon from '../components/AppIcon.vue';
import BaseModal from '../components/BaseModal.vue';
import CopyButton from '../components/CopyButton.vue';
import type { CreatedKey, GatewayKey, Model } from '../types';
import { confirmAction } from '../utils/confirm';
import { errorMessage } from '../utils/error';
import { showToast } from '../utils/toast';

interface KeyEditForm {
  id: number;
  name: string;
  rpmLimit: string;
  maxConcurrent: string;
  expiresAtLocal: string;
  allowedModels: string[];
}

const keys = ref<GatewayKey[]>([]);
const models = ref<Model[]>([]);
const loading = ref(true);
const creating = ref(false);
const saving = ref(false);
const error = ref('');
const modalError = ref('');
const name = ref('');
const created = ref<CreatedKey | null>(null);
const revealedIds = ref<number[]>([]);
const editing = ref<KeyEditForm | null>(null);

async function load() {
  loading.value = true;
  error.value = '';
  try {
    const [keyList, modelList] = await Promise.all([
      api<GatewayKey[]>('/keys'),
      api<Model[]>('/models'),
    ]);
    keys.value = keyList;
    models.value = modelList;
  } catch (e) {
    error.value = errorMessage(e);
  } finally {
    loading.value = false;
  }
}

async function create() {
  if (creating.value) {
    return;
  }
  creating.value = true;
  error.value = '';
  try {
    const res = await api<CreatedKey>('/keys', {
      method: 'POST',
      body: JSON.stringify({ name: name.value.trim() || 'default' }),
    });
    created.value = res;
    name.value = '';
    await load();
    showToast(`已创建网关 Key「${res.name}」`);
  } catch (e) {
    error.value = errorMessage(e);
  } finally {
    creating.value = false;
  }
}

async function toggle(k: GatewayKey) {
  error.value = '';
  const nextEnabled = !k.enabled;
  try {
    await api<GatewayKey>(`/keys/${k.id}`, {
      method: 'PATCH',
      body: JSON.stringify({ enabled: nextEnabled }),
    });
    await load();
    showToast(nextEnabled ? `已启用「${k.name}」` : `已停用「${k.name}」`);
  } catch (e) {
    error.value = errorMessage(e);
  }
}

function toDatetimeLocal(iso?: string): string {
  if (!iso) {
    return '';
  }
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) {
    return '';
  }
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function openEdit(k: GatewayKey) {
  modalError.value = '';
  editing.value = {
    id: k.id,
    name: k.name,
    rpmLimit: k.rpm_limit > 0 ? String(k.rpm_limit) : '',
    maxConcurrent: k.max_concurrent > 0 ? String(k.max_concurrent) : '',
    expiresAtLocal: toDatetimeLocal(k.expires_at),
    allowedModels: [...k.allowed_models],
  };
}

function toggleAllowedModel(modelName: string) {
  const form = editing.value;
  if (!form) {
    return;
  }
  if (form.allowedModels.includes(modelName)) {
    form.allowedModels = form.allowedModels.filter((m) => m !== modelName);
  } else {
    form.allowedModels = [...form.allowedModels, modelName];
  }
}

function parseOptionalNonNegInt(raw: string, label: string): number | null {
  const trimmed = raw.trim();
  if (!trimmed) {
    return 0;
  }
  const n = Number(trimmed);
  if (!Number.isInteger(n) || n < 0) {
    modalError.value = `${label}必须为 0 或正整数`;
    return null;
  }
  return n;
}

async function saveEdit() {
  const form = editing.value;
  if (!form || saving.value) {
    return;
  }
  modalError.value = '';
  const rpm = parseOptionalNonNegInt(form.rpmLimit, '每分钟请求上限');
  if (rpm === null) {
    return;
  }
  const maxConc = parseOptionalNonNegInt(form.maxConcurrent, '最大并发数');
  if (maxConc === null) {
    return;
  }
  let expiresAt = '';
  if (form.expiresAtLocal.trim()) {
    const parsed = new Date(form.expiresAtLocal);
    if (Number.isNaN(parsed.getTime())) {
      modalError.value = '过期时间格式无效';
      return;
    }
    expiresAt = parsed.toISOString();
  }

  saving.value = true;
  try {
    await api<GatewayKey>(`/keys/${form.id}`, {
      method: 'PATCH',
      body: JSON.stringify({
        name: form.name.trim() || 'default',
        rpm_limit: rpm,
        max_concurrent: maxConc,
        expires_at: expiresAt,
        allowed_models: form.allowedModels,
      }),
    });
    editing.value = null;
    await load();
    showToast('已保存网关 Key 配置');
  } catch (e) {
    modalError.value = errorMessage(e);
  } finally {
    saving.value = false;
  }
}

async function remove(k: GatewayKey) {
  if (!confirmAction(`删除 Key「${k.name || k.prefix}」？使用它的客户端将立即失效。`)) {
    return;
  }
  error.value = '';
  try {
    await api<null>(`/keys/${k.id}`, { method: 'DELETE' });
    await load();
    showToast(`已删除「${k.name || k.prefix}」`);
  } catch (e) {
    error.value = errorMessage(e);
  }
}

function isExpired(k: GatewayKey): boolean {
  if (!k.expires_at) {
    return false;
  }
  const t = new Date(k.expires_at).getTime();
  return !Number.isNaN(t) && t <= Date.now();
}

function isRevealed(id: number): boolean {
  return revealedIds.value.includes(id);
}

function toggleReveal(id: number) {
  if (isRevealed(id)) {
    revealedIds.value = revealedIds.value.filter((item) => item !== id);
  } else {
    revealedIds.value = [...revealedIds.value, id];
  }
}

/** 列表默认掩码显示，保留前缀与末 4 位，防旁窥同时方便辨认。 */
function maskKey(fullKey: string): string {
  if (fullKey.length <= 14) {
    return '••••••••••••';
  }
  return `${fullKey.slice(0, 10)}••••••••${fullKey.slice(-4)}`;
}

onMounted(() => {
  void load();
});
</script>

<template>
  <section class="page">
    <div class="page-head">
      <div class="page-title-group">
        <h2>网关 Key</h2>
        <p class="page-desc">
          客户端使用网关 Key 鉴权访问；可按 Key
          配置独立限速、并发上限、过期时间与允许访问的模型范围。
        </p>
      </div>
      <div class="row">
        <button type="button" class="ghost" :disabled="loading" @click="load">
          <AppIcon name="refresh" :size="14" />
          <span>刷新</span>
        </button>
      </div>
    </div>

    <div class="card">
      <form class="row" @submit.prevent="create">
        <label class="nowrap" for="key-name">新建网关 Key</label>
        <input
          id="key-name"
          v-model="name"
          style="flex: 1; min-width: 200px"
          placeholder="输入用途名称，例如 macbook / cursor（留空默认为 default）"
        />
        <button class="primary" type="submit" :disabled="creating">
          <AppIcon name="plus" :size="14" />
          <span>{{ creating ? '创建中…' : '创建 Key' }}</span>
        </button>
      </form>
      <p v-if="error" class="error" style="margin-top: 12px">{{ error }}</p>
    </div>

    <div v-if="created" class="card secret-banner">
      <div class="row space-between">
        <div class="row">
          <span class="badge ok">已创建</span>
          <strong>{{ created.name }}</strong>
        </div>
        <button type="button" class="icon-btn" aria-label="关闭新建提示" @click="created = null">
          <AppIcon name="close" :size="15" />
        </button>
      </div>
      <div class="row">
        <code class="secret-code">{{ created.key }}</code>
        <CopyButton :text="created.key" toast-message="网关 Key 已复制" />
      </div>
      <p class="secondary small">已保存，可随时在下方列表中查看、复制或配置限速与模型白名单。</p>
    </div>

    <div class="card table-card">
      <div v-if="loading" class="stack" style="padding: 20px">
        <div class="skeleton-line" />
        <div class="skeleton-line" />
      </div>

      <div v-else-if="!keys.length" class="empty-state">
        <span class="empty-icon"><AppIcon name="keys" :size="18" /></span>
        <p>还没有网关 Key，在上方输入名称即可创建一个。</p>
      </div>

      <div v-else class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>名称</th>
              <th>密钥</th>
              <th>状态</th>
              <th>配额与范围</th>
              <th>最后使用</th>
              <th class="num">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="k in keys" :key="k.id" class="hoverable">
              <td style="font-weight: 500">{{ k.name }}</td>
              <td>
                <div v-if="k.key" class="row nowrap">
                  <code class="mono">{{ isRevealed(k.id) ? k.key : maskKey(k.key) }}</code>
                  <button
                    type="button"
                    class="icon-btn"
                    :aria-label="isRevealed(k.id) ? '隐藏密钥' : '显示完整密钥'"
                    :title="isRevealed(k.id) ? '隐藏密钥' : '显示完整密钥'"
                    @click="toggleReveal(k.id)"
                  >
                    <AppIcon :name="isRevealed(k.id) ? 'eye-off' : 'eye'" :size="14" />
                  </button>
                  <CopyButton :text="k.key" icon-only toast-message="网关 Key 已复制" />
                </div>
                <span v-else class="muted small">{{ k.prefix }}…（旧版 Key 仅存前缀）</span>
              </td>
              <td>
                <span class="badge" :class="!k.enabled ? 'off' : isExpired(k) ? 'danger' : 'ok'">
                  <span class="status-dot" />
                  {{ !k.enabled ? '停用' : isExpired(k) ? '已过期' : '启用' }}
                </span>
              </td>
              <td>
                <div class="row small" style="flex-wrap: wrap; gap: 6px">
                  <span v-if="k.rpm_limit > 0" class="badge">{{ k.rpm_limit }} RPM</span>
                  <span v-if="k.max_concurrent > 0" class="badge">并发 {{ k.max_concurrent }}</span>
                  <span v-if="k.allowed_models.length" class="badge accent">
                    限 {{ k.allowed_models.length }} 个模型
                  </span>
                  <span v-if="k.expires_at" class="badge" :class="isExpired(k) ? 'danger' : 'off'">
                    截止 {{ formatTime(k.expires_at) }}
                  </span>
                  <span
                    v-if="
                      !k.rpm_limit && !k.max_concurrent && !k.allowed_models.length && !k.expires_at
                    "
                    class="muted"
                  >
                    全局默认 · 全部模型
                  </span>
                </div>
              </td>
              <td class="muted nowrap tabular">{{ formatTime(k.last_used_at) }}</td>
              <td>
                <div class="actions">
                  <button type="button" class="ghost sm" @click="openEdit(k)">编辑</button>
                  <button type="button" class="ghost sm" @click="toggle(k)">
                    {{ k.enabled ? '停用' : '启用' }}
                  </button>
                  <button type="button" class="ghost danger sm" @click="remove(k)">删除</button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <BaseModal v-if="editing" title="编辑网关 Key 策略" @close="editing = null">
      <form id="key-edit-form" class="stack" @submit.prevent="saveEdit">
        <div class="field">
          <label for="edit-key-name">名称</label>
          <input id="edit-key-name" v-model="editing.name" placeholder="例如 cursor / ci-bot" />
        </div>

        <div class="form-grid">
          <div class="field">
            <label for="edit-key-rpm">每分钟请求上限（RPM）</label>
            <input
              id="edit-key-rpm"
              v-model="editing.rpmLimit"
              type="text"
              inputmode="numeric"
              placeholder="留空或 0 表示沿用全局"
            />
          </div>
          <div class="field">
            <label for="edit-key-concurrent">最大并发数</label>
            <input
              id="edit-key-concurrent"
              v-model="editing.maxConcurrent"
              type="text"
              inputmode="numeric"
              placeholder="留空或 0 表示沿用全局"
            />
          </div>
        </div>

        <div class="field">
          <label for="edit-key-expires">过期时间（可选，留空表示永不过期）</label>
          <div class="row">
            <input
              id="edit-key-expires"
              v-model="editing.expiresAtLocal"
              type="datetime-local"
              style="flex: 1"
            />
            <button
              v-if="editing.expiresAtLocal"
              type="button"
              class="ghost sm"
              @click="editing.expiresAtLocal = ''"
            >
              清空
            </button>
          </div>
        </div>

        <div class="field">
          <span class="field-caption">
            允许访问的模型（全不选表示允许全部模型，当前已选 {{ editing.allowedModels.length }} 个）
          </span>
          <div v-if="models.length" class="tag-list">
            <button
              v-for="m in models"
              :key="m.id"
              type="button"
              class="chip-btn"
              :class="{ active: editing.allowedModels.includes(m.name) }"
              @click="toggleAllowedModel(m.name)"
            >
              <span class="mono">{{ m.name }}</span>
            </button>
          </div>
          <p v-else class="field-hint">当前尚未配置任何模型。</p>
        </div>
      </form>

      <template #footer>
        <p v-if="modalError" class="error">{{ modalError }}</p>
        <div class="row space-between">
          <button type="button" class="ghost" @click="editing = null">取消</button>
          <button type="submit" form="key-edit-form" class="primary" :disabled="saving">
            {{ saving ? '保存中…' : '保存策略' }}
          </button>
        </div>
      </template>
    </BaseModal>
  </section>
</template>
