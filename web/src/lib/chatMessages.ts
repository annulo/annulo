import type { UIMessage } from 'ai'

/** 历史中的失败/中止回复可能存成 parts:null；AI SDK 和界面都要求 parts 是数组。 */
export function normalizeChatMessages<T extends UIMessage>(input: unknown): T[] {
  if (!Array.isArray(input)) return []
  return input.flatMap((value) => {
    if (!value || typeof value !== 'object' || typeof value.id !== 'string' || !['system', 'user', 'assistant'].includes(value.role)) return []
    const parts = Array.isArray(value.parts)
      ? value.parts.filter((part: unknown) => part && typeof part === 'object' && 'type' in part && typeof part.type === 'string')
      : typeof value.content === 'string' && value.content ? [{ type: 'text', text: value.content }] : []
    return [{ ...value, parts } as T]
  })
}
