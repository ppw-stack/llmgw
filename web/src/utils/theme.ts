import { ref } from 'vue';

export type ThemeMode = 'system' | 'light' | 'dark';

const STORAGE_KEY = 'llmgw-theme';
const ORDER: readonly ThemeMode[] = ['system', 'light', 'dark'];

export const themeMode = ref<ThemeMode>('system');

function isThemeMode(value: string | null): value is ThemeMode {
  return value === 'system' || value === 'light' || value === 'dark';
}

function applyTheme(mode: ThemeMode) {
  const root = document.documentElement;
  if (mode === 'system') {
    root.removeAttribute('data-theme');
  } else {
    root.setAttribute('data-theme', mode);
  }
}

/** 启动前读取持久化主题并同步到根节点，避免首屏闪烁。 */
export function initTheme() {
  try {
    const saved = window.localStorage.getItem(STORAGE_KEY);
    if (isThemeMode(saved)) {
      themeMode.value = saved;
    }
  } catch {
    /* 隐身模式等环境可能禁止读写 localStorage */
  }
  applyTheme(themeMode.value);
}

/** 设置主题模式并持久化。 */
export function setThemeMode(mode: ThemeMode) {
  themeMode.value = mode;
  applyTheme(mode);
  try {
    window.localStorage.setItem(STORAGE_KEY, mode);
  } catch {
    /* 忽略持久化失败 */
  }
}

/** 在「跟随系统 → 亮色 → 深色」之间循环切换。 */
export function cycleThemeMode() {
  const idx = ORDER.indexOf(themeMode.value);
  const next = ORDER[(idx + 1) % ORDER.length] ?? 'system';
  setThemeMode(next);
}

export function themeLabel(mode: ThemeMode): string {
  switch (mode) {
    case 'light':
      return '亮色模式';
    case 'dark':
      return '深色模式';
    default:
      return '跟随系统';
  }
}
