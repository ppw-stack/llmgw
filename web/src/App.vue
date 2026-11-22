<script setup>
import { onMounted, ref } from 'vue'
import { api } from './api'
import Login from './views/Login.vue'
import PasswordDialog from './views/PasswordDialog.vue'
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

const checking = ref(true)
const authed = ref(false)
const username = ref('')
const showPassword = ref(false)
const tab = ref('overview')
const current = ref(tabs[0].component)

function resolve(id) {
  const found = tabs.find((t) => t.id === id) || tabs[0]
  tab.value = found.id
  current.value = found.component
}

resolve(location.hash.replace('#', '') || 'overview')

function go(id) {
  location.hash = id
  resolve(id)
}

async function check() {
  checking.value = true
  try {
    const me = await api('/me')
    username.value = me.username
    authed.value = true
  } catch {
    authed.value = false
  } finally {
    checking.value = false
  }
}

async function logout() {
  try {
    await api('/logout', { method: 'POST' })
  } catch {
    /* session may already be gone */
  }
  authed.value = false
}

function onPasswordChanged() {
  showPassword.value = false
  authed.value = false
}

onMounted(() => {
  check()
  window.addEventListener('llmgw-unauthorized', () => {
    authed.value = false
  })
  window.addEventListener('hashchange', () => {
    resolve(location.hash.replace('#', '') || 'overview')
  })
})
</script>

<template>
  <div v-if="checking" class="login-wrap">
    <p class="muted">加载中…</p>
  </div>

  <Login v-else-if="!authed" @done="check" />

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
      <div class="row">
        <span class="muted small">{{ username }}</span>
        <button class="ghost" @click="showPassword = true">修改密码</button>
        <button class="ghost" @click="logout">退出</button>
      </div>
    </header>

    <main class="content">
      <component :is="current" :key="tab" />
    </main>

    <PasswordDialog
      v-if="showPassword"
      @close="showPassword = false"
      @changed="onPasswordChanged"
    />
  </div>
</template>
