/** 页面记录 ID；使用旧版 WebKit 也支持的 getRandomValues，不依赖 randomUUID。 */
export function randomId() {
  const bytes = crypto.getRandomValues(new Uint8Array(16))
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')
}
