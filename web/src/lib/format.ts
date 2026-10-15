const integer = new Intl.NumberFormat('en-US')
const compact = new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 })
const relative = new Intl.RelativeTimeFormat('en', { numeric: 'auto' })
const absolute = new Intl.DateTimeFormat('en', { dateStyle: 'medium', timeStyle: 'short' })

/** 1350 → "1,350". */
export function formatNumber(value: number): string {
  return integer.format(value)
}

/** 125000 → "125K", for thresholds where exact digits are noise. */
export function formatCompact(value: number): string {
  return value < 10_000 ? integer.format(value) : compact.format(value)
}

const units: [Intl.RelativeTimeFormatUnit, number][] = [
  ['year', 365 * 24 * 3600],
  ['month', 30 * 24 * 3600],
  ['week', 7 * 24 * 3600],
  ['day', 24 * 3600],
  ['hour', 3600],
  ['minute', 60],
]

/** A timestamp as GitLab words one: "3 days ago", "last week". */
export function formatRelative(iso: string, now: Date = new Date()): string {
  const seconds = (new Date(iso).getTime() - now.getTime()) / 1000
  for (const [unit, size] of units) {
    if (Math.abs(seconds) >= size) return relative.format(Math.round(seconds / size), unit)
  }

  return 'just now'
}

/** The full timestamp, for the title of a relative one. */
export function formatAbsolute(iso: string): string {
  return absolute.format(new Date(iso))
}

/** The host a URL points at, for prose: "gitlab.example.com". */
export function hostOf(url: string): string {
  try {
    return new URL(url).host
  } catch {
    return url
  }
}
