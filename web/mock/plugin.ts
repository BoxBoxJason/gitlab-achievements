// A stand-in for the app's API, for working on the frontend without a
// GitLab instance behind it: `npm run dev:mock`. The catalog is the real
// one (mock/catalog.json, dumped from the binary's), the icons are the
// binary's own files, and the people are made up, deterministically.
//
// MOCK_AUTH picks what /api/v1/me answers: "gitlab" (the default, signed
// in as the first user), "signed-out", or "none" (a deployment without
// sign-in).

import { readFileSync } from 'node:fs'
import type { IncomingMessage, ServerResponse } from 'node:http'
import { fileURLToPath } from 'node:url'

import type { Plugin } from 'vite'

interface Tier {
  tier: number
  name: string
  description: string
  threshold: number
  exp_reward: number
}

interface Criteria {
  criteria_key: string
  category: string
  title: string
  icon_url?: string
  tiers: Tier[]
}

interface Award {
  awarded_at: string
  criteria_key: string
  name: string
  description: string
  status: string
  tier: number
  threshold: number
  exp_reward: number
  shown_on_profile: boolean
}

interface User {
  username: string
  avatar_url: string
  gitlab_user_id: number
  exp_total: number
  counters: { criteria_key: string; count: number }[]
  awards: Award[]
}

const catalog = JSON.parse(readFileSync(new URL('./catalog.json', import.meta.url), 'utf8')) as {
  criteria: Criteria[]
}

// The binary's icon for each criteria, in catalog order.
const iconFiles = [
  'commit', 'push', 'branch', 'tag', 'mropen', 'merge', 'stamp', 'reject', 'stopwatch', 'comment',
  'resolved', 'bug', 'fix', 'pipes', 'allgreen', 'firefighter', 'gears', 'rocket', 'bullseye',
  'thumbsup', 'librarian', 'mug', 'streak', 'owl', 'bird',
]
const iconOf = new Map(catalog.criteria.map((criteria, index) => [criteria.criteria_key, iconFiles[index]]))

const names = [
  'mira.okafor', 'jdoe', 'renovate-ish', 'kfischer', 'anton', 'priya.s', 'tobias', 'lea-m', 'sam',
  'yuki.tanaka', 'ofelia', 'marcus.b', 'ines', 'dk', 'hannah.w', 'raj', 'emil', 'nora', 'luca.r',
  'zoe', 'felix.h', 'amara', 'jonas', 'chloe.d', 'mateo', 'sofia.l', 'bram', 'ada', 'olu', 'kai',
  'ruth.n', 'viktor', 'elena.p', 'theo', 'maya', 'ivan.k', 'lina', 'oscar', 'fatima', 'ben.c',
  'greta', 'nils', 'aisha', 'pablo', 'mei', 'arjun', 'clara.v', 'samir', 'ingrid', 'leo',
  'tess', 'omar', 'freya', 'hugo', 'nia', 'ezra', 'linnea',
]

// A small seeded generator, so every start serves the same people.
function rng(seed: number) {
  return () => {
    seed = (seed * 1664525 + 1013904223) % 4294967296
    return seed / 4294967296
  }
}

const now = Date.now()
const day = 24 * 3600 * 1000

function buildUsers(): User[] {
  const random = rng(42)

  return names.map((username, index) => {
    // Activity falls off down the list, with some noise, like a real team.
    const level = Math.pow(1 - index / names.length, 2.2) * (0.6 + random() * 0.8)
    const counters: User['counters'] = []
    const awards: Award[] = []

    for (const criteria of catalog.criteria) {
      const top = criteria.tiers[criteria.tiers.length - 1]?.threshold ?? 1
      const reach = random() < 0.85 ? level * (0.3 + random()) : 0
      const count = Math.floor(Math.pow(top, 0.62 * reach) * reach * 4)
      if (count <= 0) continue

      counters.push({ criteria_key: criteria.criteria_key, count })

      const reached = criteria.tiers.filter((tier) => count >= tier.threshold)
      reached.forEach((tier, position) => {
        // Earlier tiers were earned earlier; the newest is recent for the busy.
        const age = (reached.length - position) * (5 + random() * 40) * day * (1.4 - level)
        const top = position === reached.length - 1
        awards.push({
          awarded_at: new Date(now - age).toISOString(),
          criteria_key: criteria.criteria_key,
          name: tier.name,
          description: tier.description,
          status: top ? 'accepted' : 'superseded',
          tier: tier.tier,
          threshold: tier.threshold,
          exp_reward: tier.exp_reward,
          shown_on_profile: top && random() < 0.4,
        })
      })
    }

    return {
      username,
      avatar_url: index % 4 === 3 ? '' : `data:image/svg+xml,${encodeURIComponent(avatar(username))}`,
      gitlab_user_id: 100 + index,
      exp_total: awards.reduce((sum, award) => sum + award.exp_reward, 0),
      counters,
      awards,
    }
  })
}

const users = buildUsers()
const byExp = () =>
  users.filter((user) => !optedOut.has(user.gitlab_user_id)).sort((a, b) => b.exp_total - a.exp_total || a.gitlab_user_id - b.gitlab_user_id)

const optedOut = new Set<number>()
let signedIn = (process.env.MOCK_AUTH ?? 'gitlab') === 'gitlab'
const authMode = process.env.MOCK_AUTH === 'none' ? 'none' : 'gitlab'

const summary = (user: User) => ({
  username: user.username,
  avatar_url: user.avatar_url,
  gitlab_user_id: user.gitlab_user_id,
  exp_total: user.exp_total,
})

function detail(user: User) {
  return {
    ...summary(user),
    rank: byExp().indexOf(user) + 1,
    counters: user.counters,
    awards: user.awards,
  }
}

function participation(user: User) {
  const out = optedOut.has(user.gitlab_user_id)
  return {
    opted_out_at: out ? new Date().toISOString() : null,
    ...(out ? { reason: 'opted_out' } : {}),
    opted_out: out,
    excluded: out,
    opt_out_allowed: true,
  }
}

const periods: Record<string, number> = { day: 1, week: 7, month: 30, year: 365 }

function page(url: URL) {
  return {
    limit: Math.min(100, Number(url.searchParams.get('limit') ?? 10)),
    offset: Number(url.searchParams.get('offset') ?? 0),
  }
}

function leaderboard(url: URL) {
  const { limit, offset } = page(url)
  const period = url.searchParams.get('period') ?? 'all'
  const sinceParam = url.searchParams.get('since')

  if (period === 'all' && !sinceParam) {
    const ranked = byExp()
    return {
      period: 'all',
      limit,
      offset,
      total: ranked.length,
      entries: ranked.slice(offset, offset + limit).map((user, i) => ({ ...summary(user), rank: offset + i + 1 })),
    }
  }

  const since = sinceParam ? new Date(sinceParam) : new Date(now - (periods[period] ?? 7) * day)
  const ranked = byExp()
    .map((user) => ({
      user,
      earned: user.awards.filter((a) => new Date(a.awarded_at) >= since).reduce((s, a) => s + a.exp_reward, 0),
    }))
    .filter((row) => row.earned > 0)
    .sort((a, b) => b.earned - a.earned || a.user.gitlab_user_id - b.user.gitlab_user_id)

  return {
    period: sinceParam ? 'custom' : period,
    since: since.toISOString(),
    limit,
    offset,
    total: ranked.length,
    entries: ranked
      .slice(offset, offset + limit)
      .map((row, i) => ({ ...summary(row.user), exp_earned: row.earned, rank: offset + i + 1 })),
  }
}

function criteriaLeaderboard(key: string, url: URL) {
  const criteria = catalog.criteria.find((c) => c.criteria_key === key)
  if (!criteria) return undefined

  const { limit, offset } = page(url)
  const ranked = byExp()
    .map((user) => ({ user, count: user.counters.find((c) => c.criteria_key === key)?.count ?? 0 }))
    .filter((row) => row.count > 0)
    .sort((a, b) => b.count - a.count || a.user.gitlab_user_id - b.user.gitlab_user_id)

  return {
    criteria_key: key,
    limit,
    offset,
    total: ranked.length,
    entries: ranked.slice(offset, offset + limit).map((row, i) => ({
      rank: offset + i + 1,
      username: row.user.username,
      avatar_url: row.user.avatar_url,
      gitlab_user_id: row.user.gitlab_user_id,
      count: row.count,
      tier: criteria.tiers.filter((t) => row.count >= t.threshold).reduce((m, t) => Math.max(m, t.tier), 0),
    })),
  }
}

// A deterministic five-by-five identicon, standing in for GitLab avatars.
function avatar(name: string): string {
  let hash = 0
  for (const char of name) hash = (hash * 31 + char.charCodeAt(0)) >>> 0
  const hue = hash % 360
  let cells = ''
  for (let y = 0; y < 5; y++) {
    for (let x = 0; x < 3; x++) {
      if ((hash >> (y * 3 + x)) & 1) {
        cells += `<rect x="${x}" y="${y}" width="1" height="1"/>`
        if (x < 2) cells += `<rect x="${4 - x}" y="${y}" width="1" height="1"/>`
      }
    }
  }
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="-1 -1 7 7" shape-rendering="crispEdges"><rect x="-1" y="-1" width="7" height="7" fill="hsl(${hue} 40% 92%)"/><g fill="hsl(${hue} 55% 45%)">${cells}</g></svg>`
}

function send(res: ServerResponse, status: number, body: unknown) {
  res.statusCode = status
  res.setHeader('Content-Type', 'application/json')
  res.end(JSON.stringify(body))
}

function me() {
  return users[0]!
}

function handle(req: IncomingMessage, res: ServerResponse): boolean {
  const url = new URL(req.url ?? '/', 'http://mock')
  const path = url.pathname
  let match: RegExpExecArray | null

  if (path === '/oauth/login') {
    signedIn = true
    res.statusCode = 302
    res.setHeader('Location', url.searchParams.get('return_to') ?? '/')
    res.end()
    return true
  }

  if (path === '/oauth/logout' && req.method === 'POST') {
    signedIn = false
    res.statusCode = 204
    res.end()
    return true
  }

  if (!path.startsWith('/api/v1/')) return false

  if ((match = /^\/api\/v1\/achievements\/([^/]+)\/icon$/.exec(path))) {
    const file = iconOf.get(decodeURIComponent(match[1]!))
    if (!file) return send(res, 404, { error: 'no icon' }), true
    res.setHeader('Content-Type', 'image/png')
    res.end(readFileSync(fileURLToPath(new URL(`../../internal/catalog/assets/${file}.png`, import.meta.url))))
    return true
  }

  if (authMode === 'gitlab' && !signedIn) return send(res, 401, { error: 'sign in first' }), true

  if (path === '/api/v1/me') {
    if (authMode === 'none') return send(res, 404, { error: 'there is no signing in on this instance (API_AUTH=none)' }), true
    const user = me()
    return send(res, 200, {
      username: user.username,
      avatar_url: user.avatar_url,
      gitlab_user_id: user.gitlab_user_id,
      record: optedOut.has(user.gitlab_user_id) ? null : detail(user),
      participation: participation(user),
    }), true
  }

  if (path === '/api/v1/me/opt-out') {
    const user = me()
    if (req.method === 'POST') optedOut.add(user.gitlab_user_id)
    if (req.method === 'DELETE') optedOut.delete(user.gitlab_user_id)
    return send(res, 200, participation(user)), true
  }

  if (path === '/api/v1/instance') return send(res, 200, { gitlab_url: 'https://gitlab.example.com', version: 'v0.0.0-mock' }), true
  if (path === '/api/v1/achievements') return send(res, 200, catalog), true
  if (path === '/api/v1/leaderboard') return send(res, 200, leaderboard(url)), true

  if ((match = /^\/api\/v1\/leaderboard\/criteria\/([^/]+)$/.exec(path))) {
    const board = criteriaLeaderboard(decodeURIComponent(match[1]!), url)
    return send(res, board ? 200 : 404, board ?? { error: 'unknown criteria' }), true
  }

  if ((match = /^\/api\/v1\/users\/([^/]+)$/.exec(path))) {
    const ref = decodeURIComponent(match[1]!)
    const user = users.find((u) => u.username === ref || String(u.gitlab_user_id) === ref)
    if (!user || optedOut.has(user.gitlab_user_id)) return send(res, 404, { error: 'no activity recorded for this user' }), true
    return send(res, 200, detail(user)), true
  }

  send(res, 404, { error: 'not found' })
  return true
}

export function mockApi(): Plugin {
  return {
    name: 'mock-api',
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        if (!handle(req, res)) next()
      })
    },
  }
}
