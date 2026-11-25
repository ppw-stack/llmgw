<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, type Component } from 'vue';
import { api } from './api';
import AppIcon, { type IconName } from './components/AppIcon.vue';
import ToastHost from './components/ToastHost.vue';
import type { Me } from './types';
import { cycleThemeMode, themeLabel, themeMode } from './utils/theme';
import { showToast } from './utils/toast';
import LoginView from './views/LoginView.vue';
import PasswordDialog from './views/PasswordDialog.vue';
import OverviewView from './views/OverviewView.vue';
import InstancesView from './views/InstancesView.vue';
import ModelsView from './views/ModelsView.vue';
import KeysView from './views/KeysView.vue';
import LogsView from './views/LogsView.vue';
import AccessView from './views/AccessView.vue';

interface NavTab {
  id: string;
  label: string;
  icon: IconName;
  group: 'observe' | 'routing' | 'access';
  component: Component;
}

const tabs = [
  { id: 'overview', label: '概览', icon: 'overview', group: 'observe', component: OverviewView },
  { id: 'logs', label: '请求日志', icon: 'logs', group: 'observe', component: LogsView },
  {
    id: 'instances',
    label: '上游接入点',
    icon: 'instances',
    group: 'routing',
    component: InstancesView,
  },
  { id: 'models', label: '模型', icon: 'models', group: 'routing', component: ModelsView },
  { id: 'keys', label: '网关 Key', icon: 'keys', group: 'access', component: KeysView },
  { id: 'access', label: '接入说明', icon: 'access', group: 'access', component: AccessView },
] as const satisfies readonly NavTab[];

const navGroups: { id: NavTab['group']; title: string }[] = [
  { id: 'observe', title: '观测' },
  { id: 'routing', title: '路由配置' },
  { id: 'access', title: '接入管理' },
];

const checking = ref(true);
const authed = ref(false);
const username = ref('');
const showPassword = ref(false);
const mobileMenuOpen = ref(false);
const tab = ref('overview');
const current = ref<Component>(tabs[0].component);

const themeIcon = computed<IconName>(() => {
  if (themeMode.value === 'light') {
    return 'sun';
  }
  if (themeMode.value === 'dark') {
    return 'moon';
  }
  return 'monitor';
});

const userInitial = computed(() => (username.value.trim()[0] ?? 'A').toUpperCase());

function tabsByGroup(group: NavTab['group']) {
  return tabs.filter((item) => item.group === group);
}

function resolve(id: string) {
  const found = tabs.find((t) => t.id === id) ?? tabs[0];
  tab.value = found.id;
  current.value = found.component;
}

resolve(location.hash.replace('#', '') || 'overview');

function go(id: string) {
  location.hash = id;
  resolve(id);
  mobileMenuOpen.value = false;
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
  mobileMenuOpen.value = false;
  authed.value = false;
}

function onPasswordChanged() {
  showPassword.value = false;
  authed.value = false;
  showToast('密码已更新，请重新登录', 'info', 3000);
}

function onUnauthorized() {
  authed.value = false;
}

function onHashChange() {
  resolve(location.hash.replace('#', '') || 'overview');
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && mobileMenuOpen.value) {
    mobileMenuOpen.value = false;
  }
}

onMounted(() => {
  void check();
  window.addEventListener('llmgw-unauthorized', onUnauthorized);
  window.addEventListener('hashchange', onHashChange);
  window.addEventListener('keydown', onKeydown);
});

onUnmounted(() => {
  window.removeEventListener('llmgw-unauthorized', onUnauthorized);
  window.removeEventListener('hashchange', onHashChange);
  window.removeEventListener('keydown', onKeydown);
});
</script>

<template>
  <div v-if="checking" class="login-page">
    <p class="muted">加载控制台…</p>
  </div>

  <LoginView v-else-if="!authed" @done="check" />

  <div v-else class="shell">
    <div
      v-if="mobileMenuOpen"
      class="drawer-backdrop"
      role="presentation"
      @click="mobileMenuOpen = false"
    />

    <aside class="sidebar" :class="{ open: mobileMenuOpen }" aria-label="主导航">
      <div class="sidebar-brand">
        <div class="brand-mark">
          <span class="brand-icon">
            <AppIcon name="spark" :size="16" />
          </span>
          <div class="brand-text">
            <span class="brand-name">llmgw</span>
            <span class="brand-sub">LLM Gateway</span>
          </div>
        </div>
        <button
          v-if="mobileMenuOpen"
          type="button"
          class="icon-btn"
          aria-label="关闭导航菜单"
          @click="mobileMenuOpen = false"
        >
          <AppIcon name="close" :size="16" />
        </button>
      </div>

      <nav class="sidebar-nav">
        <div v-for="group in navGroups" :key="group.id" class="nav-group">
          <span class="nav-group-title">{{ group.title }}</span>
          <button
            v-for="item in tabsByGroup(group.id)"
            :key="item.id"
            type="button"
            class="nav-item"
            :class="{ active: tab === item.id }"
            :aria-current="tab === item.id ? 'page' : undefined"
            @click="go(item.id)"
          >
            <AppIcon :name="item.icon" :size="16" />
            <span>{{ item.label }}</span>
          </button>
        </div>
      </nav>

      <div class="sidebar-footer">
        <div class="user-pill">
          <div class="user-pill-info">
            <span class="user-avatar" aria-hidden="true">{{ userInitial }}</span>
            <span class="user-name" :title="username">{{ username }}</span>
          </div>
          <button
            type="button"
            class="icon-btn"
            :title="`切换主题（当前：${themeLabel(themeMode)}）`"
            :aria-label="`切换主题，当前为${themeLabel(themeMode)}`"
            @click="cycleThemeMode"
          >
            <AppIcon :name="themeIcon" :size="15" />
          </button>
        </div>
        <div class="sidebar-actions">
          <button
            type="button"
            class="ghost sm"
            @click="
              mobileMenuOpen = false;
              showPassword = true;
            "
          >
            <AppIcon name="lock" :size="13" />
            <span>改密</span>
          </button>
          <button type="button" class="ghost sm" @click="logout">
            <AppIcon name="logout" :size="13" />
            <span>退出</span>
          </button>
        </div>
      </div>
    </aside>

    <div class="main-area">
      <header class="mobile-bar">
        <div class="row">
          <button
            type="button"
            class="icon-btn"
            aria-label="打开导航菜单"
            @click="mobileMenuOpen = true"
          >
            <AppIcon name="menu" :size="18" />
          </button>
          <div class="brand-mark">
            <span class="brand-icon">
              <AppIcon name="spark" :size="14" />
            </span>
            <span class="brand-name">llmgw</span>
          </div>
        </div>
        <button
          type="button"
          class="icon-btn"
          :title="`切换主题（当前：${themeLabel(themeMode)}）`"
          :aria-label="`切换主题，当前为${themeLabel(themeMode)}`"
          @click="cycleThemeMode"
        >
          <AppIcon :name="themeIcon" :size="16" />
        </button>
      </header>

      <main class="content">
        <component :is="current" :key="tab" />
      </main>
    </div>

    <PasswordDialog
      v-if="showPassword"
      @close="showPassword = false"
      @changed="onPasswordChanged"
    />
  </div>

  <ToastHost />
</template>
