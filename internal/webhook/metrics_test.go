package webhook

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"testing"

	"github.com/boxboxjason/gitlab-achievements/internal/metrics"
)

// scrapeDeliveries reads one webhook delivery counter off the metrics page,
// returning 0 for a series that does not exist yet.
func scrapeDeliveries(t *testing.T, eventType, result string) float64 {
	t.Helper()

	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	pattern := regexp.MustCompile(`gitlab_achievements_webhook_deliveries_total\{event_type="` +
		regexp.QuoteMeta(eventType) + `",result="` + regexp.QuoteMeta(result) + `"\} (\S+)`)

	match := pattern.FindStringSubmatch(rec.Body.String())
	if match == nil {
		return 0
	}

	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		t.Fatalf("failed to parse metric value %q: %v", match[1], err)
	}

	return value
}

func TestReceiver_CountsDeliveriesByResult(t *testing.T) {
	receiver := NewReceiver("s3cr3t", &fakeDispatcher{})

	acceptedBefore := scrapeDeliveries(t, "Push Hook", metrics.DeliveryAccepted)
	unauthorizedBefore := scrapeDeliveries(t, "Push Hook", metrics.DeliveryUnauthorized)

	deliver(t, receiver, "s3cr3t", "Push Hook", pushPayload)
	deliver(t, receiver, "wrong", "Push Hook", pushPayload)

	if got := scrapeDeliveries(t, "Push Hook", metrics.DeliveryAccepted) - acceptedBefore; got != 1 {
		t.Errorf("expected one accepted delivery counted, got %v", got)
	}

	if got := scrapeDeliveries(t, "Push Hook", metrics.DeliveryUnauthorized) - unauthorizedBefore; got != 1 {
		t.Errorf("expected one unauthorized delivery counted, got %v", got)
	}
}

func TestReceiver_BoundsTheEventTypeLabel(t *testing.T) {
	receiver := NewReceiver("s3cr3t", &fakeDispatcher{})

	before := scrapeDeliveries(t, "other", metrics.DeliveryUnauthorized)

	deliver(t, receiver, "wrong", "Made Up Hook 4f1c", pushPayload)

	if got := scrapeDeliveries(t, "other", metrics.DeliveryUnauthorized) - before; got != 1 {
		t.Errorf("expected an unknown event type to be counted as other, got delta %v", got)
	}

	if scrapeDeliveries(t, "Made Up Hook 4f1c", metrics.DeliveryUnauthorized) != 0 {
		t.Error("expected a caller-chosen event type never to become a label")
	}
}
