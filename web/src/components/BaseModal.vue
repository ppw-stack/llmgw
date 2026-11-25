<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue';
import AppIcon from './AppIcon.vue';

withDefaults(
  defineProps<{
    title: string;
    wide?: boolean;
  }>(),
  { wide: false },
);

const emit = defineEmits<{ close: [] }>();

const panel = ref<HTMLElement | null>(null);

const FIELD_SELECTOR = 'input, textarea, select, [contenteditable]';

/** 记录字段的基线值，用于判断 Esc 时是否存在未保存的改动。 */
const baselineValues = new Map<Element, string>();

function fieldValue(field: Element): string {
  if (field instanceof HTMLInputElement) {
    return field.type === 'checkbox' || field.type === 'radio'
      ? String(field.checked)
      : field.value;
  }
  if (field instanceof HTMLTextAreaElement || field instanceof HTMLSelectElement) {
    return field.value;
  }
  return field.textContent;
}

/** 动态新增的字段在首次获得焦点时记录基线，避免把默认值误判为改动。 */
function onFocusIn(event: FocusEvent) {
  const target = event.target;
  if (!(target instanceof Element)) {
    return;
  }
  const field = target.closest(FIELD_SELECTOR);
  if (field && !baselineValues.has(field)) {
    baselineValues.set(field, fieldValue(field));
  }
}

function hasUnsavedChanges(): boolean {
  for (const [field, baseline] of baselineValues) {
    if (!panel.value?.contains(field)) {
      continue;
    }
    if (fieldValue(field) !== baseline) {
      return true;
    }
  }
  return false;
}

function onKeydown(event: KeyboardEvent) {
  if (event.key !== 'Escape' || event.defaultPrevented) {
    return;
  }
  // 仅在存在未保存改动时不关闭；焦点在面板、按钮或未改动的字段时都允许 Esc 关闭。
  if (hasUnsavedChanges()) {
    return;
  }
  emit('close');
}

onMounted(() => {
  panel.value?.querySelectorAll(FIELD_SELECTOR).forEach((field) => {
    baselineValues.set(field, fieldValue(field));
  });
  // 让面板获得初始焦点，Esc 才能在任何场景下都到达全局监听器。
  panel.value?.focus();
  window.addEventListener('keydown', onKeydown);
});

onUnmounted(() => {
  window.removeEventListener('keydown', onKeydown);
});
</script>

<template>
  <div class="modal-backdrop" role="presentation" @click.self="emit('close')">
    <div
      ref="panel"
      class="modal-panel"
      :class="{ wide }"
      role="dialog"
      aria-modal="true"
      :aria-label="title"
      tabindex="-1"
      @focusin="onFocusIn"
    >
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
