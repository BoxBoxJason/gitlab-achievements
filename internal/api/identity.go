package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
)

const (
	// identityCacheEntries bounds the cache. Far above the number of people
	// who could plausibly be using the API within one TTL, it exists only
	// so a stream of distinct tokens cannot grow it without limit (and
	// tokens GitLab rejects are never cached in the first place).
	identityCacheEntries = 10_000
)

// ErrForbidden reports a credential GitLab accepted for an identity this
// API does not serve: one outside the allowed group.
var ErrForbidden = errors.New("not allowed to use this api")

// MembershipChecker reports whether a GitLab user belongs to the group the
// API is restricted to, inherited membership included. It is satisfied by
// a closure over *gitlabclient.ReadClient.
type MembershipChecker func(ctx context.Context, gitlabUserID int64) (bool, error)

// authorizingVerifier restricts a Verifier to members of one group.
//
// Instance administrators are let through whether or not they are members:
// they can read everything on the instance anyway, including this app's
// database, so refusing them would protect nothing and only confuse the
// person most likely to be debugging the deployment.
type authorizingVerifier struct {
	inner    Verifier
	isMember MembershipChecker
}

// Verify resolves token, then checks the identity behind it is allowed.
func (v *authorizingVerifier) Verify(ctx context.Context, token string) (*gitlabclient.Identity, error) {
	identity, err := v.inner.Verify(ctx, token)
	if err != nil {
		return nil, err //nolint:wrapcheck // the inner verifier's error is the answer, unchanged
	}

	if identity.IsAdmin {
		return identity, nil
	}

	member, err := v.isMember(ctx, identity.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to check group membership for user %d: %w", identity.ID, err)
	}

	if !member {
		return nil, fmt.Errorf("%w: user %d is not in the allowed group", ErrForbidden, identity.ID)
	}

	return identity, nil
}

// cachingVerifier remembers verifications for a short while.
//
// Without it every API request costs a GitLab round-trip, so a page making
// five calls makes five more against the instance. A cached identity is
// trusted for ttl: that is how long a token revoked on GitLab (or a user
// removed from the allowed group) keeps working here, which is the price of
// the cache and why the TTL is short and configurable down to off.
//
// A refusal for being outside the allowed group is cached for ttl too, so a
// user added to the group may wait that long to get in. Left uncached, any
// account on the instance could spend a membership lookup on this app's
// read token with every request it sent, which a cached member never costs.
//
// Every other failure is asked about again every time, so a token that
// starts working (GitLab back up) does so at once. A token GitLab rejects
// needs no cache: sending it costs the sender nothing, and a stream of
// fresh ones would miss any cache anyway.
//
// Tokens are keyed by their SHA-256, so the cache never holds a credential
// in the clear.
type cachingVerifier struct {
	inner   Verifier
	entries map[[sha256.Size]byte]cachedIdentity
	now     func() time.Time
	// order lists keys as they were stored. Every entry lives for the same
	// ttl, so this is also the order they expire in, and the oldest entry
	// is always at the front. It may still name keys since forgotten or
	// stored again; evict skips those, so each is looked at once.
	order []queuedKey
	ttl   time.Duration
	mu    sync.Mutex
}

// identityForgetter is a Verifier that remembers identities and can be told
// to drop one, which is what logout needs without knowing the cache's type.
type identityForgetter interface {
	forget(token string)
}

type cachedIdentity struct {
	expires time.Time
	// denial is the ErrForbidden the token was refused with, when it was;
	// identity is then unset.
	denial   error
	identity gitlabclient.Identity
}

type queuedKey struct {
	expires time.Time
	key     [sha256.Size]byte
}

// newCachingVerifier wraps inner, or returns it unwrapped when ttl is not
// positive.
func newCachingVerifier(inner Verifier, ttl time.Duration) Verifier {
	if ttl <= 0 {
		return inner
	}

	return &cachingVerifier{
		inner:   inner,
		ttl:     ttl,
		entries: map[[sha256.Size]byte]cachedIdentity{},
		now:     time.Now,
	}
}

// Verify answers from the cache when it can, and from inner otherwise.
func (v *cachingVerifier) Verify(ctx context.Context, token string) (*gitlabclient.Identity, error) {
	key := sha256.Sum256([]byte(token))

	if entry, ok := v.lookup(key); ok {
		if entry.denial != nil {
			return nil, entry.denial
		}

		return &entry.identity, nil
	}

	identity, err := v.inner.Verify(ctx, token)
	if errors.Is(err, ErrForbidden) {
		v.store(key, cachedIdentity{denial: err})
	}

	if err != nil {
		return nil, err //nolint:wrapcheck // the inner verifier's error is the answer, unchanged
	}

	v.store(key, cachedIdentity{identity: *identity})

	return identity, nil
}

// forget drops token from the cache, so a session that just logged out is
// not still honored through it.
func (v *cachingVerifier) forget(token string) {
	key := sha256.Sum256([]byte(token))

	v.mu.Lock()
	delete(v.entries, key)
	v.mu.Unlock()
}

func (v *cachingVerifier) lookup(key [sha256.Size]byte) (cachedIdentity, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()

	entry, ok := v.entries[key]
	if !ok {
		return cachedIdentity{}, false
	}

	if !v.now().Before(entry.expires) {
		delete(v.entries, key)

		return cachedIdentity{}, false
	}

	return entry, true
}

// store remembers entry under key for ttl.
func (v *cachingVerifier) store(key [sha256.Size]byte, entry cachedIdentity) {
	v.mu.Lock()
	defer v.mu.Unlock()

	now := v.now()
	v.evict(now)

	expires := now.Add(v.ttl)
	entry.expires = expires
	v.entries[key] = entry
	v.order = append(v.order, queuedKey{key: key, expires: expires})
}

// evict drops entries from the front of order: every one that has expired,
// then, while the cache is still full, the live ones closest to expiring.
// Which live entry goes hardly matters, since all it costs is one extra
// GitLab call for whoever held it. Each queued key is popped once, so this
// stays cheap however full the cache is.
func (v *cachingVerifier) evict(now time.Time) {
	for len(v.order) > 0 {
		oldest := v.order[0]
		if now.Before(oldest.expires) && len(v.entries) < identityCacheEntries {
			return
		}

		v.order[0] = queuedKey{}
		v.order = v.order[1:]

		// The key may have been forgotten, or stored again since and queued
		// further back; only the entry this queued key stands for goes.
		if entry, ok := v.entries[oldest.key]; ok && entry.expires.Equal(oldest.expires) {
			delete(v.entries, oldest.key)
		}
	}
}
