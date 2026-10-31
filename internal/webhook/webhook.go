// Package webhook receives GitLab webhook deliveries and turns them into
// the normalized activity the achievement engine evaluates.
//
// It is the live half of the same pipeline the historical backfill feeds:
// payloads are translated into activity.Event values and handed to the same
// activity.Processor, so historical and live activity are judged by
// identical rules rather than by two implementations that drift.
//
// Three properties shape the design:
//
//   - Fast acknowledgement. GitLab records a webhook as failed if the
//     response is slow, and disables hooks that keep failing. Deliveries
//     are therefore validated, parsed, and queued, with evaluation left to
//     background workers (see Queue).
//   - Idempotency. Hooks redeliver, and the same activity can reach the app
//     from more than one hook. Every activity carries a dedup key derived
//     from GitLab's own identifiers, and the engine discards keys it has
//     already counted, so processing the same delivery twice counts it once.
//   - Nothing lost quietly. A delivery that can't be parsed, or an activity
//     that can't be evaluated, is reported with enough detail to replay it
//     rather than dropped in silence.
package webhook

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"iter"
	"net/http"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go/v3"
	"go.uber.org/zap"

	"github.com/boxboxjason/gitlab-achievements/internal/activity"
	"github.com/boxboxjason/gitlab-achievements/internal/metrics"
)

// maxPayloadBytes bounds how large a delivery this app will read. GitLab
// push payloads grow with the commits they carry, and merge request ones
// with their diffs, but nothing legitimate approaches this; the limit is
// here so an oversized or hostile body can't be read into memory unbounded.
const maxPayloadBytes = 10 << 20 // 10 MiB

// subscribedEvents are the event types this app's hooks are registered for
// (see the bootstrap package's hook options). Deliveries of any other type
// are ignored quietly; a failure to parse one of these is worth reporting,
// because it means an event this app wanted was lost.
//
//nolint:gochecknoglobals // a package-level lookup table, read-only after init
var subscribedEvents = map[gitlab.EventType]bool{
	gitlab.EventTypePush:          true,
	gitlab.EventTypeTagPush:       true,
	gitlab.EventTypeMergeRequest:  true,
	gitlab.EventTypeIssue:         true,
	gitlab.EventConfidentialIssue: true,
	gitlab.EventTypeNote:          true,
	gitlab.EventConfidentialNote:  true,
	gitlab.EventTypePipeline:      true,
	gitlab.EventTypeJob:           true,
	gitlab.EventTypeDeployment:    true,
	gitlab.EventTypeEmoji:         true,
	gitlab.EventTypeWikiPage:      true,
	gitlab.EventTypeRelease:       true,
}

// dispatcher is what the receiver hands normalized activity to, satisfied
// by *Queue.
type dispatcher interface {
	Enqueue(ctx context.Context, events []activity.Event) error
}

// discussionSource lists a merge request's discussions, satisfied by
// *gitlabclient.ReadClient.
type discussionSource interface {
	ListMergeRequestDiscussions(pid any, mergeRequest int64, opt *gitlab.ListMergeRequestDiscussionsOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.Discussion, error]
}

// releaseSource reads a release, satisfied by *gitlabclient.ReadClient.
type releaseSource interface {
	GetRelease(pid any, tagName string, options ...gitlab.RequestOptionFunc) (*gitlab.Release, error)
}

// pipelineHistory reports how a ref's previous pipeline ended, satisfied by
// *gitlabclient.ReadClient.
type pipelineHistory interface {
	LastPipelineOutcome(pid any, ref string, beforeID int64, options ...gitlab.RequestOptionFunc) (gitlab.BuildStateValue, error)
}

// apiFetchTimeout bounds the trip to the API a delivery may cost, well
// inside the time GitLab waits for a hook to answer.
const apiFetchTimeout = 5 * time.Second

// discussionsPerPage is the page size of the merge request discussions fetch.
const discussionsPerPage = 100

// Receiver handles GitLab webhook deliveries.
type Receiver struct {
	queue       dispatcher
	discussions discussionSource
	releases    releaseSource
	pipelines   pipelineHistory
	secret      string
}

// ReceiverOption configures optional parts of a Receiver.
type ReceiverOption func(*Receiver)

// WithDiscussions lets the receiver read a merge request's discussions,
// which is the only way to see a thread being resolved on a GitLab that
// reports it as a bare merge request update, or not at all. Without it, resolutions are
// only counted from the note hooks older versions send.
func WithDiscussions(source discussionSource) ReceiverOption {
	return func(r *Receiver) { r.discussions = source }
}

// WithReleases lets the receiver read a release, which is the only way to
// learn who created one: the release hook names nobody. Without it,
// releases are only counted by backfill and reconciliation.
func WithReleases(source releaseSource) ReceiverOption {
	return func(r *Receiver) { r.releases = source }
}

// WithPipelineHistory lets the receiver look up the pipeline before a
// passing one, which is how it tells a pass that fixed the build from one
// that kept it green. Without it, fixes are only counted by backfill and
// reconciliation.
func WithPipelineHistory(source pipelineHistory) ReceiverOption {
	return func(r *Receiver) { r.pipelines = source }
}

// NewReceiver builds a Receiver that authenticates deliveries against
// secret and hands the activity it parses to queue.
func NewReceiver(secret string, queue dispatcher, opts ...ReceiverOption) *Receiver {
	receiver := &Receiver{
		secret: secret,
		queue:  queue,
	}

	for _, opt := range opts {
		opt(receiver)
	}

	return receiver
}

// ServeHTTP validates, parses, and queues one delivery.
//
// The status codes are chosen for how GitLab treats them, since a hook that
// accumulates failures is disabled:
//
//   - 401 for a bad or missing token: the delivery didn't come from the
//     hook this app registered, and nobody should retry it.
//   - 503 when the delivery is well-formed but this app can't take
//     responsibility for it right now. This is the one worth retrying.
//   - 200 for everything else, including payloads that couldn't be read or
//     parsed and event types this app tracks nothing for. Those are logged
//     rather than reported as failures: a redelivery of an unparseable body
//     would fail identically, and answering anything else would eventually
//     cost the hook its registration over events that were never
//     interesting.
func (r *Receiver) ServeHTTP(resp http.ResponseWriter, req *http.Request) {
	eventType := gitlab.HookEventType(req)
	label := eventTypeLabel(eventType)

	if !r.authenticate(req) {
		metrics.WebhookDelivery(label, metrics.DeliveryUnauthorized)
		http.Error(resp, "invalid token", http.StatusUnauthorized)

		return
	}

	payload, err := io.ReadAll(http.MaxBytesReader(resp, req.Body, maxPayloadBytes))
	if err != nil {
		zap.L().Warn("failed to read webhook payload",
			zap.String("event_type", string(eventType)),
			zap.Error(err),
		)
		metrics.WebhookDelivery(label, metrics.DeliveryUnparsable)

		// MaxBytesReader closes the connection after an oversized body only
		// when resp is net/http's own writer, which the metrics middleware
		// wraps. Asking for it explicitly keeps the server from reading on
		// through the rest of the body to reuse the connection.
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			resp.Header().Set("Connection", "close")
		}

		resp.WriteHeader(http.StatusOK)

		return
	}

	events, parsed := r.parse(req.Context(), eventType, payload)
	if len(events) == 0 {
		result := metrics.DeliveryIgnored
		if !parsed && subscribedEvents[eventType] {
			result = metrics.DeliveryUnparsable
		}

		metrics.WebhookDelivery(label, result)
		resp.WriteHeader(http.StatusOK)

		return
	}

	err = r.queue.Enqueue(req.Context(), events)
	if err != nil {
		zap.L().Error("failed to accept webhook delivery",
			zap.String("event_type", string(eventType)),
			zap.Int("activities", len(events)),
			zap.Error(err),
		)
		metrics.WebhookDelivery(label, metrics.DeliveryRejected)
		http.Error(resp, "not accepting deliveries right now", http.StatusServiceUnavailable)

		return
	}

	metrics.WebhookDelivery(label, metrics.DeliveryAccepted)
	resp.WriteHeader(http.StatusOK)
}

// eventTypeLabel bounds the event type a delivery reports to the ones this
// app subscribes to. The header is set by whoever sent the request, and is
// read before the token is checked, so passing it through as a metric label
// would let anyone mint series.
func eventTypeLabel(eventType gitlab.EventType) string {
	if subscribedEvents[eventType] {
		return string(eventType)
	}

	return "other"
}

// authenticate compares the delivery's token against the configured secret
// in constant time, so a wrong token can't be discovered a byte at a time
// by measuring the response.
func (r *Receiver) authenticate(req *http.Request) bool {
	presented := gitlab.HookEventToken(req)

	return subtle.ConstantTimeCompare([]byte(presented), []byte(r.secret)) == 1
}

// parse turns a raw delivery into the activities it represents, reporting
// nothing for the event types and payload shapes this app tracks nothing
// for. The second result reports whether the payload parsed at all.
//
// GitLab delivers event types beyond the ones a hook subscribed to (a
// subgroup event on a group hook, a type added in a newer GitLab), so an
// unrecognized type is expected rather than exceptional. A type this app
// did ask for that won't parse is a different matter: something was lost,
// and it is logged at warn so it shows up without the hook being failed.
func (r *Receiver) parse(ctx context.Context, eventType gitlab.EventType, payload []byte) ([]activity.Event, bool) {
	parsed, err := gitlab.ParseWebhook(eventType, payload)
	if err != nil {
		if subscribedEvents[eventType] {
			zap.L().Warn("failed to parse a webhook delivery this app subscribed to",
				zap.String("event_type", string(eventType)),
				zap.Error(err),
			)
		} else {
			zap.L().Debug("ignoring webhook delivery this app does not handle",
				zap.String("event_type", string(eventType)),
				zap.Error(err),
			)
		}

		return nil, false
	}

	now := time.Now()

	return append(normalize(parsed, now), r.lookUp(ctx, parsed, now)...), true
}

// lookUp derives the activities a delivery implies but does not describe,
// by asking the API what the payload leaves out: which threads an update
// resolved, who created a release, whether a pass fixed the build. Each
// lookup is made only when its source was configured and the delivery is
// one it could change the answer for.
func (r *Receiver) lookUp(ctx context.Context, parsed any, receivedAt time.Time) []activity.Event {
	switch payload := parsed.(type) {
	case *gitlab.MergeEvent:
		if r.discussions != nil && resolutionCandidate(payload) {
			return r.resolvedDiscussions(ctx, payload, receivedAt)
		}
	case *gitlab.ReleaseEvent:
		if r.releases != nil && releaseCreated(payload) {
			return r.publishedRelease(ctx, payload, receivedAt)
		}
	case *gitlab.PipelineEvent:
		if r.pipelines != nil && pipelinePassed(payload) {
			return r.fixedPipeline(ctx, payload, receivedAt)
		}
	}

	return nil
}

// fixedPipeline reports a passing pipeline as having fixed the build when
// the previous pipeline on its ref had failed.
//
// A failed lookup is logged and costs this delivery its fix only:
// reconciliation asks the same question of every pipeline that passed in
// its window.
func (r *Receiver) fixedPipeline(ctx context.Context, event *gitlab.PipelineEvent, receivedAt time.Time) []activity.Event {
	ctx, cancel := context.WithTimeout(ctx, apiFetchTimeout)
	defer cancel()

	attrs := event.ObjectAttributes

	previous, err := r.pipelines.LastPipelineOutcome(event.Project.ID, attrs.Ref, attrs.ID, gitlab.WithContext(ctx))
	if err != nil {
		zap.L().Warn("failed to look up the pipeline before a passing one",
			zap.Int64("project_id", event.Project.ID),
			zap.Int64("pipeline_id", attrs.ID),
			zap.Error(err),
		)

		return nil
	}

	return normalizePipelineFix(event, previous, receivedAt)
}

// publishedRelease reads the release a creation hook is about, to credit it
// to its author.
//
// A failed read is logged and costs this delivery its release. GitLab sends
// no second creation hook, so the release is left to the next
// reconciliation pass, which reads it from the same API.
func (r *Receiver) publishedRelease(ctx context.Context, event *gitlab.ReleaseEvent, receivedAt time.Time) []activity.Event {
	ctx, cancel := context.WithTimeout(ctx, apiFetchTimeout)
	defer cancel()

	release, err := r.releases.GetRelease(event.Project.ID, event.Tag, gitlab.WithContext(ctx))
	if err != nil {
		zap.L().Warn("failed to read a release to credit its author",
			zap.Int64("project_id", event.Project.ID),
			zap.String("tag", event.Tag),
			zap.Error(err),
		)

		return nil
	}

	return normalizeRelease(event, release, receivedAt)
}

// resolvedDiscussions reads the discussions of the merge request a hook is
// about and reports the resolved ones.
//
// A failed read is logged and costs only this delivery's resolutions: the
// next update of the merge request reads the same threads again, and the
// resolution deduplicates on its discussion, so nothing is counted twice.
func (r *Receiver) resolvedDiscussions(ctx context.Context, event *gitlab.MergeEvent, receivedAt time.Time) []activity.Event {
	ctx, cancel := context.WithTimeout(ctx, apiFetchTimeout)
	defer cancel()

	projectID := event.ObjectAttributes.TargetProjectID
	if projectID == 0 {
		projectID = event.Project.ID
	}

	var discussions []*gitlab.Discussion

	opts := &gitlab.ListMergeRequestDiscussionsOptions{ListOptions: gitlab.ListOptions{PerPage: discussionsPerPage}}

	for discussion, err := range r.discussions.ListMergeRequestDiscussions(projectID, event.ObjectAttributes.IID, opts, gitlab.WithContext(ctx)) {
		if err != nil {
			zap.L().Warn("failed to read merge request discussions for a resolution check",
				zap.Int64("project_id", projectID),
				zap.Int64("merge_request_iid", event.ObjectAttributes.IID),
				zap.Error(err),
			)

			break
		}

		discussions = append(discussions, discussion)
	}

	return normalizeDiscussions(discussions, projectID, receivedAt)
}
