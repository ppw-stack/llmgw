<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { api, formatMs, formatNumber, formatTime } from '../api';
import AppIcon from '../components/AppIcon.vue';
import CopyButton from '../components/CopyButton.vue';
import type { LogEntry } from '../types';
import { errorMessage } from '../utils/error';

type StatusFilter = 'all' | 'ok' | 'err';
type ProtocolFilter = 'all' | 'openai' | 'anthropic';

const rows = ref<LogEntry[]>([]);
const loading = ref(true);
const error = ref('');
const model = ref('');
const limit = ref(100);
const statusFilter = ref<StatusFilter>('all');
const protocolFilter = ref<ProtocolFilter>('all');
const expandedIds = ref<number[]>([]);

async function load() {
  loading.value = true;
  error.value = '';
  try {
    const q = new URLSearchParams({ limit: String(limit.value) });
    if (model.value.trim()) {
      q.set('model', model.value.trim());
    }
    rows.value = await api<LogEntry[]>('/logs?' + q.toString());
  } catch (e) {
    error.value = errorMessage(e);
  } finally {
    loading.value = false;
  }
}

function clearModel() {
  model.value = '';
  void load();
}

const filteredRows = computed(() =>
  rows.value.filter((r) => {
    if (statusFilter.value === 'ok' && r.status >= 300) {
      return false;
    }
    if (statusFilter.value === 'err' && r.status < 300) {
      return false;
    }
    if (protocolFilter.value !== 'all' && r.ingress_protocol !== protocolFilter.value) {
      return false;
    }
    return true;
  }),
);

function isExpanded(id: number): boolean {
  return expandedIds.value.includes(id);
}

function toggleExpand(id: number) {
  if (isExpanded(id)) {
    expandedIds.value = expandedIds.value.filter((item) => item !== id);
  } else {
    expandedIds.value = [...expandedIds.value, id];
  }
}

/**
 * 缓存命中比例。
 *
 * Anthropic 的 input_tokens 不含缓存 token，OpenAI 的 prompt_tokens
 * 已包含 cached tokens，所以两种入口的分母不同。
 */
function cachePercent(r: LogEntry): string {
  if (r.cache_read_tokens <= 0) {
    return '';
  }
  const total =
    r.ingress_protocol === 'anthropic'
      ? r.prompt_tokens + r.cache_read_tokens + r.cache_write_tokens
      : r.prompt_tokens;
  if (total <= 0) {
    return '';
  }
  const percent = (r.cache_read_tokens / total) * 100;
  return percent < 1 ? '<1%' : `${Math.round(percent)}%`;
}

function cacheLabel(r: LogEntry): string {
  if (r.cache_read_tokens > 0) {
    const percent = cachePercent(r);
    return percent ? `命中 ${percent}` : '命中';
  }
  if (r.cache_write_tokens > 0) {
    return `写入 ${formatNumber(r.cache_write_tokens)}`;
  }
  return '未命中';
}

onMounted(() => {
  void load();
});
</script>

<template>
  <section class="page">
    <div class="page-head">
      <div class="page-title-group">
        <h2>请求日志</h2>
        <p class="page-desc">实时排查路由命中、响应延迟、Token 明细与缓存命中率。</p>
      </div>
      <div class="row">
        <button type="button" class="ghost" :disabled="loading" @click="load">
          <AppIcon name="refresh" :size="14" />
          <span>刷新</span>
        </button>
      </div>
    </div>

    <div class="card">
      <form class="row space-between" @submit.prevent="load">
        <div class="row" style="flex: 1">
          <div class="input-wrap" style="max-width: 260px">
            <label class="visually-hidden" for="log-model">按模型过滤</label>
            <input id="log-model" v-model="model" placeholder="按模型名过滤（回车查询）" />
            <span v-if="model" class="input-suffix">
              <button type="button" class="icon-btn" aria-label="清空模型过滤" @click="clearModel">
                <AppIcon name="close" :size="13" />
              </button>
            </span>
          </div>

          <div class="segmented" role="group" aria-label="按状态筛选">
            <button
              type="button"
              :class="{ active: statusFilter === 'all' }"
              @click="statusFilter = 'all'"
            >
              全部
            </button>
            <button
              type="button"
              :class="{ active: statusFilter === 'ok' }"
              @click="statusFilter = 'ok'"
            >
              成功
            </button>
            <button
              type="button"
              :class="{ active: statusFilter === 'err' }"
              @click="statusFilter = 'err'"
            >
              异常
            </button>
          </div>

          <div class="segmented" role="group" aria-label="按协议筛选">
            <button
              type="button"
              :class="{ active: protocolFilter === 'all' }"
              @click="protocolFilter = 'all'"
            >
              全协议
            </button>
            <button
              type="button"
              :class="{ active: protocolFilter === 'openai' }"
              @click="protocolFilter = 'openai'"
            >
              OpenAI
            </button>
            <button
              type="button"
              :class="{ active: protocolFilter === 'anthropic' }"
              @click="protocolFilter = 'anthropic'"
            >
              Anthropic
            </button>
          </div>
        </div>

        <div class="row">
          <label class="muted small" for="log-limit">条数</label>
          <select id="log-limit" v-model.number="limit" @change="load">
            <option :value="50">50</option>
            <option :value="100">100</option>
            <option :value="300">300</option>
          </select>
          <button type="submit" class="ghost sm">查询</button>
        </div>
      </form>
    </div>

    <p v-if="error" class="error">{{ error }}</p>

    <div class="card table-card">
      <div v-if="loading" class="stack" style="padding: 20px">
        <div class="skeleton-line" />
        <div class="skeleton-line" />
        <div class="skeleton-line" />
      </div>

      <div v-else-if="!filteredRows.length" class="empty-state">
        <span class="empty-icon"><AppIcon name="logs" :size="18" /></span>
        <p>{{ rows.length ? '当前筛选条件下无匹配日志' : '暂无请求日志' }}</p>
      </div>

      <div v-else class="table-wrap">
        <table>
          <thead>
            <tr>
              <th style="width: 36px"></th>
              <th>时间</th>
              <th>令牌</th>
              <th>协议</th>
              <th>模型 / 上游</th>
              <th class="num">状态</th>
              <th class="num">延迟 / TTFT</th>
              <th class="num">输入 / 输出</th>
              <th class="num">缓存</th>
            </tr>
          </thead>
          <tbody>
            <template v-for="r in filteredRows" :key="r.id">
              <tr class="hoverable">
                <td>
                  <button
                    type="button"
                    class="icon-btn"
                    :aria-expanded="isExpanded(r.id)"
                    :aria-label="isExpanded(r.id) ? '收起请求详情' : '展开请求详情'"
                    @click="toggleExpand(r.id)"
                  >
                    <AppIcon
                      :name="isExpanded(r.id) ? 'chevron-down' : 'chevron-right'"
                      :size="14"
                    />
                  </button>
                </td>
                <td class="muted nowrap tabular">{{ formatTime(r.ts) }}</td>
                <td class="nowrap">{{ r.key_name || '#' + r.api_key_id }}</td>
                <td>
                  <span class="badge" :class="r.ingress_protocol">{{ r.ingress_protocol }}</span>
                </td>
                <td>
                  <code>{{ r.model }}</code>
                  <div class="muted small">{{ r.provider }} → {{ r.upstream_model }}</div>
                </td>
                <td class="num">
                  <span class="badge" :class="r.status < 300 ? 'ok' : 'danger'">
                    {{ r.status }}
                  </span>
                  <div v-if="r.error_code" class="muted small">{{ r.error_code }}</div>
                </td>
                <td class="num tabular">
                  <div>{{ formatMs(r.latency_ms) }}</div>
                  <div class="muted small">TTFT {{ r.ttft_ms ? formatMs(r.ttft_ms) : '-' }}</div>
                </td>
                <td class="num tabular">
                  <div>{{ formatNumber(r.prompt_tokens) }}</div>
                  <div class="muted small">{{ formatNumber(r.completion_tokens) }}</div>
                </td>
                <td class="num tabular">
                  <span
                    v-if="r.usage_estimated"
                    class="muted"
                    title="上游未返回用量，缓存信息不可用"
                  >
                    -
                  </span>
                  <template v-else>
                    <span :class="r.cache_read_tokens > 0 ? 'status-ok' : 'muted'">
                      {{ cacheLabel(r) }}
                    </span>
                    <div v-if="r.cache_read_tokens > 0" class="muted small">
                      读 {{ formatNumber(r.cache_read_tokens) }}
                    </div>
                  </template>
                </td>
              </tr>

              <tr v-if="isExpanded(r.id)" class="log-detail-row">
                <td colspan="9">
                  <div class="log-detail-grid">
                    <div class="detail-item">
                      <span class="detail-label">Request ID</span>
                      <div class="row">
                        <code class="mono">{{ r.request_id || '-' }}</code>
                        <CopyButton
                          v-if="r.request_id"
                          :text="r.request_id"
                          icon-only
                          toast-message="Request ID 已复制"
                        />
                      </div>
                    </div>
                    <div class="detail-item">
                      <span class="detail-label">上游路由</span>
                      <span class="mono small">
                        {{ r.provider }} / {{ r.endpoint }} → {{ r.upstream_model }}
                      </span>
                    </div>
                    <div class="detail-item">
                      <span class="detail-label">错误码 / 状态</span>
                      <span>
                        HTTP {{ r.status }}
                        <template v-if="r.error_code"> · {{ r.error_code }}</template>
                      </span>
                    </div>
                    <div class="detail-item">
                      <span class="detail-label">Token 明细</span>
                      <span class="tabular">
                        输入 {{ formatNumber(r.prompt_tokens) }} · 输出
                        {{ formatNumber(r.completion_tokens) }}
                        <span v-if="r.usage_estimated" class="muted">（估算）</span>
                      </span>
                    </div>
                    <div class="detail-item">
                      <span class="detail-label">缓存明细</span>
                      <span class="tabular">
                        读 {{ formatNumber(r.cache_read_tokens) }} · 写
                        {{ formatNumber(r.cache_write_tokens) }}
                      </span>
                    </div>
                    <div class="detail-item">
                      <span class="detail-label">耗时详情</span>
                      <span class="tabular">
                        总延迟 {{ formatMs(r.latency_ms) }} · 首字 TTFT
                        {{ r.ttft_ms ? formatMs(r.ttft_ms) : '-' }}
                      </span>
                    </div>
                  </div>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
      </div>
    </div>
  </section>
</template>
