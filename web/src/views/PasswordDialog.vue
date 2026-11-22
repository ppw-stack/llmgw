<script setup lang="ts">
import { ref } from 'vue';
import { api } from '../api';
import { errorMessage } from '../utils/error';

const emit = defineEmits<{ close: []; changed: [] }>();

const oldPassword = ref('');
const newPassword = ref('');
const confirm = ref('');
const error = ref('');
const busy = ref(false);

async function submit() {
  error.value = '';
  if (newPassword.value.length < 12) {
    error.value = '新密码至少 12 个字符';
    return;
  }
  if (newPassword.value !== confirm.value) {
    error.value = '两次输入的新密码不一致';
    return;
  }
  busy.value = true;
  try {
    await api<null>('/password', {
      method: 'POST',
      body: JSON.stringify({
        old_password: oldPassword.value,
        new_password: newPassword.value,
      }),
    });
    emit('changed');
  } catch (e) {
    error.value = errorMessage(e);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <div class="modal-backdrop" role="presentation" @click.self="emit('close')">
    <div class="card modal">
      <h3>修改密码</h3>
      <p class="muted">修改后所有登录会话都会失效，需要重新登录。</p>
      <form @submit.prevent="submit">
        <label for="password-old">当前密码</label>
        <input
          id="password-old"
          v-model="oldPassword"
          type="password"
          placeholder="当前密码"
          autocomplete="current-password"
        />
        <label for="password-new">新密码（至少 12 位）</label>
        <input
          id="password-new"
          v-model="newPassword"
          type="password"
          placeholder="新密码（至少 12 位）"
          autocomplete="new-password"
        />
        <label for="password-confirm">确认新密码</label>
        <input
          id="password-confirm"
          v-model="confirm"
          type="password"
          placeholder="确认新密码"
          autocomplete="new-password"
        />
        <p v-if="error" class="error">{{ error }}</p>
        <div class="row">
          <button class="primary" type="submit" :disabled="busy">
            {{ busy ? '保存中…' : '保存' }}
          </button>
          <button class="ghost" type="button" @click="emit('close')">取消</button>
        </div>
      </form>
    </div>
  </div>
</template>
