import { describe, expect, it } from 'vitest'

import type { Catalog, Criteria } from '@/api/types'

import { groupByCategory, standingOf, standingsOf } from './catalog'

const commits: Criteria = {
  criteria_key: 'commits',
  category: 'git',
  title: 'Committer',
  tiers: [
    { tier: 2, name: 'Committer II', description: 'Author 5 commits.', threshold: 5, exp_reward: 3 },
    { tier: 1, name: 'Committer I', description: 'Author your first commit.', threshold: 1, exp_reward: 1 },
    { tier: 3, name: 'Committer III', description: 'Author 10 commits.', threshold: 10, exp_reward: 5 },
  ],
}

describe('standingOf', () => {
  it('has reached nothing at zero, and is on its way to the first tier', () => {
    const standing = standingOf(commits, 0)

    expect(standing.reached).toBeUndefined()
    expect(standing.next?.tier).toBe(1)
    expect(standing.progress).toBe(0)
  })

  it('measures progress from the reached threshold, not from zero', () => {
    const standing = standingOf(commits, 7)

    expect(standing.reached?.tier).toBe(2)
    expect(standing.next?.tier).toBe(3)
    expect(standing.progress).toBeCloseTo(0.4)
  })

  it('counts a threshold met exactly as reached', () => {
    expect(standingOf(commits, 5).reached?.tier).toBe(2)
  })

  it('is complete past the top tier', () => {
    const standing = standingOf(commits, 1000)

    expect(standing.reached?.tier).toBe(3)
    expect(standing.next).toBeUndefined()
    expect(standing.progress).toBe(1)
  })

  it('dates the reached tier from the matching award', () => {
    const standing = standingOf(commits, 6, [
      {
        awarded_at: '2026-03-14T09:21:04Z',
        criteria_key: 'commits',
        name: 'Committer II',
        description: '',
        status: 'accepted',
        tier: 2,
        threshold: 5,
        exp_reward: 3,
        shown_on_profile: false,
      },
    ])

    expect(standing.reachedAt).toBe('2026-03-14T09:21:04Z')
  })
})

describe('standingsOf', () => {
  it('gives every criteria a standing, counted or not', () => {
    const catalog: Catalog = { criteria: [commits, { ...commits, criteria_key: 'pushes' }] }
    const standings = standingsOf(catalog, [{ criteria_key: 'commits', count: 3 }], [])

    expect(standings.get('commits')?.count).toBe(3)
    expect(standings.get('pushes')?.count).toBe(0)
  })
})

describe('groupByCategory', () => {
  it('keeps catalog order and puts criteria it cannot place last', () => {
    const groups = groupByCategory({
      criteria: [
        { ...commits, criteria_key: 'old', category: '' },
        { ...commits, criteria_key: 'deployments', category: 'ci_cd' },
        commits,
      ],
    })

    expect(groups.map((group) => group.key)).toEqual(['git', 'ci_cd', 'other'])
  })
})
