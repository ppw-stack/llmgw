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
  if (event.key === 'Escape') {
    emit('close');
  }
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
