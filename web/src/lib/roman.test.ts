import { expect, it } from 'vitest'

import { roman } from './roman'

it('numbers tiers the way the catalog names them', () => {
  expect([1, 4, 7, 9, 11].map(roman)).toEqual(['I', 'IV', 'VII', 'IX', 'XI'])
})
