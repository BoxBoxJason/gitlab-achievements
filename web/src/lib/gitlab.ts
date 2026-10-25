// GitLab's own rule for a username (Gitlab::PathRegex): letters, digits,
// "_", "." and "-", not starting with "-". Anything else in a stored name
// didn't come from GitLab: it came from a payload signed with the webhook
// secret, which is the one field here a sender writes freely.
const username = /^[A-Za-z0-9_.][A-Za-z0-9_.-]*$/

/**
 * The URL of a user's profile on the GitLab instance at gitlabURL, or '' when
 * username isn't one GitLab could have issued.
 *
 * The name is resolved as a path under the instance rather than trusted to be
 * one: "//evil.example/login" or "javascript:…" resolves to another origin,
 * and ".." to a page above the instance's own. The result is only returned
 * when it still lands on the instance, one segment below its base.
 */
export function profileURL(gitlabURL: string, name: string): string {
  if (!username.test(name) || /^\.+$/.test(name)) return ''

  try {
    const base = new URL(gitlabURL.replace(/\/?$/, '/'))
    const url = new URL(encodeURIComponent(name), base)

    return url.origin === base.origin && url.pathname === `${base.pathname}${name}` ? url.toString() : ''
  } catch {
    return ''
  }
}
