import { onScopeDispose, shallowRef, watchEffect } from 'vue'
import type { RouteLocationRaw } from 'vue-router'

export interface Crumb {
  label: string
  to?: RouteLocationRaw
}

/** Crumbs a page sets once it knows them, over its route's static ones. */
export const pageCrumbs = shallowRef<Crumb[]>()

/** Lets a page name itself in the breadcrumb from what it loaded. */
export function useCrumbs(crumbs: () => Crumb[] | undefined): void {
  watchEffect(() => {
    pageCrumbs.value = crumbs()
  })
  onScopeDispose(() => {
    pageCrumbs.value = undefined
  })
}
