import { onScopeDispose, ref, shallowRef, watch, type Ref, type WatchSource } from 'vue'

import { ApiError } from '@/api/client'

import { noteUnauthorized } from './session'

/**
 * Loads data whenever source changes, keeping only the answer to the latest
 * request: paging quickly through a ranking must not let a slow page 2
 * overwrite page 3.
 */
export function useRequest<T, S>(source: WatchSource<S>, load: (value: S) => Promise<T>) {
  const data = shallowRef<T>()
  const error: Ref<ApiError | Error | undefined> = ref()
  const loading = ref(true)
  let latest = 0

  const run = async (value: S) => {
    const id = ++latest
    loading.value = true
    error.value = undefined

    try {
      const result = await load(value)
      if (id === latest) data.value = result
    } catch (caught) {
      if (id !== latest) return
      noteUnauthorized(caught)
      error.value = caught instanceof Error ? caught : new Error(String(caught))
      data.value = undefined
    } finally {
      if (id === latest) loading.value = false
    }
  }

  watch(source, run, { immediate: true })
  onScopeDispose(() => {
    latest = -1
  })

  return { data, error, loading, reload: () => run((typeof source === 'function' ? source() : source.value) as S) }
}
