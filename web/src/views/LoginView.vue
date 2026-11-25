<script setup lang="ts">
import { computed, ref } from 'vue';
import { api } from '../api';
import AppIcon, { type IconName } from '../components/AppIcon.vue';
import { errorMessage } from '../utils/error';
import { cycleThemeMode, themeLabel, themeMode } from '../utils/theme';

const emit = defineEmits<{ done: [] }>();

const username = ref('admin');
const password = ref('');
const showPassword = ref(false);
const error = ref('');
const busy = ref(false);

const themeIcon = computed<IconName>(() => {
  if (themeMode.value === 'light') {
    return 'sun';
  }
  if (themeMode.value === 'dark') {
    return 'moon';
  }
  return 'monitor';
});

async function submit() {
  if (!password.value) {
    error.value = '请输入密码';
    return;
  }
  busy.value = true;
  error.value = '';
  try {
    await api<null>('/login', {
      method: 'POST',
      body: JSON.stringify({ username: username.value.trim(), password: password.value }),
    });
    emit('done');
  } catch (e) {
    error.value = errorMessage(e);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <div class="login-page">
    <button
      type="button"
      class="ghost sm login-theme-btn"
      :aria-label="`切换主题，当前为${themeLabel(themeMode)}`"
      @click="cycleThemeMode"
    >
      <AppIcon :name="themeIcon" :size="14" />
      <span>{{ themeLabel(themeMode) }}</span>
    </button>

    <div class="card login-card">
      <div class="brand-mark">
        <span class="brand-icon">
          <AppIcon name="spark" :size="16" />
        </span>
        <div class="brand-text">
          <span class="brand-name">llmgw</span>
          <span class="brand-sub">LLM Gateway 控制台</span>
        </div>
      </div>

      <p class="secondary small">输入管理员凭据以管理上游接入点、路由模型与访问密钥。</p>

      <form @submit.prevent="submit">
        <div class="field">
          <label for="login-username">用户名</label>
          <input
            id="login-username"
            v-model="username"
            placeholder="admin"
            autocomplete="username"
          />
        </div>

        <div class="field">
          <label for="login-password">密码</label>
          <div class="input-wrap">
            <input
              id="login-password"
              v-model="password"
              :type="showPassword ? 'text' : 'password'"
              placeholder="输入管理员密码"
              autocomplete="current-password"
            />
            <span class="input-suffix">
              <button
                type="button"
                class="icon-btn"
                :aria-label="showPassword ? '隐藏密码' : '显示密码'"
                @click="showPassword = !showPassword"
              >
                <AppIcon :name="showPassword ? 'eye-off' : 'eye'" :size="15" />
              </button>
            </span>
          </div>
        </div>

        <p v-if="error" class="error">{{ error }}</p>

        <button class="primary" type="submit" :disabled="busy">
          {{ busy ? '登录中…' : '登录控制台' }}
        </button>
      </form>

      <p class="login-hint">
        首次启动时密码会写入数据库旁的 <code>admin-password.txt</code>，或由
        <code>server.admin_password</code> 指定。
      </p>
    </div>
  </div>
</template>
