// The API's wire shapes, as docs/api.md describes them.

export interface Summary {
  username: string
  avatar_url: string
  gitlab_user_id: number
  exp_total: number
}

export interface Counter {
  criteria_key: string
  count: number
}

export type AwardStatus = 'pending' | 'accepted' | 'failed' | 'superseded'

export interface EarnedTier {
  awarded_at: string
  criteria_key: string
  name: string
  description: string
  status: AwardStatus
  tier: number
  threshold: number
  exp_reward: number
  shown_on_profile: boolean
}

export interface Detail extends Summary {
  rank: number
  counters: Counter[]
  awards: EarnedTier[]
}

export interface Entry extends Summary {
  rank: number
  exp_earned?: number
}

export type Period = 'all' | 'day' | 'week' | 'month' | 'year'

export interface Leaderboard {
  period: Period | 'custom'
  since?: string
  limit: number
  offset: number
  total: number
  entries: Entry[]
}

export interface CriteriaEntry {
  rank: number
  username: string
  avatar_url: string
  gitlab_user_id: number
  count: number
  tier: number
}

export interface CriteriaLeaderboard {
  criteria_key: string
  limit: number
  offset: number
  total: number
  entries: CriteriaEntry[]
}

export interface Tier {
  tier: number
  name: string
  description: string
  threshold: number
  exp_reward: number
}

export interface Criteria {
  criteria_key: string
  category: string
  title: string
  icon_url?: string
  tiers: Tier[]
}

export interface Catalog {
  criteria: Criteria[]
}

export interface Participation {
  opted_out_at: string | null
  reason?: 'bot' | 'configured' | 'opted_out' | string
  opted_out: boolean
  excluded: boolean
  opt_out_allowed: boolean
}

export interface Me {
  username: string
  avatar_url: string
  gitlab_user_id: number
  record: Detail | null
  participation?: Participation
}

export interface Instance {
  gitlab_url: string
  version: string
}
