import type { Catalog, Counter, Criteria, EarnedTier, Tier } from '@/api/types'

/** Category keys in the order the catalog presents them, with their names. */
export const categories: { key: string; name: string }[] = [
  { key: 'git', name: 'Git' },
  { key: 'merge_requests', name: 'Merge requests & review' },
  { key: 'issues', name: 'Issues' },
  { key: 'ci_cd', name: 'CI/CD' },
  { key: 'collaboration', name: 'Collaboration' },
  { key: 'engagement', name: 'Engagement' },
]

/** The catalog's criteria grouped by category, uncategorized ones last. */
export function groupByCategory(catalog: Catalog): { key: string; name: string; criteria: Criteria[] }[] {
  const groups = categories.map((category) => ({
    ...category,
    criteria: catalog.criteria.filter((criteria) => criteria.category === category.key),
  }))

  const known = new Set(categories.map((category) => category.key))
  const other = catalog.criteria.filter((criteria) => !known.has(criteria.category))
  if (other.length > 0) groups.push({ key: 'other', name: 'Retired', criteria: other })

  return groups.filter((group) => group.criteria.length > 0)
}

/** Where one user stands on one criteria. */
export interface Standing {
  criteria: Criteria
  count: number
  /** The highest tier reached, or undefined for none yet. */
  reached?: Tier
  /** The next tier to earn, or undefined once the top one is held. */
  next?: Tier
  /** How far from the reached tier's threshold to the next one, 0 to 1. */
  progress: number
  /** When the reached tier was earned, when the user's awards say. */
  reachedAt?: string
}

/**
 * Works out a user's standing on a criteria from their counter alone, the
 * way the server does: the highest tier whose threshold the count meets.
 */
export function standingOf(criteria: Criteria, count: number, awards: readonly EarnedTier[] = []): Standing {
  const tiers = [...criteria.tiers].sort((a, b) => a.threshold - b.threshold)

  let reached: Tier | undefined
  let next: Tier | undefined
  for (const tier of tiers) {
    if (count >= tier.threshold) {
      reached = tier
    } else {
      next = tier
      break
    }
  }

  let progress = 1
  if (next) {
    const floor = reached?.threshold ?? 0
    progress = Math.min(1, Math.max(0, (count - floor) / (next.threshold - floor)))
  }

  const reachedAt = reached
    ? awards.find((award) => award.criteria_key === criteria.criteria_key && award.tier === reached.tier)?.awarded_at
    : undefined

  return { criteria, count, reached, next, progress, reachedAt }
}

/** Every criteria's standing for one user's counters. */
export function standingsOf(
  catalog: Catalog,
  counters: readonly Counter[],
  awards: readonly EarnedTier[],
): Map<string, Standing> {
  const counts = new Map(counters.map((counter) => [counter.criteria_key, counter.count]))

  return new Map(
    catalog.criteria.map((criteria) => [
      criteria.criteria_key,
      standingOf(criteria, counts.get(criteria.criteria_key) ?? 0, awards),
    ]),
  )
}
