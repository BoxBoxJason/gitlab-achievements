import { shallowRef } from 'vue'

import { api } from '@/api/client'
import type { Catalog } from '@/api/types'

const catalog = shallowRef<Catalog>()
let pending: Promise<Catalog> | undefined

/**
 * The achievement catalog, fetched once per visit: it only changes when the
 * app is upgraded, which reloads the page anyway.
 */
export function loadCatalog(): Promise<Catalog> {
  pending ??= api.catalog().then(
    (result) => (catalog.value = result),
    (error: unknown) => {
      pending = undefined
      throw error
    },
  )

  return pending
}

export function useCatalog() {
  return catalog
}
