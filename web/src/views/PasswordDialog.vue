<script setup lang="ts">
import { computed, ref } from 'vue';
import { api } from '../api';
import AppIcon from '../components/AppIcon.vue';
import BaseModal from '../components/BaseModal.vue';
import { errorMessage } from '../utils/error';

const emit = defineEmits<{ close: []; changed: [] }>();

const oldPassword = ref('');
const newPassword = ref('');
const confirm = ref('');
const showOld = ref(false);
const showNew = ref(false);
const error = ref('');
const busy = ref(false);

const lengthOk = computed(() => newPassword.value.length >= 12);
const matchOk = computed(() => confirm.value.length > 0 && newPassword.value === confirm.value);

async function submit() {
  error.value = '';
  if (!oldPassword.value) {
    error.value = '请输入当前密码';
    return;
  }
  if (!lengthOk.value) {
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
  <BaseModal title="修改管理员密码" @close="emit('close')">
    <p class="secondary small">修改成功后，所有现有登录会话都会失效，需要使用新密码重新登录。</p>

    <form id="password-form" class="stack" @submit.prevent="submit">
      <div class="field">
        <label for="password-old">当前密码</label>
        <div class="input-wrap">
          <input
            id="password-old"
            v-model="oldPassword"
            :type="showOld ? 'text' : 'password'"
            placeholder="输入当前密码"
            autocomplete="current-password"
          />
          <span class="input-suffix">
            <button
              type="button"
              class="icon-btn"
              :aria-label="showOld ? '隐藏当前密码' : '显示当前密码'"
              @click="showOld = !showOld"
            >
              <AppIcon :name="showOld ? 'eye-off' : 'eye'" :size="15" />
            </button>
          </span>
        </div>
      </div>

      <div class="field">
        <label for="password-new">新密码</label>
        <div class="input-wrap">
          <input
            id="password-new"
            v-model="newPassword"
            :type="showNew ? 'text' : 'password'"
            placeholder="至少 12 位字符"
            autocomplete="new-password"
          />
          <span class="input-suffix">
            <button
              type="button"
              class="icon-btn"
              :aria-label="showNew ? '隐藏新密码' : '显示新密码'"
              @click="showNew = !showNew"
            >
              <AppIcon :name="showNew ? 'eye-off' : 'eye'" :size="15" />
            </button>
          </span>
        </div>
      </div>

      <div class="field">
        <label for="password-confirm">确认新密码</label>
        <input
          id="password-confirm"
          v-model="confirm"
          :type="showNew ? 'text' : 'password'"
          placeholder="再次输入新密码"
          autocomplete="new-password"
        />
      </div>

      <div class="row small">
        <span class="badge" :class="lengthOk ? 'ok' : 'off'">
          <AppIcon v-if="lengthOk" name="check" :size="12" />
          至少 12 位
        </span>
        <span class="badge" :class="matchOk ? 'ok' : 'off'">
          <AppIcon v-if="matchOk" name="check" :size="12" />
          两次输入一致
        </span>
      </div>
    </form>

    <template #footer>
      <p v-if="error" class="error">{{ error }}</p>
      <div class="row space-between">
        <button class="ghost" type="button" @click="emit('close')">取消</button>
        <button class="primary" form="password-form" type="submit" :disabled="busy">
          {{ busy ? '保存中…' : '保存新密码' }}
        </button>
      </div>
    </template>
  </BaseModal>
</template>
