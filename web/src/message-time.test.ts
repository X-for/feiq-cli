import { describe, expect, it } from 'vitest'

import { formatMessageTime } from './message-time'

describe('formatMessageTime', () => {
  it('includes the full local date and time', () => {
    expect(formatMessageTime('2026-08-13T14:05:00')).toBe('2026-08-13 14:05')
  })

  it('returns an empty string for an invalid timestamp', () => {
    expect(formatMessageTime('invalid')).toBe('')
  })
})
