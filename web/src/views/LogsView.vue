<script setup lang="ts">
import { onMounted, ref } from 'vue';
import { api, formatMs, formatNumber, formatTime } from '../api';
import type { LogEntry } from '../types';
import { errorMessage } from '../utils/error';

const rows = ref<LogEntry[]>([]);
const loading = ref(true);
const error = ref('');
const model = ref('');
const limit = ref(100);

async function load() {
  loading.value = true;
  error.value = '';
  try {
    const q = new URLSearchParams({ limit: String(limit.value) });
    if (model.value.trim()) q.set('model', model.value.trim());
    rows.value = await api<LogEntry[]>('/logs?' + q.toString());
  } catch (e) {
    error.value = errorMessage(e);
  } finally {
    loading.value = false;
  }
}

/**
 * 缓存命中比例。
 *
 * Anthropic 的 input_tokens 不含缓存 token，OpenAI 的 prompt_tokens
 * 已包含 cached tokens，所以两种入口的分母不同。
 */
function cachePercent(r: LogEntry): string {
  if (r.cache_read_tokens <= 0) return '';
  const total =
    r.ingress_protocol === 'anthropic'
      ? r.prompt_tokens + r.cache_read_tokens + r.cache_write_tokens
      : r.prompt_tokens;
  if (total <= 0) return '';
  const percent = (r.cache_read_tokens / total) * 100;
  return percent < 1 ? '<1%' : `${Math.round(percent)}%`;
}

function cacheLabel(r: LogEntry): string {
  if (r.cache_read_tokens > 0) {
    const percent = cachePercent(r);
    return percent ? `命中 ${percent}` : '命中';
  }
  if (r.cache_write_tokens > 0) return `写入 ${formatNumber(r.cache_write_tokens)}`;
  return '未命中';
}

onMounted(() => {
  void load();
});
</script>

<template>
  <section>
    <div class="section-head">
      <h2>请求日志</h2>
      <div class="row">
        <label class="visually-hidden" for="log-model">按模型过滤</label>
        <input id="log-model" v-model="model" placeholder="按模型过滤" @keyup.enter="load" />
        <label class="visually-hidden" for="log-limit">日志条数</label>
        <select id="log-limit" v-model.number="limit" @change="load">
          <option :value="50">50</option>
          <option :value="100">100</option>
          <option :value="300">300</option>
        </select>
        <button class="ghost" @click="load">刷新</button>
      </div>
    </div>

    <p v-if="error" class="error">{{ error }}</p>

    <div class="card table-wrap">
      <p v-if="loading" class="muted">加载中…</p>
      <table v-else>
        <thead>
          <tr>
            <th>时间</th>
            <th>令牌</th>
            <th>协议</th>
            <th>模型</th>
            <th>端点</th>
            <th class="num">状态</th>
            <th class="num">延迟</th>
            <th class="num">TTFT</th>
            <th class="num">输入</th>
            <th class="num">输出</th>
            <th class="num">缓存</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in rows" :key="r.id">
            <td class="muted nowrap">{{ formatTime(r.ts) }}</td>
            <td>{{ r.key_name || '#' + r.api_key_id }}</td>
            <td>
              <span class="badge" :class="r.ingress_protocol">{{ r.ingress_protocol }}</span>
            </td>
            <td>
              <code>{{ r.model }}</code>
              <div class="muted small">{{ r.provider }} / {{ r.endpoint }}</div>
            </td>
            <td class="muted small">{{ r.upstream_model }}</td>
            <td class="num">
              <span :class="r.status < 300 ? 'status-ok' : 'status-bad'">{{ r.status }}</span>
              <div v-if="r.error_code" class="muted small">{{ r.error_code }}</div>
            </td>
            <td class="num">{{ formatMs(r.latency_ms) }}</td>
            <td class="num">{{ r.ttft_ms ? formatMs(r.ttft_ms) : '-' }}</td>
            <td class="num">{{ formatNumber(r.prompt_tokens) }}</td>
            <td class="num">{{ formatNumber(r.completion_tokens) }}</td>
            <td class="num">
              <span v-if="r.usage_estimated" class="muted" title="上游未返回用量，缓存信息不可用">
                -
              </span>
              <template v-else>
                <span :class="r.cache_read_tokens > 0 ? 'status-ok' : 'muted'">
                  {{ cacheLabel(r) }}
                </span>
                <div v-if="r.cache_read_tokens > 0" class="muted small">
                  读 {{ formatNumber(r.cache_read_tokens) }} / 写
                  {{ formatNumber(r.cache_write_tokens) }}
                </div>
              </template>
            </td>
          </tr>
          <tr v-if="!rows.length">
            <td colspan="11" class="muted">暂无日志</td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>
