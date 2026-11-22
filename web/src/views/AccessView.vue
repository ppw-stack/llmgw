<script setup lang="ts">
import { computed, ref } from 'vue';

const origin = location.origin;
const openaiBase = computed(() => origin + '/openai/v1');
const anthropicBase = computed(() => origin + '/anthropic');

const copied = ref('');
async function copy(text: string, label: string) {
  try {
    await navigator.clipboard.writeText(text);
    copied.value = label;
    setTimeout(() => (copied.value = ''), 1500);
  } catch {
    /* 剪贴板不可用 */
  }
}

const clients = [
  { name: 'OpenAI SDK / Codex CLI', base: 'openai' },
  { name: 'Cursor / Cline（OpenAI Compatible）', base: 'openai' },
  { name: 'Anthropic SDK', base: 'anthropic' },
  { name: 'Claude Code（ANTHROPIC_BASE_URL）', base: 'anthropic' },
  { name: 'Cline（Anthropic 模式）', base: 'anthropic' },
];
</script>

<template>
  <section>
    <div class="section-head"><h2>接入说明</h2></div>

    <div class="grid-2">
      <div class="card">
        <h3>OpenAI 协议</h3>
        <div class="row">
          <code class="secret-value">{{ openaiBase }}</code>
          <button class="ghost" @click="copy(openaiBase, 'openai')">
            {{ copied === 'openai' ? '已复制' : '复制' }}
          </button>
        </div>
        <p class="muted">
          base_url 必须带 <code>/v1</code>，SDK 会在其后拼接 <code>/chat/completions</code>。
        </p>
      </div>

      <div class="card">
        <h3>Anthropic 协议</h3>
        <div class="row">
          <code class="secret-value">{{ anthropicBase }}</code>
          <button class="ghost" @click="copy(anthropicBase, 'anthropic')">
            {{ copied === 'anthropic' ? '已复制' : '复制' }}
          </button>
        </div>
        <p class="muted">base_url 不带 <code>/v1</code>，SDK 会拼接 <code>/v1/messages</code>。</p>
      </div>
    </div>

    <div class="card">
      <h3>客户端对应关系</h3>
      <table>
        <thead>
          <tr>
            <th>客户端</th>
            <th>使用入口</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="c in clients" :key="c.name">
            <td>{{ c.name }}</td>
            <td>
              <code>{{ c.base === 'openai' ? openaiBase : anthropicBase }}</code>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="card">
      <h3>鉴权</h3>
      <p class="muted">
        两种协议都使用网关 Key（<code>sk-gw-</code> 开头）：OpenAI 客户端发送
        <code>Authorization: Bearer sk-gw-…</code>，Anthropic 客户端发送
        <code>x-api-key: sk-gw-…</code>。上游 Key 不会离开网关。
      </p>
    </div>

    <div class="card">
      <h3>协议范围</h3>
      <p class="muted">
        v1 只做同协议直通，不进行 OpenAI ↔ Anthropic 转换。模型只在另一个协议存在时，请求会返回
        <code>400</code> 并提示正确的入口。
      </p>
    </div>
  </section>
</template>
