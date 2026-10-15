import { describe, expect, it } from 'vitest'
import { effectScope, nextTick, ref } from 'vue'

import { useRequest } from './useRequest'

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((r) => (resolve = r))
  return { promise, resolve }
}

describe('useRequest', () => {
  it('keeps the answer to the latest request when an earlier one lands last', async () => {
    const page = ref(1)
    const answers = new Map([1, 2].map((n) => [n, deferred<string>()]))

    const scope = effectScope()
    const request = scope.run(() => useRequest(page, (n) => answers.get(n)!.promise))!

    page.value = 2
    await nextTick()

    answers.get(2)!.resolve('page 2')
    await nextTick()
    answers.get(1)!.resolve('page 1')
    await new Promise((r) => setTimeout(r))

    expect(request.data.value).toBe('page 2')
    expect(request.loading.value).toBe(false)
    scope.stop()
  })

  it('reports a failure and clears stale data', async () => {
    const key = ref('a')
    const scope = effectScope()
    const request = scope.run(() =>
      useRequest(key, async (k) => {
        if (k === 'b') throw new Error('gone')
        return k
      }),
    )!

    await new Promise((r) => setTimeout(r))
    expect(request.data.value).toBe('a')

    key.value = 'b'
    await new Promise((r) => setTimeout(r))

    expect(request.error.value?.message).toBe('gone')
    expect(request.data.value).toBeUndefined()
    scope.stop()
  })
})
