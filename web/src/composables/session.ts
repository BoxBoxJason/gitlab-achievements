import { reactive, readonly } from 'vue'

import { api, ApiError } from '@/api/client'
import type { Instance, Me } from '@/api/types'

/**
 * Who is looking, as /api/v1/me answers it:
 *
 * - signed-in: a GitLab user with a session;
 * - signed-out: sign-in is required and there is no session yet;
 * - anonymous: this deployment has no sign-in (API_AUTH=none);
 * - forbidden: signed in, but outside the group the API is restricted to.
 */
export type SessionStatus = 'loading' | 'signed-in' | 'signed-out' | 'anonymous' | 'forbidden' | 'error'

const state = reactive({
  status: 'loading' as SessionStatus,
  me: null as Me | null,
  instance: null as Instance | null,
  error: '',
})

export const session = readonly(state)

/** Asks the API who is looking, and about the instance once allowed to. */
export async function loadSession(): Promise<void> {
  try {
    state.me = await api.me()
    state.status = 'signed-in'
  } catch (error) {
    if (!(error instanceof ApiError)) {
      state.status = 'error'
      state.error = 'The API could not be reached.'
      return
    }

    switch (error.status) {
      case 404:
        state.status = 'anonymous'
        break
      case 401:
        state.status = 'signed-out'
        return
      case 403:
        state.status = 'forbidden'
        state.error = error.message
        return
      default:
        state.status = 'error'
        state.error = error.message
        return
    }
  }

  try {
    state.instance = await api.instance()
  } catch {
    // The instance's description only links names to GitLab; everything
    // else works without it.
  }
}

/** Re-reads /me after something changed it, such as opting out. */
export async function refreshMe(): Promise<void> {
  if (state.status !== 'signed-in') return
  try {
    state.me = await api.me()
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) state.status = 'signed-out'
  }
}

/** Reacts to a request the API refused for want of a session. */
export function noteUnauthorized(error: unknown): void {
  if (error instanceof ApiError && error.status === 401 && state.status === 'signed-in') {
    state.status = 'signed-out'
    state.me = null
  }
}

export async function signOut(): Promise<void> {
  await api.logout()
  state.status = 'signed-out'
  state.me = null
}

/** Whether ref names the signed-in user. */
export function isMe(ref: string | number): boolean {
  const me = state.me
  if (!me) return false

  return String(ref) === String(me.gitlab_user_id) || String(ref).toLowerCase() === me.username.toLowerCase()
}
