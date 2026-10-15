import { describe, expect, it } from 'vitest'

import { formatCompact, formatNumber, formatRelative, hostOf } from './format'

describe('format', () => {
  it('groups digits', () => {
    expect(formatNumber(1350)).toBe('1,350')
  })

  it('only abbreviates numbers too long to read at a glance', () => {
    expect(formatCompact(5000)).toBe('5,000')
    expect(formatCompact(100_000)).toBe('100K')
  })

  it('words timestamps relative to now', () => {
    const now = new Date('2026-10-15T12:00:00Z')

    expect(formatRelative('2026-10-12T12:00:00Z', now)).toBe('3 days ago')
    expect(formatRelative('2026-10-15T11:59:30Z', now)).toBe('just now')
  })

  it('names the host of a URL', () => {
    expect(hostOf('https://gitlab.example.com/')).toBe('gitlab.example.com')
  })
})
