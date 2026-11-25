<script setup lang="ts">
import AppIcon from './AppIcon.vue';
import { toasts } from '../utils/toast';
</script>

<template>
  <div v-if="toasts.length" class="toast-host" aria-live="polite" aria-atomic="true">
    <div v-for="item in toasts" :key="item.id" class="toast-item" :class="item.kind">
      <AppIcon :name="item.kind === 'error' ? 'close' : 'check'" :size="14" />
      <span>{{ item.message }}</span>
    </div>
  </div>
</template>
