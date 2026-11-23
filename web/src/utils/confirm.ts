/**
 * 二次确认。
 *
 * 规范用 no-alert 禁止 alert / confirm / prompt，这里集中放行 confirm 一处：
 * 删除上游接入点、删除网关 Key 属于不可逆操作，浏览器原生对话框自带焦点管理、
 * 读屏支持，并且「取消」是默认焦点，比自绘弹窗更可靠。
 *
 * 新增调用点请复用本文件，不要在业务代码里直接调用 window.confirm。
 */
export function confirmAction(message: string): boolean {
  // eslint-disable-next-line no-alert -- 集中放行删除类操作的二次确认，理由见上方注释
  return window.confirm(message);
}
