package gitlabclient

import (
	"iter"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// pageFunc is a GitLab List* call with everything bound except the
// per-request options (pagination cursor, context, ...).
type pageFunc[T any] func(options ...gitlab.RequestOptionFunc) ([]T, *gitlab.Response, error)

// iteratePages walks every page of a List call, yielding one item at a
// time instead of materializing the full collection in memory. It follows
// whichever pagination style the response actually uses (keyset, offset, or
// GraphQL cursor — see gitlab.WithNext), so keyset pagination is used
// automatically whenever the caller requested it (opt.Pagination =
// "keyset") and the endpoint supports it.
//
// Iteration stops after the first error, which is yielded as the final
// value with a zero item.
func iteratePages[T any](call pageFunc[T]) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var next gitlab.RequestOptionFunc

		for {
			var reqOpts []gitlab.RequestOptionFunc
			if next != nil {
				reqOpts = []gitlab.RequestOptionFunc{next}
			}

			items, resp, err := call(reqOpts...)
			if err != nil {
				var zero T

				yield(zero, err)

				return
			}

			for _, item := range items {
				if !yield(item, nil) {
					return
				}
			}

			var ok bool

			next, ok = gitlab.WithNext(resp)
			if !ok {
				return
			}
		}
	}
}

// withExtra returns a new slice combining base with extra, without
// mutating either.
func withExtra(base []gitlab.RequestOptionFunc, extra ...gitlab.RequestOptionFunc) []gitlab.RequestOptionFunc {
	merged := make([]gitlab.RequestOptionFunc, 0, len(base)+len(extra))
	merged = append(merged, base...)
	merged = append(merged, extra...)

	return merged
}
