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
	// so a stream of distinct bogus tokens cannot grow it without limit
	// (and failed verifications are never cached in the first place).
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

// cachingVerifier remembers successful verifications for a short while.
//
// Without it every API request costs a GitLab round-trip, so a page making
// five calls makes five more against the instance. A cached identity is
// trusted for ttl: that is how long a token revoked on GitLab (or a user
// removed from the allowed group) keeps working here, which is the price of
// the cache and why the TTL is short and configurable down to off.
//
// Only successes are cached. A rejected or unverifiable credential is asked
// about again every time, so a token that starts working (GitLab back up,
// user added to the group) does so at once.
//
// Tokens are keyed by their SHA-256, so the cache never holds a credential
// in the clear.
type cachingVerifier struct {
	inner   Verifier
	entries map[[sha256.Size]byte]cachedIdentity
	now     func() time.Time
	ttl     time.Duration
	mu      sync.Mutex
}

// identityForgetter is a Verifier that remembers identities and can be told
// to drop one, which is what logout needs without knowing the cache's type.
type identityForgetter interface {
	forget(token string)
}

type cachedIdentity struct {
	expires  time.Time
	identity gitlabclient.Identity
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

	if identity, ok := v.lookup(key); ok {
		return identity, nil
	}

	identity, err := v.inner.Verify(ctx, token)
	if err != nil {
		return nil, err //nolint:wrapcheck // the inner verifier's error is the answer, unchanged
	}

	v.store(key, identity)

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

func (v *cachingVerifier) lookup(key [sha256.Size]byte) (*gitlabclient.Identity, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()

	entry, ok := v.entries[key]
	if !ok {
		return nil, false
	}

	if !v.now().Before(entry.expires) {
		delete(v.entries, key)

		return nil, false
	}

	identity := entry.identity

	return &identity, true
}

func (v *cachingVerifier) store(key [sha256.Size]byte, identity *gitlabclient.Identity) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if len(v.entries) >= identityCacheEntries {
		v.evict()
	}

	v.entries[key] = cachedIdentity{identity: *identity, expires: v.now().Add(v.ttl)}
}

// evict makes room: expired entries first, and if none had expired, an
// arbitrary one. Which live entry goes hardly matters, since all it costs
// is one extra GitLab call for whoever held it.
func (v *cachingVerifier) evict() {
	now := v.now()

	for key, entry := range v.entries {
		if !now.Before(entry.expires) {
			delete(v.entries, key)
		}
	}

	if len(v.entries) < identityCacheEntries {
		return
	}

	for key := range v.entries {
		delete(v.entries, key)

		return
	}
}
