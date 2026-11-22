<script setup>
import { onMounted, ref } from 'vue'
import { api, formatMs, formatNumber, formatTime, formatUSD } from '../api'

const rows = ref([])
const loading = ref(true)
const error = ref('')
const model = ref('')
const limit = ref(100)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const q = new URLSearchParams({ limit: String(limit.value) })
    if (model.value.trim()) q.set('model', model.value.trim())
    rows.value = await api('/logs?' + q.toString())
  } catch (e) {
    error.value = e.message
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <section>
    <div class="section-head">
      <h2>请求日志</h2>
      <div class="row">
        <input v-model="model" placeholder="按模型过滤" @keyup.enter="load" />
        <select v-model.number="limit" @change="load">
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
            <th>时间</th><th>协议</th><th>模型</th><th>端点</th><th class="num">状态</th>
            <th class="num">延迟</th><th class="num">TTFT</th>
            <th class="num">输入</th><th class="num">输出</th><th class="num">成本</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in rows" :key="r.id">
            <td class="muted nowrap">{{ formatTime(r.ts) }}</td>
            <td><span class="badge" :class="r.ingress_protocol">{{ r.ingress_protocol }}</span></td>
            <td><code>{{ r.model }}</code><div class="muted small">{{ r.provider }} / {{ r.endpoint }}</div></td>
            <td class="muted small">{{ r.upstream_model }}</td>
            <td class="num">
              <span :class="r.status < 300 ? 'status-ok' : 'status-bad'">{{ r.status }}</span>
              <div v-if="r.error_code" class="muted small">{{ r.error_code }}</div>
            </td>
            <td class="num">{{ formatMs(r.latency_ms) }}</td>
            <td class="num">{{ r.ttft_ms ? formatMs(r.ttft_ms) : '-' }}</td>
            <td class="num">{{ formatNumber(r.prompt_tokens) }}</td>
            <td class="num">{{ formatNumber(r.completion_tokens) }}</td>
            <td class="num">{{ formatUSD(r.cost_usd) }}</td>
          </tr>
          <tr v-if="!rows.length">
            <td colspan="10" class="muted">暂无日志</td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>
