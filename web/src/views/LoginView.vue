<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue';
import { api, healthz } from '../api';
import AppIcon, { type IconName } from '../components/AppIcon.vue';
import { errorMessage } from '../utils/error';
import {
  initGatewayCanvas,
  type GatewayCanvasController,
  type GatewayCanvasPalette,
} from '../utils/gatewayCanvas';
import { cycleThemeMode, themeLabel, themeMode } from '../utils/theme';

const emit = defineEmits<{ done: [] }>();

interface CapabilityCard {
  id: string;
  icon: IconName;
  title: string;
  desc: string;
}

interface RoutePreview {
  method: string;
  path: string;
  target: string;
}

const FEATURE_PILLS = [
  'OpenAI / Anthropic 兼容',
  '多上游故障切换',
  '时段路由与额度观测',
  '密钥限速与审计',
] as const;

const CAPABILITY_CARDS: readonly CapabilityCard[] = [
  {
    id: 'logs',
    icon: 'logs',
    title: '观测与请求日志',
    desc: '实时耗时、Token 计量与状态追踪',
  },
  {
    id: 'instances',
    icon: 'instances',
    title: '上游接入点',
    desc: '多渠道池化、额度探测与故障切换',
  },
  {
    id: 'models',
    icon: 'models',
    title: '模型路由分发',
    desc: '协议映射、权重调度与时段策略',
  },
  {
    id: 'keys',
    icon: 'keys',
    title: '网关 Key 管理',
    desc: '多租户密钥签发、RPM 与并发限流',
  },
];

const ROUTE_PREVIEWS: readonly RoutePreview[] = [
  { method: 'POST', path: '/openai/v1/chat/completions', target: 'upstream:openai-pool' },
  { method: 'POST', path: '/anthropic/v1/messages', target: 'upstream:anthropic-pool' },
  { method: 'POST', path: '/openai/v1/chat/completions', target: 'upstream:deepseek-flash' },
  { method: 'GET', path: '/openai/v1/models', target: 'router:model-catalog' },
];

const LIGHT_PALETTE: GatewayCanvasPalette = {
  bg: '#FAF9F5',
  gridDot: 'rgba(155, 149, 140, 0.16)',
  cardBg: 'rgba(250, 249, 245, 0.92)',
  cardBgActive: 'rgba(250, 249, 245, 0.98)',
  borderSubtle: '#E6E2D8',
  inkPrimary: '#262522',
  inkSecondary: '#6E6A63',
  inkMuted: '#9B958C',
  accent: '#C96442',
  accentRgb: '201, 100, 66',
  mutedRgb: '155, 149, 140',
  inLineIdle: 'rgba(212, 206, 192, 0.65)',
  hubStar: '#FAF9F5',
  fontSans: 'ui-sans-serif, system-ui, -apple-system, "PingFang SC", sans-serif',
  fontMono: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
};

const DARK_PALETTE: GatewayCanvasPalette = {
  bg: '#161513',
  gridDot: 'rgba(188, 181, 169, 0.13)',
  cardBg: 'rgba(34, 32, 29, 0.9)',
  cardBgActive: 'rgba(40, 37, 33, 0.98)',
  borderSubtle: 'rgba(255, 248, 235, 0.11)',
  inkPrimary: '#F3EFE6',
  inkSecondary: '#BCB5A9',
  inkMuted: '#8C867C',
  accent: '#D97757',
  accentRgb: '217, 119, 87',
  mutedRgb: '140, 134, 124',
  inLineIdle: 'rgba(188, 181, 169, 0.22)',
  hubStar: '#1B1613',
  fontSans: 'ui-sans-serif, system-ui, -apple-system, "PingFang SC", sans-serif',
  fontMono: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
};

const canvasEl = ref<HTMLCanvasElement | null>(null);
const usernameInput = ref<HTMLInputElement | null>(null);
const passwordInput = ref<HTMLInputElement | null>(null);

const modalOpen = ref(false);
const username = ref('');
const password = ref('');
const showPassword = ref(false);
const error = ref('');
const busy = ref(false);
const shaking = ref(false);
const loginSuccess = ref(false);

const healthStatus = ref<'checking' | 'online' | 'offline'>('checking');
const healthLatencyMs = ref<number | null>(null);
const probing = ref(false);

const tickerIndex = ref(0);
const currentRoute = computed<RoutePreview>(() => {
  return (
    ROUTE_PREVIEWS[tickerIndex.value] ?? {
      method: 'POST',
      path: '/openai/v1/chat/completions',
      target: 'upstream:openai-pool',
    }
  );
});

const themeIcon = computed<IconName>(() => {
  if (themeMode.value === 'light') {
    return 'sun';
  }
  if (themeMode.value === 'dark') {
    return 'moon';
  }
  return 'monitor';
});

const healthLabel = computed(() => {
  if (healthStatus.value === 'online') {
    return 'GET /healthz · Online';
  }
  if (healthStatus.value === 'offline') {
    return 'GET /healthz · Offline';
  }
  return 'GET /healthz';
});

const healthMeta = computed(() => {
  if (healthStatus.value === 'online' && healthLatencyMs.value !== null) {
    return `${healthLatencyMs.value}ms`;
  }
  if (healthStatus.value === 'offline') {
    return '重试';
  }
  return '检测中…';
});

let canvasController: GatewayCanvasController | null = null;
let colorSchemeQuery: MediaQueryList | null = null;
let healthAbort: AbortController | null = null;
let tickerTimer: number | null = null;
let shakeTimer: number | null = null;
let successTimer: number | null = null;

function isDarkMode(): boolean {
  if (themeMode.value === 'dark') {
    return true;
  }
  if (themeMode.value === 'light') {
    return false;
  }
  return colorSchemeQuery?.matches ?? false;
}

function getCanvasPalette(): GatewayCanvasPalette {
  return isDarkMode() ? DARK_PALETTE : LIGHT_PALETTE;
}

function triggerShake(): void {
  shaking.value = false;
  if (shakeTimer !== null) {
    window.clearTimeout(shakeTimer);
    shakeTimer = null;
  }
  window.requestAnimationFrame(() => {
    shaking.value = true;
    shakeTimer = window.setTimeout(() => {
      shaking.value = false;
      shakeTimer = null;
    }, 380);
  });
}

async function probeHealth(): Promise<void> {
  if (probing.value) {
    return;
  }
  probing.value = true;
  healthStatus.value = 'checking';
  healthAbort?.abort();
  const controller = new AbortController();
  healthAbort = controller;
  const timeoutId = window.setTimeout(() => {
    controller.abort();
  }, 1500);

  try {
    const result = await healthz(controller.signal);
    if (result.ok) {
      healthStatus.value = 'online';
      healthLatencyMs.value = result.latencyMs;
    } else {
      healthStatus.value = 'offline';
      healthLatencyMs.value = null;
    }
  } catch {
    healthStatus.value = 'offline';
    healthLatencyMs.value = null;
  } finally {
    window.clearTimeout(timeoutId);
    probing.value = false;
  }
}

function openModal(targetTab?: string): void {
  if (targetTab) {
    location.hash = targetTab;
  }
  error.value = '';
  modalOpen.value = true;
  void nextTick(() => {
    if (username.value.trim()) {
      passwordInput.value?.focus();
    } else {
      usernameInput.value?.focus();
    }
  });
}

function closeModal(): void {
  if (busy.value || loginSuccess.value) {
    return;
  }
  modalOpen.value = false;
  error.value = '';
}

async function submit(): Promise<void> {
  if (busy.value || loginSuccess.value) {
    return;
  }
  if (!password.value) {
    error.value = '请输入密码';
    triggerShake();
    passwordInput.value?.focus();
    return;
  }
  busy.value = true;
  error.value = '';
  try {
    await api<null>('/login', {
      method: 'POST',
      body: JSON.stringify({ username: username.value.trim(), password: password.value }),
    });
    loginSuccess.value = true;
    const successColor = isDarkMode() ? '#56B886' : '#2E7D5B';
    canvasController?.triggerPulse(22, successColor);
    successTimer = window.setTimeout(() => {
      emit('done');
    }, 650);
  } catch (e) {
    error.value = errorMessage(e);
    triggerShake();
  } finally {
    busy.value = false;
  }
}

function onKeydown(event: KeyboardEvent): void {
  if (event.defaultPrevented) {
    return;
  }
  if (event.key === 'Escape' && modalOpen.value) {
    event.preventDefault();
    closeModal();
    return;
  }
  if (event.key === 'Enter' && !modalOpen.value) {
    const target = event.target;
    if (target instanceof HTMLElement && target.closest('button, a, input, textarea, select')) {
      return;
    }
    event.preventDefault();
    openModal();
  }
}

function onColorSchemeChange(): void {
  canvasController?.refreshTheme();
}

/**
 * 弹窗打开时画布背后的 blur 合成每帧都要重算，暂停 rAF 只保留静态帧。
 * 登录成功后恢复动画，让成功脉冲可见。
 */
const canvasPaused = computed(() => modalOpen.value && !loginSuccess.value);

watch(canvasPaused, (paused) => {
  canvasController?.setPaused(paused);
});

watch(themeMode, () => {
  canvasController?.refreshTheme();
});

onMounted(() => {
  colorSchemeQuery = window.matchMedia('(prefers-color-scheme: dark)');
  colorSchemeQuery.addEventListener('change', onColorSchemeChange);

  if (canvasEl.value) {
    canvasController = initGatewayCanvas(canvasEl.value, {
      isModalOpen: () => modalOpen.value,
      getPalette: getCanvasPalette,
    });
  }

  void probeHealth();

  if (!window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
    tickerTimer = window.setInterval(() => {
      tickerIndex.value = (tickerIndex.value + 1) % ROUTE_PREVIEWS.length;
    }, 5200);
  }

  window.addEventListener('keydown', onKeydown);
});

onUnmounted(() => {
  canvasController?.destroy();
  canvasController = null;
  colorSchemeQuery?.removeEventListener('change', onColorSchemeChange);
  healthAbort?.abort();
  if (tickerTimer !== null) {
    window.clearInterval(tickerTimer);
  }
  if (shakeTimer !== null) {
    window.clearTimeout(shakeTimer);
  }
  if (successTimer !== null) {
    window.clearTimeout(successTimer);
  }
  window.removeEventListener('keydown', onKeydown);
});
</script>

<template>
  <div class="landing-root">
    <!-- Layer 1: 全屏高并发 Token 路由拓扑画布 -->
    <canvas
      ref="canvasEl"
      class="landing-canvas"
      :class="{ blurred: modalOpen }"
      aria-hidden="true"
    />

    <!-- Layer 2: 顶栏（品牌、真实探活、主题切换、登录入口） -->
    <header class="landing-header" :inert="modalOpen">
      <div class="brand-mark">
        <span class="brand-icon">
          <AppIcon name="spark" :size="16" />
        </span>
        <div class="brand-text">
          <span class="brand-name">llmgw</span>
          <span class="brand-sub">LLM Gateway</span>
        </div>
      </div>

      <div class="header-actions">
        <button
          type="button"
          class="health-pill"
          :class="healthStatus"
          :disabled="probing"
          title="点击重新探测 /healthz"
          @click="void probeHealth()"
        >
          <span class="health-dot" aria-hidden="true" />
          <span class="health-label">{{ healthLabel }}</span>
          <span class="health-meta">{{ healthMeta }}</span>
        </button>

        <button
          type="button"
          class="header-btn"
          :aria-label="`切换主题，当前为${themeLabel(themeMode)}`"
          :title="`切换主题（当前：${themeLabel(themeMode)}）`"
          @click="cycleThemeMode"
        >
          <AppIcon :name="themeIcon" :size="14" />
          <span class="hide-sm">{{ themeLabel(themeMode) }}</span>
        </button>

        <button type="button" class="header-btn login-trigger" @click="openModal()">登录</button>
      </div>
    </header>

    <!-- Layer 3: 中上部 Hero 与底部交互栈 -->
    <main class="landing-stage" :class="{ faded: modalOpen }" :inert="modalOpen">
      <section class="hero-anchor" aria-labelledby="landing-title">
        <h1 id="landing-title" class="hero-title">统一的大模型路由与观测网关</h1>
        <div class="feature-pills">
          <span v-for="pill in FEATURE_PILLS" :key="pill" class="feature-pill">
            {{ pill }}
          </span>
        </div>
      </section>

      <section class="bottom-stack" aria-label="控制台入口与核心能力">
        <div class="route-ticker" aria-live="polite">
          <span class="ticker-method">{{ currentRoute.method }}</span>
          <span class="ticker-path">{{ currentRoute.path }}</span>
          <span class="ticker-arrow" aria-hidden="true">→</span>
          <span class="ticker-target">{{ currentRoute.target }}</span>
        </div>

        <div class="cta-block">
          <button type="button" class="cta-btn" @click="openModal()">
            <span>进入控制台</span>
            <AppIcon name="arrow-right" :size="15" />
          </button>
          <p class="shortcut-hint">按 <kbd>Enter</kbd> 呼出登录 · <kbd>ESC</kbd> 收起</p>
        </div>

        <div class="capability-grid">
          <button
            v-for="card in CAPABILITY_CARDS"
            :key="card.id"
            type="button"
            class="capability-card"
            @click="openModal(card.id)"
          >
            <span class="capability-icon" aria-hidden="true">
              <AppIcon :name="card.icon" :size="15" />
            </span>
            <span class="capability-body">
              <span class="capability-title">{{ card.title }}</span>
              <span class="capability-desc">{{ card.desc }}</span>
            </span>
          </button>
        </div>
      </section>
    </main>

    <!-- Layer 4: 按需毛玻璃登录弹窗 -->
    <Transition name="modal-fade">
      <div v-if="modalOpen" class="auth-backdrop" role="presentation" @click.self="closeModal">
        <div
          class="auth-card"
          :class="{ shaking, success: loginSuccess }"
          role="dialog"
          aria-modal="true"
          aria-labelledby="login-modal-title"
        >
          <div class="auth-card-head">
            <div class="brand-mark">
              <span class="brand-icon">
                <AppIcon name="spark" :size="16" />
              </span>
              <div class="brand-text">
                <span id="login-modal-title" class="brand-name">llmgw 控制台</span>
                <span class="brand-sub">管理员身份鉴权</span>
              </div>
            </div>
            <button
              type="button"
              class="icon-btn"
              aria-label="关闭登录弹窗"
              :disabled="busy || loginSuccess"
              @click="closeModal"
            >
              <AppIcon name="close" :size="16" />
            </button>
          </div>

          <p class="secondary small">输入管理员凭据以管理上游接入点、路由模型与访问密钥。</p>

          <form class="auth-form" @submit.prevent="void submit()">
            <div class="field">
              <label for="login-username">用户名</label>
              <input
                id="login-username"
                ref="usernameInput"
                v-model="username"
                placeholder="admin"
                autocomplete="username"
                :disabled="busy || loginSuccess"
              />
            </div>

            <div class="field">
              <label for="login-password">密码</label>
              <div class="input-wrap">
                <input
                  id="login-password"
                  ref="passwordInput"
                  v-model="password"
                  :type="showPassword ? 'text' : 'password'"
                  placeholder="输入管理员密码"
                  autocomplete="current-password"
                  :disabled="busy || loginSuccess"
                />
                <span class="input-suffix">
                  <button
                    type="button"
                    class="icon-btn"
                    :aria-label="showPassword ? '隐藏密码' : '显示密码'"
                    @click="showPassword = !showPassword"
                  >
                    <AppIcon :name="showPassword ? 'eye-off' : 'eye'" :size="15" />
                  </button>
                </span>
              </div>
            </div>

            <p v-if="error" class="auth-error" role="alert">{{ error }}</p>
            <p v-else-if="loginSuccess" class="auth-ok" role="status">鉴权通过，正在进入控制台…</p>

            <button class="primary auth-submit" type="submit" :disabled="busy || loginSuccess">
              {{ loginSuccess ? '已通过 · 跳转中…' : busy ? '登录中…' : '登录控制台' }}
            </button>
          </form>
        </div>
      </div>
    </Transition>
  </div>
</template>

<style scoped>
.landing-root {
  position: relative;
  width: 100%;
  min-height: 100vh;
  overflow: hidden;
  background: var(--bg-canvas);
  color: var(--text-primary);
  display: flex;
  flex-direction: column;
}

/* Layer 1: Canvas */
.landing-canvas {
  position: fixed;
  inset: 0;
  width: 100%;
  height: 100%;
  z-index: 1;
  transition:
    filter 0.3s ease,
    opacity 0.3s ease;
}

.landing-canvas.blurred {
  filter: blur(6px);
  opacity: 0.72;
}

/* Layer 2: Top Bar */
.landing-header {
  position: relative;
  z-index: 20;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 18px 28px;
  pointer-events: none;
}

.landing-header > * {
  pointer-events: auto;
}

.header-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.health-pill {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  padding: 6px 12px;
  border-radius: 999px;
  border: 1px solid var(--border-subtle);
  background: color-mix(in srgb, var(--bg-canvas) 86%, transparent);
  backdrop-filter: blur(12px);
  color: var(--text-secondary);
  font-family: var(--font-mono);
  font-size: 11.5px;
  line-height: 1;
  transition:
    border-color 0.2s ease,
    background-color 0.2s ease;
}

.health-pill:hover {
  border-color: var(--border-strong);
  background: var(--bg-surface);
}

.health-dot {
  width: 7px;
  height: 7px;
  border-radius: 999px;
  background: var(--warn);
  flex-shrink: 0;
}

.health-pill.online .health-dot {
  background: var(--ok);
  box-shadow: 0 0 0 3px var(--ok-soft);
}

.health-pill.offline .health-dot {
  background: var(--danger);
  box-shadow: 0 0 0 3px var(--danger-soft);
}

.health-meta {
  color: var(--text-muted);
  padding-left: 6px;
  border-left: 1px solid var(--border-subtle);
}

.health-pill.online .health-meta {
  color: var(--ok);
}

.health-pill.offline .health-meta {
  color: var(--danger);
}

.header-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 6px 12px;
  border-radius: 999px;
  border: 1px solid var(--border-subtle);
  background: color-mix(in srgb, var(--bg-canvas) 86%, transparent);
  backdrop-filter: blur(12px);
  color: var(--text-secondary);
  font-size: 12.5px;
  font-weight: 500;
  transition:
    border-color 0.2s ease,
    color 0.2s ease,
    background-color 0.2s ease;
}

.header-btn:hover {
  border-color: var(--border-strong);
  color: var(--text-primary);
  background: var(--bg-surface);
}

.header-btn.login-trigger {
  color: var(--text-primary);
  border-color: var(--border-strong);
}

.header-btn.login-trigger:hover {
  border-color: var(--accent);
  color: var(--accent);
}

/* Layer 3: Hero & Bottom Stack */
.landing-stage {
  position: relative;
  z-index: 10;
  flex: 1;
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  align-items: center;
  padding: 12px 24px 28px;
  pointer-events: none;
  transition:
    opacity 0.24s ease,
    transform 0.24s ease;
}

.landing-stage.faded {
  opacity: 0;
  transform: scale(0.985);
}

.hero-anchor {
  display: flex;
  flex-direction: column;
  align-items: center;
  text-align: center;
  gap: 12px;
  max-width: 680px;
  margin-top: 4px;
}

.hero-title {
  font-size: clamp(24px, 3vw, 34px);
  font-weight: 700;
  letter-spacing: -0.025em;
  line-height: 1.25;
  color: var(--text-primary);
}

.feature-pills {
  display: flex;
  flex-wrap: wrap;
  justify-content: center;
  gap: 8px;
}

.feature-pill {
  padding: 4px 11px;
  border-radius: 999px;
  border: 1px solid var(--border-subtle);
  background: color-mix(in srgb, var(--bg-subtle) 86%, transparent);
  backdrop-filter: blur(8px);
  color: var(--text-secondary);
  font-size: 12px;
  font-weight: 500;
}

.bottom-stack {
  width: 100%;
  max-width: 960px;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 14px;
}

.route-ticker {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 6px 14px;
  border-radius: 999px;
  border: 1px solid var(--border-subtle);
  background: color-mix(in srgb, var(--bg-canvas) 86%, transparent);
  backdrop-filter: blur(12px);
  font-family: var(--font-mono);
  font-size: 11.5px;
  color: var(--text-secondary);
  max-width: 100%;
  overflow: hidden;
  white-space: nowrap;
}

.ticker-method {
  color: var(--accent);
  font-weight: 600;
}

.ticker-path {
  color: var(--text-primary);
}

.ticker-arrow {
  color: var(--text-muted);
}

.ticker-target {
  color: var(--text-secondary);
}

.cta-block {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  pointer-events: auto;
}

.cta-btn {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 11px 24px;
  border-radius: 999px;
  border: 1px solid var(--text-primary);
  background: var(--text-primary);
  color: var(--bg-canvas);
  font-size: 14px;
  font-weight: 600;
  box-shadow: var(--shadow-sm);
  transition:
    transform 0.18s ease,
    background-color 0.18s ease,
    border-color 0.18s ease,
    box-shadow 0.18s ease;
}

.cta-btn:hover {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--accent-text);
  transform: translateY(-1px);
  box-shadow: var(--shadow-lg);
}

.shortcut-hint {
  font-size: 11.5px;
  color: var(--text-muted);
}

.shortcut-hint kbd {
  display: inline-block;
  padding: 1px 6px;
  margin: 0 2px;
  border-radius: 4px;
  border: 1px solid var(--border-subtle);
  background: var(--bg-subtle);
  font-family: var(--font-mono);
  font-size: 10.5px;
  color: var(--text-secondary);
}

.capability-grid {
  width: 100%;
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 10px;
  pointer-events: auto;
}

.capability-card {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 12px 14px;
  text-align: left;
  border-radius: var(--radius-lg);
  border: 1px solid var(--border-subtle);
  background: color-mix(in srgb, var(--bg-canvas) 86%, transparent);
  backdrop-filter: blur(14px);
  color: var(--text-primary);
  transition:
    border-color 0.2s ease,
    transform 0.2s ease,
    background-color 0.2s ease;
}

.capability-card:hover {
  border-color: var(--accent-border);
  background: color-mix(in srgb, var(--bg-surface) 94%, transparent);
  transform: translateY(-2px);
}

.capability-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border-radius: var(--radius-md);
  background: var(--accent-soft);
  color: var(--accent);
  flex-shrink: 0;
}

.capability-body {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.capability-title {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--text-primary);
}

.capability-desc {
  font-size: 11px;
  line-height: 1.45;
  color: var(--text-muted);
}

/* Layer 4: On-demand Glassmorphism Login Modal */
.auth-backdrop {
  position: fixed;
  inset: 0;
  z-index: 40;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
  background: color-mix(in srgb, var(--bg-canvas) 32%, transparent);
  backdrop-filter: blur(6px);
}

.auth-card {
  width: 100%;
  max-width: 400px;
  max-height: calc(100vh - 40px);
  max-height: calc(100dvh - 40px);
  overflow-y: auto;
  padding: 26px;
  border-radius: var(--radius-xl);
  border: 1px solid var(--border-subtle);
  background: color-mix(in srgb, var(--bg-canvas) 88%, transparent);
  backdrop-filter: blur(16px);
  box-shadow: var(--shadow-lg);
  display: flex;
  flex-direction: column;
  gap: 16px;
  transition:
    border-color 0.25s ease,
    box-shadow 0.25s ease;
}

.auth-card.success {
  border-color: var(--ok);
  box-shadow:
    0 0 0 3px var(--ok-soft),
    var(--shadow-lg);
}

.auth-card.shaking {
  border-color: var(--danger);
  animation: auth-shake 380ms cubic-bezier(0.36, 0.07, 0.19, 0.97) both;
}

.auth-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.auth-form {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.auth-error {
  font-size: 12.5px;
  color: var(--danger);
  background: var(--danger-soft);
  border: 1px solid var(--danger-border);
  border-radius: var(--radius-md);
  padding: 8px 10px;
}

.auth-ok {
  font-size: 12.5px;
  color: var(--ok);
  background: var(--ok-soft);
  border: 1px solid var(--ok);
  border-radius: var(--radius-md);
  padding: 8px 10px;
}

.auth-submit {
  width: 100%;
  justify-content: center;
  padding: 10px 16px;
}

@keyframes auth-shake {
  0%,
  100% {
    transform: translateX(0);
  }
  20% {
    transform: translateX(-7px);
  }
  40% {
    transform: translateX(6px);
  }
  60% {
    transform: translateX(-4px);
  }
  80% {
    transform: translateX(3px);
  }
}

.modal-fade-enter-active,
.modal-fade-leave-active {
  transition: opacity 0.2s ease;
}

.modal-fade-enter-active .auth-card,
.modal-fade-leave-active .auth-card {
  transition:
    transform 0.22s ease,
    opacity 0.22s ease;
}

.modal-fade-enter-from,
.modal-fade-leave-to {
  opacity: 0;
}

.modal-fade-enter-from .auth-card,
.modal-fade-leave-to .auth-card {
  opacity: 0;
  transform: translateY(8px) scale(0.98);
}

@media (max-width: 900px) {
  .capability-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 640px) {
  .landing-root {
    overflow-y: auto;
  }

  .landing-header {
    padding: 14px 16px;
  }

  .health-label {
    display: none;
  }

  .health-meta {
    border-left: none;
    padding-left: 0;
  }

  .hide-sm {
    display: none;
  }

  .landing-stage {
    padding: 8px 16px 20px;
    gap: 24px;
  }

  .route-ticker {
    font-size: 10.5px;
    padding: 5px 10px;
  }

  .capability-grid {
    grid-template-columns: 1fr;
  }
}
</style>
