<script setup>
import { ref } from 'vue'
import { api } from '../api'

const emit = defineEmits(['done'])
const username = ref('admin')
const password = ref('')
const error = ref('')
const busy = ref(false)

async function submit() {
  if (!password.value) {
    error.value = '请输入密码'
    return
  }
  busy.value = true
  error.value = ''
  try {
    await api('/login', {
      method: 'POST',
      body: JSON.stringify({ username: username.value.trim(), password: password.value }),
    })
    emit('done')
  } catch (e) {
    error.value = e.message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="login-wrap">
    <div class="card login-card">
      <h1>llmgw</h1>
      <p class="muted">登录控制台</p>
      <form @submit.prevent="submit">
        <input v-model="username" placeholder="用户名" autocomplete="username" />
        <input
          v-model="password"
          type="password"
          placeholder="密码"
          autocomplete="current-password"
          autofocus
        />
        <button class="primary" type="submit" :disabled="busy">
          {{ busy ? '登录中…' : '登录' }}
        </button>
      </form>
      <p v-if="error" class="error">{{ error }}</p>
      <p class="hint">
        首次启动时密码会写入数据库旁的 <code>admin-password.txt</code>，或由
        <code>server.admin_password</code> 指定。
      </p>
    </div>
  </div>
</template>
