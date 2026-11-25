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

/**
 * 访问 localStorage 的安全封装：隐身模式或隐私策略可能直接抛出异常，
 * 此时返回 fallback，避免把失败静默吞在空 catch 里。
 */
function withStorage<T>(operation: () => T, fallback: T): T {
  try {
    return operation();
  } catch {
    return fallback;
  }
}

/** 启动前读取持久化主题并同步到根节点，避免首屏闪烁。 */
export function initTheme() {
  const saved = withStorage(() => window.localStorage.getItem(STORAGE_KEY), null);
  if (isThemeMode(saved)) {
    themeMode.value = saved;
  }
  applyTheme(themeMode.value);
}

/** 设置主题模式并持久化；存储不可用时仅本次会话生效。 */
export function setThemeMode(mode: ThemeMode) {
  themeMode.value = mode;
  applyTheme(mode);
  withStorage(() => {
    window.localStorage.setItem(STORAGE_KEY, mode);
  }, undefined);
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
