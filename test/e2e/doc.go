// Package e2e drives the built binary against a real, throwaway GitLab
// instance and checks that every criteria in the catalog actually advances,
// through both the live webhook path and the two Events API read paths
// (backfill and reconcile).
//
// Unit tests pin the normalizers against payloads written from GitLab's
// documentation. What they cannot catch is GitLab changing what it really
// sends, which is how a criteria silently stops counting on a newer release.
// This suite produces one of every activity the catalog tracks, as real
// users, and reads the resulting counters back through the app's own API.
//
// It is behind the e2e build tag and skips unless pointed at an instance.
// See test/e2e/run.sh for bringing one up, and docs/content/docs/e2e.md for what it
// covers and what it deliberately does not.
package e2e
