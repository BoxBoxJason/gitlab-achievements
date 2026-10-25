import { describe, expect, it } from 'vitest'

import { profileURL } from './gitlab'

describe('profileURL', () => {
  it('links a username under the instance', () => {
    expect(profileURL('https://gitlab.example.com', 'alice.b-c_d')).toBe('https://gitlab.example.com/alice.b-c_d')
    expect(profileURL('https://example.com/gitlab/', 'alice')).toBe('https://example.com/gitlab/alice')
  })

  it('refuses names GitLab would not issue', () => {
    for (const name of [
      '//attacker.example.test/login',
      'javascript:alert(1)',
      'https://attacker.example.test',
      '..',
      '.',
      '-alice',
      'alice/../admin',
      'alice?x=1',
      'alice#frag',
      '',
    ]) {
      expect(profileURL('https://gitlab.example.com/', name), name).toBe('')
    }
  })
})
