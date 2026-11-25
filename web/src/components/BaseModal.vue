<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue';
import AppIcon from './AppIcon.vue';

withDefaults(
  defineProps<{
    title: string;
    wide?: boolean;
  }>(),
  { wide: false },
);

const emit = defineEmits<{ close: [] }>();

function onKeydown(event: KeyboardEvent) {
  if (event.key !== 'Escape' || event.defaultPrevented) {
    return;
  }
  const target = event.target;
  if (!(target instanceof Element) || !target.closest('.modal-panel')) {
    return;
  }
  // 正在编辑表单控件时按 Esc 只交给控件自身处理，避免无确认地丢弃未保存的输入。
  if (target.closest('input, textarea, select, [contenteditable]')) {
    return;
  }
  emit('close');
}

onMounted(() => {
  window.addEventListener('keydown', onKeydown);
});

onUnmounted(() => {
  window.removeEventListener('keydown', onKeydown);
});
</script>

<template>
  <div class="modal-backdrop" role="presentation" @click.self="emit('close')">
    <div class="modal-panel" :class="{ wide }" role="dialog" aria-modal="true" :aria-label="title">
      <div class="modal-header">
        <h3>{{ title }}</h3>
        <button type="button" class="icon-btn" aria-label="关闭弹窗" @click="emit('close')">
          <AppIcon name="close" :size="16" />
        </button>
      </div>
      <div class="modal-body">
        <slot />
      </div>
      <div v-if="$slots.footer" class="modal-footer">
        <slot name="footer" />
      </div>
    </div>
  </div>
</template>
