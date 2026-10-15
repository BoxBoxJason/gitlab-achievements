import type {
  Catalog,
  CriteriaLeaderboard,
  Detail,
  Instance,
  Leaderboard,
  Me,
  Participation,
  Period,
} from './types'

/** An answer from the API other than a 2xx, with the message it gave. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

async function request<T>(method: string, path: string, query?: Record<string, string | number | undefined>): Promise<T> {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query ?? {})) {
    if (value !== undefined) params.set(key, String(value))
  }

  const url = params.size > 0 ? `${path}?${params}` : path
  const res = await fetch(url, { method, headers: { Accept: 'application/json' } })

  if (!res.ok) {
    let message = res.statusText
    try {
      const body = (await res.json()) as { error?: string }
      if (body.error) message = body.error
    } catch {
      // Not every failure has a JSON body (a proxy's 502, say).
    }

    throw new ApiError(res.status, message)
  }

  return (await res.json()) as T
}

/** The page size every ranking on this frontend uses. */
export const PAGE_SIZE = 20

export const api = {
  me: () => request<Me>('GET', '/api/v1/me'),
  instance: () => request<Instance>('GET', '/api/v1/instance'),
  user: (ref: string) => request<Detail>('GET', `/api/v1/users/${encodeURIComponent(ref)}`),
  catalog: () => request<Catalog>('GET', '/api/v1/achievements'),
  leaderboard: (period: Period, offset: number, since?: string) =>
    request<Leaderboard>('GET', '/api/v1/leaderboard', {
      limit: PAGE_SIZE,
      offset,
      ...(since ? { since } : { period }),
    }),
  criteriaLeaderboard: (criteria: string, offset: number) =>
    request<CriteriaLeaderboard>('GET', `/api/v1/leaderboard/criteria/${encodeURIComponent(criteria)}`, {
      limit: PAGE_SIZE,
      offset,
    }),
  optOut: () => request<Participation>('POST', '/api/v1/me/opt-out'),
  optIn: () => request<Participation>('DELETE', '/api/v1/me/opt-out'),
  logout: async () => {
    const res = await fetch('/oauth/logout', { method: 'POST' })
    if (!res.ok && res.status !== 404) throw new ApiError(res.status, res.statusText)
  },
}

/** Where to send a browser to sign in, coming back to returnTo. */
export function loginURL(returnTo: string): string {
  return `/oauth/login?return_to=${encodeURIComponent(returnTo)}`
}
