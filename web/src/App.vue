<script setup>
import { onMounted, ref } from 'vue'
import { getToken, clearToken } from './api'
import Login from './views/Login.vue'
import Overview from './views/Overview.vue'
import Providers from './views/Providers.vue'
import Keys from './views/Keys.vue'
import Logs from './views/Logs.vue'
import Access from './views/Access.vue'

const tabs = [
  { id: 'overview', label: '概览', component: Overview },
  { id: 'providers', label: '供应商', component: Providers },
  { id: 'keys', label: '网关 Key', component: Keys },
  { id: 'logs', label: '请求日志', component: Logs },
  { id: 'access', label: '接入说明', component: Access },
]

const authed = ref(!!getToken())
const tab = ref(location.hash.replace('#', '') || 'overview')
const current = ref(tabs[0].component)

function resolve(id) {
  const found = tabs.find((t) => t.id === id) || tabs[0]
  tab.value = found.id
  current.value = found.component
}

resolve(tab.value)

function go(id) {
  location.hash = id
  resolve(id)
}

function logout() {
  clearToken()
  authed.value = false
}

onMounted(() => {
  window.addEventListener('llmgw-unauthorized', () => {
    authed.value = false
  })
  window.addEventListener('hashchange', () => {
    resolve(location.hash.replace('#', '') || 'overview')
  })
})
</script>

<template>
  <Login v-if="!authed" @done="authed = true" />

  <div v-else class="layout">
    <header class="topbar">
      <div class="brand">
        <span class="dot"></span>
        <strong>llmgw</strong>
        <span class="muted">控制台</span>
      </div>
      <nav class="nav">
        <button
          v-for="t in tabs"
          :key="t.id"
          :class="{ active: tab === t.id }"
          @click="go(t.id)"
        >
          {{ t.label }}
        </button>
      </nav>
      <button class="ghost" @click="logout">退出</button>
    </header>

    <main class="content">
      <component :is="current" :key="tab" />
    </main>
  </div>
</template>
