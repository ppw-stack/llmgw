/**
 * LLMGW Warm-Parchment High-Concurrency Token Routing Canvas Engine.
 * 零第三方依赖，支持高 DPI、亮/暗主题动态色板、prefers-reduced-motion 降级与卸载清理。
 */

export interface GatewayCanvasPalette {
  bg: string;
  gridDot: string;
  cardBg: string;
  cardBgActive: string;
  borderSubtle: string;
  inkPrimary: string;
  inkSecondary: string;
  inkMuted: string;
  accent: string;
  accentRgb: string;
  mutedRgb: string;
  inLineIdle: string;
  hubStar: string;
  fontSans: string;
  fontMono: string;
}

export interface GatewayCanvasOptions {
  spawnChance?: number;
  maxParticles?: number;
  speedMin?: number;
  speedMax?: number;
  isModalOpen?: () => boolean;
  getPalette?: () => GatewayCanvasPalette;
}

export interface GatewayCanvasController {
  triggerPulse: (burstCount?: number, color?: string) => void;
  refreshTheme: () => void;
  destroy: () => void;
}

interface Point {
  x: number;
  y: number;
}

interface TopologyNode {
  yRatio: number;
  label: string;
  sub: string;
  glow: number;
}

interface Particle {
  stage: 'in' | 'out';
  t: number;
  speed: number;
  targetIdx: number;
  startYRatio: number;
  targetYRatio: number;
  outColor: string | null;
  size: number;
  trail: Point[];
}

interface Ripple {
  r: number;
  maxR: number;
  alpha: number;
  color: string;
}

const DEFAULT_PALETTE: GatewayCanvasPalette = {
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

export function initGatewayCanvas(
  canvas: HTMLCanvasElement,
  options: GatewayCanvasOptions = {},
): GatewayCanvasController | null {
  const rawCtx = canvas.getContext('2d');
  if (!rawCtx) {
    return null;
  }
  const ctx: CanvasRenderingContext2D = rawCtx;

  const spawnChance = options.spawnChance ?? 0.28;
  const maxParticles = options.maxParticles ?? 115;
  const speedMin = options.speedMin ?? 0.015;
  const speedMax = options.speedMax ?? 0.024;
  const isModalOpen = options.isModalOpen ?? (() => false);
  const getPalette = options.getPalette ?? (() => DEFAULT_PALETTE);

  let width = window.innerWidth;
  let height = window.innerHeight;
  const particles: Particle[] = [];
  const ripples: Ripple[] = [];
  const pulseTimers = new Set<number>();

  let isHubHovered = false;
  let hubScale = 1;
  let starRotation = 0;
  let rafId: number | null = null;
  let destroyed = false;

  const reducedMotionQuery = window.matchMedia('(prefers-reduced-motion: reduce)');
  let prefersReducedMotion = reducedMotionQuery.matches;

  const leftNodes: TopologyNode[] = [
    { yRatio: 0.34, label: 'Chat Client', sub: 'POST /openai/v1/chat/completions', glow: 0 },
    { yRatio: 0.44, label: 'Agent / SDK', sub: 'OpenAI / Anthropic SDK', glow: 0 },
    { yRatio: 0.54, label: 'CLI & IDE', sub: 'Stream SSE Tokens', glow: 0 },
    { yRatio: 0.64, label: 'Internal API', sub: 'Bearer Gateway Key', glow: 0 },
  ];

  const rightNodes: TopologyNode[] = [
    { yRatio: 0.34, label: 'OpenAI Upstream', sub: 'Chat & Reasoning', glow: 0 },
    { yRatio: 0.44, label: 'Anthropic Upstream', sub: 'Messages Protocol', glow: 0 },
    { yRatio: 0.54, label: 'DeepSeek / Flash', sub: 'High-Throughput Pool', glow: 0 },
    { yRatio: 0.64, label: 'Custom Provider', sub: 'Self-Hosted / vLLM', glow: 0 },
  ];

  function getLeftX(): number {
    if (width < 640) {
      return width * 0.12;
    }
    if (width < 1024) {
      return Math.max(190, width * 0.23);
    }
    return Math.max(255, width * 0.24);
  }

  function getRightX(): number {
    if (width < 640) {
      return width * 0.88;
    }
    if (width < 1024) {
      return Math.min(width - 190, width * 0.77);
    }
    return Math.min(width - 255, width * 0.76);
  }

  function getHubCenter(): Point {
    return { x: width * 0.5, y: height * 0.49 };
  }

  function cubicBezier(t: number, p0: Point, p1: Point, p2: Point, p3: Point): Point {
    const u = 1 - t;
    const tt = t * t;
    const uu = u * u;
    return {
      x: uu * u * p0.x + 3 * uu * t * p1.x + 3 * u * tt * p2.x + tt * t * p3.x,
      y: uu * u * p0.y + 3 * uu * t * p1.y + 3 * u * tt * p2.y + tt * t * p3.y,
    };
  }

  function drawRoundRect(x: number, y: number, w: number, h: number, r: number): void {
    ctx.beginPath();
    ctx.moveTo(x + r, y);
    ctx.lineTo(x + w - r, y);
    ctx.quadraticCurveTo(x + w, y, x + w, y + r);
    ctx.lineTo(x + w, y + h - r);
    ctx.quadraticCurveTo(x + w, y + h, x + w - r, y + h);
    ctx.lineTo(x + r, y + h);
    ctx.quadraticCurveTo(x, y + h, x, y + h - r);
    ctx.lineTo(x, y + r);
    ctx.quadraticCurveTo(x, y, x + r, y);
    ctx.closePath();
  }

  function drawWarmGrid(palette: GatewayCanvasPalette): void {
    const spacing = 32;
    ctx.fillStyle = palette.gridDot;
    for (let x = spacing; x < width; x += spacing) {
      for (let y = spacing; y < height; y += spacing) {
        ctx.beginPath();
        ctx.arc(x, y, 0.9, 0, Math.PI * 2);
        ctx.fill();
      }
    }
  }

  function drawTopology(palette: GatewayCanvasPalette): void {
    const lx = getLeftX();
    const rx = getRightX();
    const hub = getHubCenter();
    const showCards = width >= 768;
    const cardW = width >= 1120 ? 204 : 168;
    const cardH = 44;

    for (const node of leftNodes) {
      const ny = height * node.yRatio;
      const cp = (hub.x - lx) * 0.52;
      node.glow = Math.max(0, node.glow - 0.035);

      ctx.lineWidth = 1.1;
      ctx.strokeStyle =
        node.glow > 0.1
          ? `rgba(${palette.mutedRgb}, ${0.45 + node.glow * 0.35})`
          : palette.inLineIdle;
      ctx.beginPath();
      ctx.moveTo(lx, ny);
      ctx.bezierCurveTo(lx + cp, ny, hub.x - cp, hub.y, hub.x, hub.y);
      ctx.stroke();

      if (showCards) {
        drawRoundRect(lx - cardW - 10, ny - cardH / 2, cardW, cardH, 10);
        ctx.fillStyle = palette.cardBg;
        ctx.fill();
        ctx.lineWidth = 1;
        ctx.strokeStyle =
          node.glow > 0.1
            ? `rgba(${palette.mutedRgb}, ${0.45 + node.glow * 0.4})`
            : palette.borderSubtle;
        ctx.stroke();

        ctx.textAlign = 'right';
        ctx.font = `600 11.5px ${palette.fontSans}`;
        ctx.fillStyle = palette.inkPrimary;
        ctx.fillText(node.label, lx - 22, ny - 3);
        ctx.font = `400 9.5px ${palette.fontMono}`;
        ctx.fillStyle = palette.inkMuted;
        ctx.fillText(node.sub, lx - 22, ny + 11);
      }

      ctx.beginPath();
      ctx.arc(lx, ny, 3.2 + node.glow * 0.8, 0, Math.PI * 2);
      ctx.fillStyle = node.glow > 0.2 ? palette.inkSecondary : palette.inkMuted;
      ctx.fill();
    }

    for (const node of rightNodes) {
      const ny = height * node.yRatio;
      const cp = (rx - hub.x) * 0.52;
      node.glow = Math.max(0, node.glow - 0.035);

      ctx.lineWidth = 1.1;
      ctx.strokeStyle =
        node.glow > 0.1
          ? `rgba(${palette.accentRgb}, ${0.24 + node.glow * 0.42})`
          : `rgba(${palette.accentRgb}, 0.22)`;
      ctx.beginPath();
      ctx.moveTo(hub.x, hub.y);
      ctx.bezierCurveTo(hub.x + cp, hub.y, rx - cp, ny, rx, ny);
      ctx.stroke();

      if (showCards) {
        drawRoundRect(rx + 10, ny - cardH / 2, cardW, cardH, 10);
        ctx.fillStyle = node.glow > 0.1 ? palette.cardBgActive : palette.cardBg;
        ctx.fill();
        ctx.lineWidth = 1;
        ctx.strokeStyle =
          node.glow > 0.1
            ? `rgba(${palette.accentRgb}, ${0.28 + node.glow * 0.55})`
            : palette.borderSubtle;
        ctx.stroke();

        ctx.textAlign = 'left';
        ctx.font = `600 11.5px ${palette.fontMono}`;
        ctx.fillStyle = palette.inkPrimary;
        ctx.fillText(node.label, rx + 22, ny - 3);
        ctx.font = `400 9.5px ${palette.fontMono}`;
        ctx.fillStyle = node.glow > 0.2 ? palette.accent : palette.inkMuted;
        ctx.fillText(node.sub, rx + 22, ny + 11);
      }

      ctx.beginPath();
      ctx.arc(rx, ny, 3.4 + node.glow * 1.1, 0, Math.PI * 2);
      ctx.fillStyle = palette.accent;
      ctx.fill();
    }
  }

  function drawHub(palette: GatewayCanvasPalette): void {
    const hub = getHubCenter();
    if (!prefersReducedMotion) {
      hubScale += ((isHubHovered ? 1.09 : 1) - hubScale) * 0.14;
      starRotation += 0.004;
    } else {
      hubScale = 1;
    }

    ctx.save();
    ctx.translate(hub.x, hub.y);
    ctx.scale(hubScale, hubScale);

    const halo = ctx.createRadialGradient(0, 0, 12, 0, 0, 58);
    halo.addColorStop(0, `rgba(${palette.accentRgb}, 0.24)`);
    halo.addColorStop(1, `rgba(${palette.accentRgb}, 0)`);
    ctx.fillStyle = halo;
    ctx.beginPath();
    ctx.arc(0, 0, 58, 0, Math.PI * 2);
    ctx.fill();

    ctx.beginPath();
    ctx.arc(0, 0, 32, 0, Math.PI * 2);
    ctx.fillStyle = palette.bg;
    ctx.fill();
    ctx.lineWidth = 1.5;
    ctx.strokeStyle = isHubHovered ? palette.accent : palette.borderSubtle;
    ctx.stroke();

    ctx.beginPath();
    ctx.arc(0, 0, 23, 0, Math.PI * 2);
    ctx.fillStyle = palette.accent;
    ctx.fill();

    ctx.save();
    ctx.rotate(Math.sin(starRotation) * 0.14);
    ctx.fillStyle = palette.hubStar;
    ctx.beginPath();
    for (let i = 0; i < 8; i++) {
      const r = i % 2 === 0 ? 11.5 : 3.2;
      const angle = (i * Math.PI) / 4 - Math.PI / 2;
      const sx = Math.cos(angle) * r;
      const sy = Math.sin(angle) * r;
      if (i === 0) {
        ctx.moveTo(sx, sy);
      } else {
        ctx.lineTo(sx, sy);
      }
    }
    ctx.closePath();
    ctx.fill();
    ctx.restore();
    ctx.restore();
  }

  function spawnParticle(
    isDirectBurst = false,
    customColor: string | null = null,
  ): Particle | null {
    const startIdx = Math.floor(Math.random() * leftNodes.length);
    const targetIdx = Math.floor(Math.random() * rightNodes.length);
    const startNode = leftNodes[startIdx];
    const targetNode = rightNodes[targetIdx];
    if (!startNode || !targetNode) {
      return null;
    }

    const speed = speedMin + Math.random() * (speedMax - speedMin);
    if (!isDirectBurst) {
      startNode.glow = 0.75;
    }

    const particle: Particle = {
      stage: isDirectBurst ? 'out' : 'in',
      t: 0,
      speed: isDirectBurst ? speed * 1.25 : speed,
      targetIdx,
      startYRatio: startNode.yRatio,
      targetYRatio: targetNode.yRatio,
      outColor: customColor,
      size: isDirectBurst ? 3.2 : 2.3 + Math.random() * 0.9,
      trail: [],
    };
    particles.push(particle);
    return particle;
  }

  function triggerPulse(burstCount = 18, color?: string): void {
    if (destroyed || prefersReducedMotion) {
      return;
    }
    const pulseColor = color ?? getPalette().accent;
    ripples.push({ r: 28, maxR: 135, alpha: 0.55, color: pulseColor });
    for (let i = 0; i < burstCount; i++) {
      const timerId = window.setTimeout(() => {
        pulseTimers.delete(timerId);
        if (!destroyed) {
          spawnParticle(true, pulseColor);
        }
      }, i * 18);
      pulseTimers.add(timerId);
    }
  }

  function drawStaticFrame(): void {
    const palette = getPalette();
    ctx.fillStyle = palette.bg;
    ctx.fillRect(0, 0, width, height);
    drawWarmGrid(palette);
    drawTopology(palette);
    drawHub(palette);
  }

  function render(): void {
    if (destroyed) {
      return;
    }
    if (prefersReducedMotion) {
      drawStaticFrame();
      rafId = null;
      return;
    }

    const palette = getPalette();
    ctx.fillStyle = palette.bg;
    ctx.fillRect(0, 0, width, height);
    drawWarmGrid(palette);
    drawTopology(palette);

    const lx = getLeftX();
    const rx = getRightX();
    const hub = getHubCenter();

    for (let i = ripples.length - 1; i >= 0; i--) {
      const rp = ripples[i];
      if (!rp) {
        continue;
      }
      rp.r += 2.2;
      rp.alpha -= 0.012;
      if (rp.alpha <= 0 || rp.r >= rp.maxR) {
        ripples.splice(i, 1);
        continue;
      }
      ctx.beginPath();
      ctx.arc(hub.x, hub.y, rp.r, 0, Math.PI * 2);
      ctx.strokeStyle = rp.color;
      ctx.globalAlpha = Math.max(0, rp.alpha);
      ctx.lineWidth = 1.5;
      ctx.stroke();
      ctx.globalAlpha = 1;
    }

    for (let i = particles.length - 1; i >= 0; i--) {
      const p = particles[i];
      if (!p) {
        continue;
      }
      p.t += p.speed;
      let pos: Point;

      if (p.stage === 'in') {
        const p0: Point = { x: lx, y: height * p.startYRatio };
        const cp = (hub.x - lx) * 0.52;
        pos = cubicBezier(
          Math.min(p.t, 1),
          p0,
          { x: lx + cp, y: p0.y },
          { x: hub.x - cp, y: hub.y },
          hub,
        );
        if (p.t >= 1) {
          p.stage = 'out';
          p.t = 0;
          p.trail = [];
          if (ripples.length < 7 && Math.random() < 0.35) {
            ripples.push({ r: 26, maxR: 92, alpha: 0.34, color: palette.accent });
          }
        }
      } else {
        const p3: Point = { x: rx, y: height * p.targetYRatio };
        const cp = (rx - hub.x) * 0.52;
        pos = cubicBezier(
          Math.min(p.t, 1),
          hub,
          { x: hub.x + cp, y: hub.y },
          { x: rx - cp, y: p3.y },
          p3,
        );
        if (p.t >= 1) {
          const target = rightNodes[p.targetIdx];
          if (target) {
            target.glow = 1.0;
          }
          particles.splice(i, 1);
          continue;
        }
      }

      p.trail.push(pos);
      if (p.trail.length > 7) {
        p.trail.shift();
      }

      const activeColor = p.stage === 'in' ? palette.inkMuted : (p.outColor ?? palette.accent);
      if (p.trail.length > 1) {
        for (let j = 1; j < p.trail.length; j++) {
          const prev = p.trail[j - 1];
          const curr = p.trail[j];
          if (!prev || !curr) {
            continue;
          }
          const ratio = j / p.trail.length;
          ctx.beginPath();
          ctx.moveTo(prev.x, prev.y);
          ctx.lineTo(curr.x, curr.y);
          ctx.strokeStyle = activeColor;
          ctx.globalAlpha = ratio * (p.stage === 'in' ? 0.35 : 0.55);
          ctx.lineWidth = p.size * ratio * 0.9;
          ctx.lineCap = 'round';
          ctx.stroke();
        }
        ctx.globalAlpha = 1;
      }

      ctx.beginPath();
      ctx.arc(pos.x, pos.y, p.size, 0, Math.PI * 2);
      ctx.fillStyle = activeColor;
      ctx.fill();
    }

    drawHub(palette);

    if (particles.length < maxParticles && Math.random() < spawnChance) {
      spawnParticle(false);
      if (Math.random() < 0.4) {
        spawnParticle(false);
      }
    }

    rafId = window.requestAnimationFrame(render);
  }

  function resize(): void {
    const dpr = window.devicePixelRatio || 1;
    width = window.innerWidth;
    height = window.innerHeight;
    canvas.width = Math.floor(width * dpr);
    canvas.height = Math.floor(height * dpr);
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    if (prefersReducedMotion) {
      drawStaticFrame();
    }
  }

  function onMouseMove(event: MouseEvent): void {
    if (isModalOpen()) {
      isHubHovered = false;
      canvas.style.cursor = 'default';
      return;
    }
    const hub = getHubCenter();
    isHubHovered = Math.hypot(event.clientX - hub.x, event.clientY - hub.y) <= 38;
    canvas.style.cursor = isHubHovered ? 'pointer' : 'default';
  }

  function onClick(event: MouseEvent): void {
    if (isModalOpen()) {
      return;
    }
    const hub = getHubCenter();
    if (Math.hypot(event.clientX - hub.x, event.clientY - hub.y) <= 40) {
      triggerPulse(18);
    }
  }

  function onReducedMotionChange(event: MediaQueryListEvent): void {
    prefersReducedMotion = event.matches;
    if (prefersReducedMotion) {
      if (rafId !== null) {
        window.cancelAnimationFrame(rafId);
        rafId = null;
      }
      drawStaticFrame();
    } else if (rafId === null && !destroyed) {
      rafId = window.requestAnimationFrame(render);
    }
  }

  resize();
  window.addEventListener('resize', resize);
  canvas.addEventListener('mousemove', onMouseMove);
  canvas.addEventListener('click', onClick);
  reducedMotionQuery.addEventListener('change', onReducedMotionChange);

  if (prefersReducedMotion) {
    drawStaticFrame();
  } else {
    for (let i = 0; i < 24; i++) {
      const p = spawnParticle(i % 2 === 0);
      if (p) {
        p.t = Math.random() * 0.85;
      }
    }
    rafId = window.requestAnimationFrame(render);
  }

  return {
    triggerPulse,
    refreshTheme() {
      if (!destroyed && prefersReducedMotion) {
        drawStaticFrame();
      }
    },
    destroy() {
      destroyed = true;
      if (rafId !== null) {
        window.cancelAnimationFrame(rafId);
        rafId = null;
      }
      for (const timerId of pulseTimers) {
        window.clearTimeout(timerId);
      }
      pulseTimers.clear();
      window.removeEventListener('resize', resize);
      canvas.removeEventListener('mousemove', onMouseMove);
      canvas.removeEventListener('click', onClick);
      reducedMotionQuery.removeEventListener('change', onReducedMotionChange);
    },
  };
}
