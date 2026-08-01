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
	"io"
	"net/http"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
	"go.uber.org/zap"

	"github.com/boxboxjason/gitlab-achievements/internal/activity"
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
}

// dispatcher is what the receiver hands normalized activity to, satisfied
// by *Queue.
type dispatcher interface {
	Enqueue(ctx context.Context, events []activity.Event) error
}

// Receiver handles GitLab webhook deliveries.
type Receiver struct {
	queue  dispatcher
	secret string
}

// NewReceiver builds a Receiver that authenticates deliveries against
// secret and hands the activity it parses to queue.
func NewReceiver(secret string, queue dispatcher) *Receiver {
	return &Receiver{
		secret: secret,
		queue:  queue,
	}
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
	if !r.authenticate(req) {
		http.Error(resp, "invalid token", http.StatusUnauthorized)

		return
	}

	eventType := gitlab.HookEventType(req)

	payload, err := io.ReadAll(http.MaxBytesReader(resp, req.Body, maxPayloadBytes))
	if err != nil {
		zap.L().Warn("failed to read webhook payload",
			zap.String("event_type", string(eventType)),
			zap.Error(err),
		)
		resp.WriteHeader(http.StatusOK)

		return
	}

	events := r.parse(eventType, payload)
	if len(events) == 0 {
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
		http.Error(resp, "not accepting deliveries right now", http.StatusServiceUnavailable)

		return
	}

	resp.WriteHeader(http.StatusOK)
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
// for.
//
// GitLab delivers event types beyond the ones a hook subscribed to (a
// subgroup event on a group hook, a type added in a newer GitLab), so an
// unrecognized type is expected rather than exceptional. A type this app
// did ask for that won't parse is a different matter: something was lost,
// and it is logged at warn so it shows up without the hook being failed.
func (r *Receiver) parse(eventType gitlab.EventType, payload []byte) []activity.Event {
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

		return nil
	}

	return normalize(parsed, time.Now())
}
