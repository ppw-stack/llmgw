<script setup lang="ts">
import { onUnmounted, ref } from 'vue';
import AppIcon from './AppIcon.vue';
import { showToast } from '../utils/toast';

const props = withDefaults(
  defineProps<{
    text: string;
    label?: string;
    copiedLabel?: string;
    toastMessage?: string;
    iconOnly?: boolean;
    compact?: boolean;
  }>(),
  {
    label: '复制',
    copiedLabel: '已复制',
    toastMessage: '已复制到剪贴板',
    iconOnly: false,
    compact: false,
  },
);

const copied = ref(false);
let resetTimer: number | undefined;

async function handleCopy() {
  try {
    await navigator.clipboard.writeText(props.text);
    copied.value = true;
    showToast(props.toastMessage, 'success', 1800);
    if (resetTimer !== undefined) {
      window.clearTimeout(resetTimer);
    }
    resetTimer = window.setTimeout(() => {
      copied.value = false;
      resetTimer = undefined;
    }, 1500);
  } catch {
    showToast('当前环境无法访问剪贴板', 'error');
  }
}

onUnmounted(() => {
  if (resetTimer !== undefined) {
    window.clearTimeout(resetTimer);
    resetTimer = undefined;
  }
});
</script>

<template>
  <button
    type="button"
    :class="iconOnly ? 'icon-btn' : ['ghost', { sm: compact }]"
    :aria-label="copied ? copiedLabel : label"
    :title="copied ? copiedLabel : label"
    @click="handleCopy"
  >
    <AppIcon :name="copied ? 'check' : 'copy'" :size="14" />
    <span v-if="!iconOnly">{{ copied ? copiedLabel : label }}</span>
  </button>
</template>
