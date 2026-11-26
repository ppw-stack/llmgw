<script setup lang="ts">
import { computed, ref } from 'vue';
import CopyButton from '../components/CopyButton.vue';

type SnippetTab = 'claude-code' | 'openai-curl' | 'anthropic-curl';

const origin = location.origin;
const openaiBase = computed(() => `${origin}/openai/v1`);
const anthropicBase = computed(() => `${origin}/anthropic`);

const activeSnippet = ref<SnippetTab>('claude-code');

const snippetCode = computed(() => {
  if (activeSnippet.value === 'claude-code') {
    return [
      `export ANTHROPIC_BASE_URL="${anthropicBase.value}"`,
      `export ANTHROPIC_AUTH_TOKEN="<YOUR_GATEWAY_KEY>"`,
      `claude`,
    ].join('\n');
  }
  if (activeSnippet.value === 'openai-curl') {
    return [
      `curl ${openaiBase.value}/chat/completions \\`,
      `  -H "Authorization: Bearer <YOUR_GATEWAY_KEY>" \\`,
      `  -H "Content-Type: application/json" \\`,
      `  -d '{"model":"your-model","messages":[{"role":"user","content":"Hello"}]}'`,
    ].join('\n');
  }
  return [
    `curl ${anthropicBase.value}/v1/messages \\`,
    `  -H "x-api-key: <YOUR_GATEWAY_KEY>" \\`,
    `  -H "anthropic-version: 2023-06-01" \\`,
    `  -H "Content-Type: application/json" \\`,
    `  -d '{"model":"your-model","max_tokens":256,"messages":[{"role":"user","content":"Hello"}]}'`,
  ].join('\n');
});

const clients = [
  { name: 'OpenAI SDK / Codex CLI', protocol: 'openai' as const },
  { name: 'Cursor / Cline（OpenAI Compatible）', protocol: 'openai' as const },
  { name: 'Anthropic SDK', protocol: 'anthropic' as const },
  { name: 'Claude Code（ANTHROPIC_BASE_URL）', protocol: 'anthropic' as const },
  { name: 'Cline（Anthropic 模式）', protocol: 'anthropic' as const },
];
</script>

<template>
  <section class="page">
    <div class="page-head">
      <div class="page-title-group">
        <h2>接入说明</h2>
        <p class="page-desc">
          将下方网关入口地址与在「网关 Key」页创建的令牌填入客户端即可开始调用。
        </p>
      </div>
    </div>

    <div class="grid-2">
      <div class="card stack">
        <div class="row space-between">
          <h3>OpenAI 协议入口</h3>
          <span class="badge openai">openai</span>
        </div>
        <div class="row">
          <code class="secret-code">{{ openaiBase }}</code>
          <CopyButton :text="openaiBase" toast-message="OpenAI 入口地址已复制" />
        </div>
        <p class="secondary small">
          <code>base_url</code> 必须包含 <code>/v1</code>，客户端 SDK 会在其后拼接
          <code>/chat/completions</code>、<code>/responses</code>、<code>/images/generations</code>
          或 <code>/embeddings</code>。
        </p>
      </div>

      <div class="card stack">
        <div class="row space-between">
          <h3>Anthropic 协议入口</h3>
          <span class="badge anthropic">anthropic</span>
        </div>
        <div class="row">
          <code class="secret-code">{{ anthropicBase }}</code>
          <CopyButton :text="anthropicBase" toast-message="Anthropic 入口地址已复制" />
        </div>
        <p class="secondary small">
          <code>base_url</code> 不带 <code>/v1</code>，客户端 SDK 会自动拼接
          <code>/v1/messages</code>。
        </p>
      </div>
    </div>

    <div class="card stack">
      <div class="row space-between">
        <div>
          <h3>快速接入示例</h3>
          <p class="muted small">
            将示例中的 <code>&lt;YOUR_GATEWAY_KEY&gt;</code> 替换为你的网关 Key
          </p>
        </div>
        <div class="segmented" role="group" aria-label="接入示例类型">
          <button
            type="button"
            :class="{ active: activeSnippet === 'claude-code' }"
            @click="activeSnippet = 'claude-code'"
          >
            Claude Code
          </button>
          <button
            type="button"
            :class="{ active: activeSnippet === 'openai-curl' }"
            @click="activeSnippet = 'openai-curl'"
          >
            OpenAI cURL
          </button>
          <button
            type="button"
            :class="{ active: activeSnippet === 'anthropic-curl' }"
            @click="activeSnippet = 'anthropic-curl'"
          >
            Anthropic cURL
          </button>
        </div>
      </div>

      <div class="code-block">
        <div class="code-block-head">
          <span class="muted small mono">shell</span>
          <CopyButton :text="snippetCode" compact toast-message="示例代码已复制" />
        </div>
        <pre><code>{{ snippetCode }}</code></pre>
      </div>
    </div>

    <div class="card table-card">
      <div class="card-header" style="padding: 16px 18px 0">
        <h3>常见客户端对应关系</h3>
      </div>
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>客户端</th>
              <th>协议</th>
              <th>使用入口</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="c in clients" :key="c.name" class="hoverable">
              <td>{{ c.name }}</td>
              <td>
                <span class="badge" :class="c.protocol">{{ c.protocol }}</span>
              </td>
              <td>
                <div class="row nowrap">
                  <code>{{ c.protocol === 'openai' ? openaiBase : anthropicBase }}</code>
                  <CopyButton
                    :text="c.protocol === 'openai' ? openaiBase : anthropicBase"
                    icon-only
                  />
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <div class="grid-2">
      <div class="card stack">
        <h3>鉴权方式</h3>
        <p class="secondary small">
          两种协议统一使用网关 Key（<code>sk-gw-</code> 开头）：OpenAI 客户端发送
          <code>Authorization: Bearer sk-gw-…</code>，Anthropic 客户端发送
          <code>x-api-key: sk-gw-…</code>。上游 Key 始终保存在网关内。
        </p>
      </div>

      <div class="card stack">
        <h3>协议范围</h3>
        <p class="secondary small">
          v1 仅做同协议直通，不进行 OpenAI ↔ Anthropic
          协议转换。若模型仅在另一协议下存在，网关会返回
          <code>400</code> 并提示正确的入口。
        </p>
      </div>
    </div>
  </section>
</template>
