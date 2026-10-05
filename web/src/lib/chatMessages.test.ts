import assert from 'node:assert/strict'
import test from 'node:test'
import { normalizeChatMessages } from './chatMessages.ts'

test('aborted empty reply can be restored without parts.some crashing', () => {
  const input = [{ id: 'a1', role: 'assistant', parts: null, metadata: { aborted: true, error: 'stopped' } }]
  const [message] = normalizeChatMessages(input)
  assert.equal(message.parts.some((part) => part.type === 'text'), false)
  assert.deepEqual(message.parts, [])
  assert.deepEqual(message.metadata, input[0].metadata)
  assert.equal(input[0].parts, null) // 不改写用户原历史
})

test('valid messages and tool state survive while malformed parts are ignored', () => {
  const tool = { type: 'dynamic-tool', toolName: 'db_query', toolCallId: 't1', state: 'output-available', output: { total: 1 } }
  const [message] = normalizeChatMessages([{ id: 'a2', role: 'assistant', parts: [null, tool, { type: 'text', text: 'done' }] }])
  assert.deepEqual(message.parts, [tool, { type: 'text', text: 'done' }])
  assert.deepEqual(normalizeChatMessages(null), [])
  assert.deepEqual(normalizeChatMessages([null, { id: 1, role: 'user' }]), [])
})
