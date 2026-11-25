import { ref } from 'vue';

export type ToastKind = 'success' | 'error' | 'info';

export interface ToastItem {
  id: number;
  message: string;
  kind: ToastKind;
}

const DEFAULT_DURATION_MS = 2400;
let seq = 0;

export const toasts = ref<ToastItem[]>([]);

/** 展示一条轻提示，自动在短时间后淡出。 */
export function showToast(message: string, kind: ToastKind = 'success', durationMs?: number) {
  const id = ++seq;
  toasts.value = [...toasts.value.slice(-3), { id, message, kind }];
  window.setTimeout(() => {
    dismissToast(id);
  }, durationMs ?? DEFAULT_DURATION_MS);
}

export function dismissToast(id: number) {
  toasts.value = toasts.value.filter((item) => item.id !== id);
}
