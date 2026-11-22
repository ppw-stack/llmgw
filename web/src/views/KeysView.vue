<script setup lang="ts">
import { onMounted, ref } from 'vue';
import { api, formatTime } from '../api';
import type { CreatedKey, GatewayKey } from '../types';
import { confirmAction } from '../utils/confirm';
import { errorMessage } from '../utils/error';

const keys = ref<GatewayKey[]>([]);
const loading = ref(true);
const error = ref('');
const name = ref('');
const created = ref<CreatedKey | null>(null);

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
  error.value = '';
  try {
    const res = await api<CreatedKey>('/keys', {
      method: 'POST',
      body: JSON.stringify({ name: name.value.trim() || 'default' }),
    });
    created.value = res;
    name.value = '';
    await load();
  } catch (e) {
    error.value = errorMessage(e);
  }
}

async function toggle(k: GatewayKey) {
  error.value = '';
  try {
    await api<null>(`/keys/${k.id}`, {
      method: 'PATCH',
      body: JSON.stringify({ enabled: !k.enabled }),
    });
    await load();
  } catch (e) {
    error.value = errorMessage(e);
  }
}

async function remove(k: GatewayKey) {
  if (!confirmAction(`删除 Key「${k.name || k.prefix}」？使用它的客户端将立即失效。`)) return;
  error.value = '';
  try {
    await api<null>(`/keys/${k.id}`, { method: 'DELETE' });
    await load();
  } catch (e) {
    error.value = errorMessage(e);
  }
}

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    /* 剪贴板不可用 */
  }
}

onMounted(() => {
  void load();
});
</script>

<template>
  <section>
    <div class="section-head">
      <h2>网关 Key</h2>
      <button class="ghost" @click="load">刷新</button>
    </div>
    <p class="muted">客户端使用这里的 Key 访问网关；上游 Key 始终留在网关内。</p>

    <div class="card">
      <div class="row">
        <label class="visually-hidden" for="key-name">Key 名称</label>
        <input
          id="key-name"
          v-model="name"
          placeholder="Key 名称，例如 laptop"
          @keyup.enter="create"
        />
        <button class="primary" @click="create">创建 Key</button>
      </div>
      <p v-if="error" class="error">{{ error }}</p>
    </div>

    <div v-if="created" class="card secret">
      <div class="row space-between">
        <strong>新 Key 已创建，只显示这一次</strong>
        <button class="ghost" @click="created = null">关闭</button>
      </div>
      <div class="row">
        <code class="secret-value">{{ created.key }}</code>
        <button class="ghost" @click="copy(created.key)">复制</button>
      </div>
      <p class="muted">请立即保存；关闭后无法再次查看。</p>
    </div>

    <div class="card">
      <p v-if="loading" class="muted">加载中…</p>
      <table v-else>
        <thead>
          <tr>
            <th>名称</th>
            <th>前缀</th>
            <th>状态</th>
            <th>创建时间</th>
            <th>最后使用</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="k in keys" :key="k.id">
            <td>{{ k.name }}</td>
            <td>
              <code>{{ k.prefix }}…</code>
            </td>
            <td>
              <span class="badge" :class="k.enabled ? 'ok' : 'off'">
                {{ k.enabled ? '启用' : '停用' }}
              </span>
            </td>
            <td class="muted">{{ formatTime(k.created_at) }}</td>
            <td class="muted">{{ formatTime(k.last_used_at) }}</td>
            <td class="actions">
              <button class="ghost" @click="toggle(k)">{{ k.enabled ? '停用' : '启用' }}</button>
              <button class="ghost danger" @click="remove(k)">删除</button>
            </td>
          </tr>
          <tr v-if="!keys.length">
            <td colspan="6" class="muted">还没有 Key</td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>
