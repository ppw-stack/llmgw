<script setup lang="ts">
import { onMounted, ref } from 'vue';
import { api, formatTime } from '../api';
import AppIcon from '../components/AppIcon.vue';
import CopyButton from '../components/CopyButton.vue';
import type { CreatedKey, GatewayKey } from '../types';
import { confirmAction } from '../utils/confirm';
import { errorMessage } from '../utils/error';
import { showToast } from '../utils/toast';

const keys = ref<GatewayKey[]>([]);
const loading = ref(true);
const creating = ref(false);
const error = ref('');
const name = ref('');
const created = ref<CreatedKey | null>(null);
const revealedIds = ref<number[]>([]);

async function load() {
  loading.value = true;
  error.value = '';
  try {
    keys.value = await api<GatewayKey[]>('/keys');
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
    await api<null>(`/keys/${k.id}`, {
      method: 'PATCH',
      body: JSON.stringify({ enabled: nextEnabled }),
    });
    await load();
    showToast(nextEnabled ? `已启用「${k.name}」` : `已停用「${k.name}」`);
  } catch (e) {
    error.value = errorMessage(e);
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
          客户端使用网关 Key 鉴权访问；上游厂商 API Key 始终留在网关内部，不会暴露给调用方。
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
      <p class="secondary small">已保存，可随时在下方列表中查看或复制。</p>
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
              <th>创建时间</th>
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
                <span class="badge" :class="k.enabled ? 'ok' : 'off'">
                  <span class="status-dot" />
                  {{ k.enabled ? '启用' : '停用' }}
                </span>
              </td>
              <td class="muted nowrap tabular">{{ formatTime(k.created_at) }}</td>
              <td class="muted nowrap tabular">{{ formatTime(k.last_used_at) }}</td>
              <td>
                <div class="actions">
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
  </section>
</template>
