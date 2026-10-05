// 产品从 Shuttle 改名 Annulo（docs/annulo-plan.md 第 1 步）：外壳和运营后台页面之间的消息新旧名字都认。
// 页面发来的 annulo:xxx 当作 shuttle:xxx；发给页面的两个名字都发（已有的页面只听 shuttle:xxx）。Go 那边是 internal/brand。

/** 页面发来的消息类型，annulo:xxx 换成 shuttle:xxx */
export function msgType(data: unknown): string | undefined {
  const t = (data as { type?: unknown } | null)?.type
  if (typeof t !== 'string') return undefined
  return t.startsWith('annulo:') ? 'shuttle:' + t.slice('annulo:'.length) : t
}

/** 发给运营后台页面（iframe）：type 写 shuttle:xxx，同时用 annulo:xxx 再发一份 */
export function postToPage(w: Window | null | undefined, msg: { type: string } & Record<string, unknown>) {
  if (!w) return
  w.postMessage(msg, location.origin)
  if (msg.type.startsWith('shuttle:')) w.postMessage({ ...msg, type: 'annulo:' + msg.type.slice('shuttle:'.length) }, location.origin)
}
