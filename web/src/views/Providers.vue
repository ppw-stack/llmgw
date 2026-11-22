<script setup>
import { onMounted, ref } from 'vue'
import { api } from '../api'

const providers = ref([])
const loading = ref(true)
const error = ref('')

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

onMounted(load)
</script>

<template>
  <section>
    <div class="section-head">
      <h2>供应商</h2>
      <button class="ghost" @click="load">刷新</button>
    </div>
    <p class="muted">
      供应商与端点来自 <code>config.yaml</code>，修改后 1 秒内自动生效。
    </p>
    <p v-if="error" class="error">{{ error }}</p>
    <p v-if="loading" class="muted">加载中…</p>

    <div v-else-if="!providers.length" class="card muted">尚未配置供应商</div>

    <div v-for="p in providers" :key="p.name" class="card provider">
      <div class="provider-head">
        <h3>{{ p.name }}</h3>
        <span class="badge">{{ p.keys }} 个上游 Key</span>
      </div>

      <div v-for="ep in p.endpoints" :key="ep.name" class="endpoint">
        <div class="endpoint-head">
          <span class="badge" :class="ep.protocol">{{ ep.protocol }}</span>
          <code class="base">{{ ep.base_url }}</code>
        </div>
        <div class="models">
          <span v-for="(upstream, alias) in ep.models" :key="alias" class="tag">
            {{ alias }}<template v-if="alias !== upstream"> → {{ upstream }}</template>
          </span>
        </div>
      </div>
    </div>
  </section>
</template>
