/**
 * 把未知异常转成可以直接展示给用户的文案。
 *
 * 在 strict 模式下 catch 到的变量是 unknown，必须显式处理。
 */
export function errorMessage(error: unknown): string {
  if (error instanceof Error) return error.message;
  if (typeof error === 'string') return error;
  return '未知错误';
}
