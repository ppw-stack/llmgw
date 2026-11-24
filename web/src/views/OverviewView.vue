<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { api, formatNumber } from '../api';
import type { UsageReport } from '../types';
import { errorMessage } from '../utils/error';

const days = ref(7);
const loading = ref(true);
const error = ref('');
const report = ref<UsageReport>({ daily: [], by_model: [], by_key: [] });

async function load() {
  loading.value = true;
  error.value = '';
  try {
    report.value = await api<UsageReport>(`/usage?days=${days.value}`);
  } catch (e) {
    error.value = errorMessage(e);
  } finally {
    loading.value = false;
  }
}

const totals = computed(() => {
  const t = { requests: 0, prompt: 0, completion: 0, cacheRead: 0, cacheWrite: 0 };
  for (const d of report.value.daily ?? []) {
    t.requests += d.requests;
    t.prompt += d.prompt_tokens;
    t.completion += d.completion_tokens;
    t.cacheRead += d.cache_read_tokens;
    t.cacheWrite += d.cache_write_tokens;
  }
  return t;
});

const maxDaily = computed(() => {
  const list = report.value.daily ?? [];
  return Math.max(1, ...list.map((d) => d.requests));
});

onMounted(() => {
  void load();
});
</script>

<template>
  <section>
    <div class="section-head">
      <h2>概览</h2>
      <div class="row">
        <label class="visually-hidden" for="usage-days">统计区间</label>
        <select id="usage-days" v-model.number="days" @change="load">
          <option :value="1">今天</option>
          <option :value="7">近 7 天</option>
          <option :value="30">近 30 天</option>
        </select>
        <button class="ghost" @click="load">刷新</button>
      </div>
    </div>

    <p v-if="error" class="error">{{ error }}</p>

    <div class="cards">
      <div class="card stat">
        <span class="muted">请求数</span>
        <strong>{{ formatNumber(totals.requests) }}</strong>
      </div>
      <div class="card stat">
        <span class="muted">输入 tokens</span>
        <strong>{{ formatNumber(totals.prompt) }}</strong>
      </div>
      <div class="card stat">
        <span class="muted">输出 tokens</span>
        <strong>{{ formatNumber(totals.completion) }}</strong>
      </div>
      <div class="card stat">
        <span class="muted">缓存读 tokens</span>
        <strong>{{ formatNumber(totals.cacheRead) }}</strong>
        <span class="muted small">写入 {{ formatNumber(totals.cacheWrite) }}</span>
      </div>
    </div>

    <div class="card">
      <h3>每日请求</h3>
      <p v-if="loading" class="muted">加载中…</p>
      <p v-else-if="!report.daily?.length" class="muted">暂无数据</p>
      <div v-else class="bars">
        <div
          v-for="d in report.daily ?? []"
          :key="d.day"
          class="bar-col"
          :title="`${d.day}: ${d.requests} 次`"
        >
          <div class="bar" :style="{ height: (d.requests / maxDaily) * 100 + '%' }"></div>
          <span class="bar-label">{{ d.day.slice(5) }}</span>
        </div>
      </div>
    </div>

    <div class="grid-2">
      <div class="card">
        <h3>按模型</h3>
        <table>
          <thead>
            <tr>
              <th>模型</th>
              <th class="num">请求</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="m in report.by_model ?? []" :key="m.model">
              <td>
                <code>{{ m.model }}</code>
              </td>
              <td class="num">{{ formatNumber(m.requests) }}</td>
            </tr>
            <tr v-if="!report.by_model?.length">
              <td colspan="2" class="muted">暂无数据</td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="card">
        <h3>按网关 Key</h3>
        <table>
          <thead>
            <tr>
              <th>Key</th>
              <th class="num">请求</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="k in report.by_key ?? []" :key="k.key_id">
              <td>{{ k.name || '#' + k.key_id }}</td>
              <td class="num">{{ formatNumber(k.requests) }}</td>
            </tr>
            <tr v-if="!report.by_key?.length">
              <td colspan="2" class="muted">暂无数据</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </section>
</template>
