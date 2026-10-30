import { expect, it } from 'vitest'

import { tierBand } from './tier'

it('bands tiers the way the GitLab badges are drawn, the top band taking any past it', () => {
  expect([1, 3, 4, 6, 7, 9, 10, 11, 12, 13].map(tierBand)).toEqual([
    'bronze',
    'bronze',
    'silver',
    'silver',
    'gold',
    'gold',
    'purple',
    'purple',
    'purple',
    'purple',
  ])
})
