<script setup>
import { ref } from 'vue'
import { api, setToken } from '../api'

const emit = defineEmits(['done'])
const token = ref('')
const error = ref('')
const busy = ref(false)

async function submit() {
  if (!token.value.trim()) {
    error.value = '请输入 admin token'
    return
  }
  busy.value = true
  error.value = ''
  const previous = localStorage.getItem('llmgw_admin_token') || ''
  setToken(token.value.trim())
  try {
    await api('/providers')
    emit('done')
  } catch (e) {
    setToken(previous)
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
      <p class="muted">输入管理令牌以进入控制台</p>
      <form @submit.prevent="submit">
        <input
          v-model="token"
          type="password"
          placeholder="admin token"
          autocomplete="current-password"
        />
        <button class="primary" type="submit" :disabled="busy">
          {{ busy ? '验证中…' : '进入' }}
        </button>
      </form>
      <p v-if="error" class="error">{{ error }}</p>
      <p class="hint">
        首次启动时令牌会写入数据库旁的 <code>admin-token.txt</code>，或由
        <code>server.admin_token</code> 指定。
      </p>
    </div>
  </div>
</template>
