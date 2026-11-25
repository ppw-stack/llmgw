<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { api, formatNumber } from '../api';
import AppIcon from '../components/AppIcon.vue';
import type { UsageDay, UsageReport } from '../types';
import { errorMessage } from '../utils/error';

type ChartMetric = 'requests' | 'tokens';

const dayOptions = [
  { value: 1, label: '今天' },
  { value: 7, label: '近 7 天' },
  { value: 30, label: '近 30 天' },
] as const;

const days = ref(7);
const metric = ref<ChartMetric>('requests');
const loading = ref(true);
const error = ref('');
const report = ref<UsageReport>({ daily: [], by_model: [], by_key: [] });
const activeDay = ref<string | null>(null);

async function load() {
  loading.value = true;
  error.value = '';
  try {
    report.value = await api<UsageReport>(`/usage?days=${days.value}`);
    const list = report.value.daily ?? [];
    activeDay.value = list.length ? (list[list.length - 1]?.day ?? null) : null;
  } catch (e) {
    error.value = errorMessage(e);
  } finally {
    loading.value = false;
  }
}

function selectDays(value: number) {
  if (days.value === value) {
    return;
  }
  days.value = value;
  void load();
}

function dayTokens(d: UsageDay): number {
  return d.prompt_tokens + d.completion_tokens;
}

function metricValue(d: UsageDay): number {
  return metric.value === 'requests' ? d.requests : dayTokens(d);
}

const dailyList = computed(() => report.value.daily ?? []);
const modelList = computed(() => report.value.by_model ?? []);
const keyList = computed(() => report.value.by_key ?? []);

const totals = computed(() => {
  const t = { requests: 0, prompt: 0, completion: 0, cacheRead: 0, cacheWrite: 0 };
  for (const d of dailyList.value) {
    t.requests += d.requests;
    t.prompt += d.prompt_tokens;
    t.completion += d.completion_tokens;
    t.cacheRead += d.cache_read_tokens;
    t.cacheWrite += d.cache_write_tokens;
  }
  return t;
});

const totalTokens = computed(() => totals.value.prompt + totals.value.completion);

const maxMetric = computed(() => {
  const values = dailyList.value.map((d) => metricValue(d));
  return Math.max(1, ...values);
});

const maxModelRequests = computed(() =>
  Math.max(1, ...modelList.value.map((item) => item.requests)),
);

const maxKeyRequests = computed(() => Math.max(1, ...keyList.value.map((item) => item.requests)));

const focusedDay = computed<UsageDay | null>(() => {
  if (!dailyList.value.length) {
    return null;
  }
  return (
    dailyList.value.find((d) => d.day === activeDay.value) ??
    dailyList.value[dailyList.value.length - 1] ??
    null
  );
});

function formatCompact(n: number): string {
  if (n >= 1_000_000) {
    return `${(n / 1_000_000).toFixed(1)}M`;
  }
  if (n >= 10_000) {
    return `${(n / 1_000).toFixed(1)}K`;
  }
  return formatNumber(n);
}

function sharePct(part: number, total: number): string {
  if (total <= 0) {
    return '0%';
  }
  const pct = (part / total) * 100;
  return pct < 1 && part > 0 ? '<1%' : `${Math.round(pct)}%`;
}

onMounted(() => {
  void load();
});
</script>

<template>
  <section class="page">
    <div class="page-head">
      <div class="page-title-group">
        <h2>概览</h2>
        <p class="page-desc">查看网关流量趋势、Token 消耗与模型/密钥分布。</p>
      </div>
      <div class="row">
        <div class="segmented" role="group" aria-label="统计区间">
          <button
            v-for="opt in dayOptions"
            :key="opt.value"
            type="button"
            :class="{ active: days === opt.value }"
            @click="selectDays(opt.value)"
          >
            {{ opt.label }}
          </button>
        </div>
        <button type="button" class="ghost" :disabled="loading" @click="load">
          <AppIcon name="refresh" :size="14" />
          <span>刷新</span>
        </button>
      </div>
    </div>

    <p v-if="error" class="error">{{ error }}</p>

    <div class="cards-4">
      <div class="card stat-card">
        <div class="stat-top">
          <span>请求总数</span>
          <AppIcon name="activity" :size="15" />
        </div>
        <strong class="stat-value">{{ formatNumber(totals.requests) }}</strong>
        <span class="stat-sub">总 Tokens {{ formatCompact(totalTokens) }}</span>
      </div>

      <div class="card stat-card">
        <div class="stat-top">
          <span>输入 Tokens</span>
          <AppIcon name="layers" :size="15" />
        </div>
        <strong class="stat-value">{{ formatNumber(totals.prompt) }}</strong>
        <span class="stat-sub">占总用量 {{ sharePct(totals.prompt, totalTokens) }}</span>
      </div>

      <div class="card stat-card">
        <div class="stat-top">
          <span>输出 Tokens</span>
          <AppIcon name="spark" :size="15" />
        </div>
        <strong class="stat-value">{{ formatNumber(totals.completion) }}</strong>
        <span class="stat-sub">占总用量 {{ sharePct(totals.completion, totalTokens) }}</span>
      </div>

      <div class="card stat-card">
        <div class="stat-top">
          <span>缓存读 Tokens</span>
          <AppIcon name="overview" :size="15" />
        </div>
        <strong class="stat-value">{{ formatNumber(totals.cacheRead) }}</strong>
        <span class="stat-sub">缓存写入 {{ formatNumber(totals.cacheWrite) }}</span>
      </div>
    </div>

    <div class="card">
      <div class="card-header">
        <div>
          <h3>每日趋势</h3>
          <p class="muted small">悬停或点击柱状条查看当日明细</p>
        </div>
        <div class="segmented" role="group" aria-label="图表指标">
          <button
            type="button"
            :class="{ active: metric === 'requests' }"
            @click="metric = 'requests'"
          >
            请求数
          </button>
          <button type="button" :class="{ active: metric === 'tokens' }" @click="metric = 'tokens'">
            总 Tokens
          </button>
        </div>
      </div>

      <div v-if="loading" class="stack">
        <div class="skeleton-line" style="height: 150px" />
      </div>

      <div v-else-if="!dailyList.length" class="empty-state">
        <span class="empty-icon"><AppIcon name="activity" :size="18" /></span>
        <p>该时间区间暂无流量记录</p>
      </div>

      <template v-else>
        <div class="chart-stage">
          <div class="chart-grid" aria-hidden="true">
            <div class="chart-grid-line">
              <span>{{ formatCompact(maxMetric) }}</span>
            </div>
            <div class="chart-grid-line">
              <span>{{ formatCompact(Math.round(maxMetric / 2)) }}</span>
            </div>
            <div class="chart-grid-line">
              <span>0</span>
            </div>
          </div>

          <div class="bars">
            <div v-for="d in dailyList" :key="d.day" class="bar-col">
              <button
                type="button"
                class="bar-hit"
                :aria-label="`${d.day}：${d.requests} 次请求，${dayTokens(d)} tokens`"
                @mouseenter="activeDay = d.day"
                @focus="activeDay = d.day"
                @click="activeDay = d.day"
              >
                <span
                  class="bar"
                  :style="{ height: `${Math.max(2, (metricValue(d) / maxMetric) * 100)}%` }"
                />
              </button>
              <span class="bar-label">{{ d.day.slice(5) }}</span>
            </div>
          </div>
        </div>

        <div v-if="focusedDay" class="chart-tooltip tabular">
          <strong>{{ focusedDay.day }}</strong>
          <span>请求 {{ formatNumber(focusedDay.requests) }} 次</span>
          <span>输入 {{ formatNumber(focusedDay.prompt_tokens) }}</span>
          <span>输出 {{ formatNumber(focusedDay.completion_tokens) }}</span>
          <span>缓存读 {{ formatNumber(focusedDay.cache_read_tokens) }}</span>
          <span>缓存写 {{ formatNumber(focusedDay.cache_write_tokens) }}</span>
        </div>
      </template>
    </div>

    <div class="grid-2">
      <div class="card table-card">
        <div class="card-header" style="padding: 16px 18px 0">
          <h3>按模型分布</h3>
          <span class="muted small">{{ modelList.length }} 项</span>
        </div>
        <div class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>模型</th>
                <th class="num">请求数</th>
                <th class="num">占比</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="m in modelList" :key="m.model" class="hoverable">
                <td>
                  <code>{{ m.model }}</code>
                  <div class="rank-bar-track">
                    <div
                      class="rank-bar-fill"
                      :style="{ width: `${(m.requests / maxModelRequests) * 100}%` }"
                    />
                  </div>
                </td>
                <td class="num">{{ formatNumber(m.requests) }}</td>
                <td class="num muted">{{ sharePct(m.requests, totals.requests) }}</td>
              </tr>
              <tr v-if="!modelList.length">
                <td colspan="3" class="muted">暂无数据</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div class="card table-card">
        <div class="card-header" style="padding: 16px 18px 0">
          <h3>按网关 Key 分布</h3>
          <span class="muted small">{{ keyList.length }} 项</span>
        </div>
        <div class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>网关 Key</th>
                <th class="num">请求数</th>
                <th class="num">占比</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="k in keyList" :key="k.key_id" class="hoverable">
                <td>
                  <span>{{ k.name || '#' + k.key_id }}</span>
                  <div class="rank-bar-track">
                    <div
                      class="rank-bar-fill"
                      :style="{ width: `${(k.requests / maxKeyRequests) * 100}%` }"
                    />
                  </div>
                </td>
                <td class="num">{{ formatNumber(k.requests) }}</td>
                <td class="num muted">{{ sharePct(k.requests, totals.requests) }}</td>
              </tr>
              <tr v-if="!keyList.length">
                <td colspan="3" class="muted">暂无数据</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </section>
</template>
