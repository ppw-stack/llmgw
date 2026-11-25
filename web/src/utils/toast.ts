import { ref } from 'vue';

export type ToastKind = 'success' | 'error' | 'info';

export interface ToastItem {
  id: number;
  message: string;
  kind: ToastKind;
}

const DEFAULT_DURATION_MS = 2400;
const MAX_TOASTS = 4;
let seq = 0;

export const toasts = ref<ToastItem[]>([]);

/** 每条轻提示的自动消失定时器，按 id 索引以便统一清理。 */
const timers = new Map<number, number>();

function clearToastTimer(id: number) {
  const timer = timers.get(id);
  if (timer !== undefined) {
    window.clearTimeout(timer);
    timers.delete(id);
  }
}

/** 展示一条轻提示，自动在短时间后淡出。 */
export function showToast(message: string, kind: ToastKind = 'success', durationMs?: number) {
  const id = ++seq;
  const next = [...toasts.value, { id, message, kind }];
  const dropped = next.slice(0, Math.max(next.length - MAX_TOASTS, 0));
  for (const item of dropped) {
    clearToastTimer(item.id);
  }
  toasts.value = next.slice(-MAX_TOASTS);
  const timer = window.setTimeout(() => {
    dismissToast(id);
  }, durationMs ?? DEFAULT_DURATION_MS);
  timers.set(id, timer);
}

export function dismissToast(id: number) {
  clearToastTimer(id);
  toasts.value = toasts.value.filter((item) => item.id !== id);
}

/** 清空所有轻提示并取消未触发的定时器，供宿主组件卸载时调用。 */
export function clearToasts() {
  for (const timer of timers.values()) {
    window.clearTimeout(timer);
  }
  timers.clear();
  toasts.value = [];
}
