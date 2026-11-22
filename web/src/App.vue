<script setup lang="ts">
import { onMounted, onUnmounted, ref, type Component } from 'vue';
import { api } from './api';
import type { Me } from './types';
import LoginView from './views/LoginView.vue';
import PasswordDialog from './views/PasswordDialog.vue';
import OverviewView from './views/OverviewView.vue';
import ProvidersView from './views/ProvidersView.vue';
import KeysView from './views/KeysView.vue';
import LogsView from './views/LogsView.vue';
import AccessView from './views/AccessView.vue';

interface Tab {
  id: string;
  label: string;
  component: Component;
}

const tabs = [
  { id: 'overview', label: '概览', component: OverviewView },
  { id: 'providers', label: '供应商', component: ProvidersView },
  { id: 'keys', label: '网关 Key', component: KeysView },
  { id: 'logs', label: '请求日志', component: LogsView },
  { id: 'access', label: '接入说明', component: AccessView },
] as const satisfies readonly Tab[];

const checking = ref(true);
const authed = ref(false);
const username = ref('');
const showPassword = ref(false);
const tab = ref('overview');
const current = ref<Component>(tabs[0].component);

function resolve(id: string) {
  const found = tabs.find((t) => t.id === id) ?? tabs[0];
  tab.value = found.id;
  current.value = found.component;
}

resolve(location.hash.replace('#', '') || 'overview');

function go(id: string) {
  location.hash = id;
  resolve(id);
}

async function check() {
  checking.value = true;
  try {
    const me = await api<Me>('/me');
    username.value = me.username;
    authed.value = true;
  } catch {
    authed.value = false;
  } finally {
    checking.value = false;
  }
}

async function logout() {
  try {
    await api<null>('/logout', { method: 'POST' });
  } catch {
    /* 会话可能已经失效 */
  }
  authed.value = false;
}

function onPasswordChanged() {
  showPassword.value = false;
  authed.value = false;
}

function onUnauthorized() {
  authed.value = false;
}

function onHashChange() {
  resolve(location.hash.replace('#', '') || 'overview');
}

onMounted(() => {
  void check();
  window.addEventListener('llmgw-unauthorized', onUnauthorized);
  window.addEventListener('hashchange', onHashChange);
});

onUnmounted(() => {
  window.removeEventListener('llmgw-unauthorized', onUnauthorized);
  window.removeEventListener('hashchange', onHashChange);
});
</script>

<template>
  <div v-if="checking" class="login-wrap">
    <p class="muted">加载中…</p>
  </div>

  <LoginView v-else-if="!authed" @done="check" />

  <div v-else class="layout">
    <header class="topbar">
      <div class="brand">
        <span class="dot" />
        <strong>llmgw</strong>
        <span class="muted">控制台</span>
      </div>
      <nav class="nav">
        <button v-for="t in tabs" :key="t.id" :class="{ active: tab === t.id }" @click="go(t.id)">
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
