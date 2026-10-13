// Package metrics exposes this app's operational metrics in the Prometheus
// text format.
//
// Everything worth alerting on in production lives here: whether webhook
// deliveries are being accepted, whether the queue behind them is keeping
// up or dropping activity, whether awards are reaching GitLab, whether the
// background sweeps are still succeeding, and how GitLab itself is
// answering.
//
// The metrics are package-level, the same way zap.L() is: they are written
// from every layer of the app, and threading a registry through each
// constructor would touch every signature for no behavior of its own. They
// are registered on a registry of this package's own rather than on
// Prometheus's global one, so nothing a dependency registers globally ends
// up on the endpoint by accident.
//
// Label values are always drawn from small, fixed sets. Nothing a caller
// controls (a path, a username, a project ID) becomes a label, so the
// number of series stays bounded however large the instance is or however
// hostile its traffic.
package metrics

import (
	"context"
	"net/http"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// namespace prefixes every metric this app exports.
const namespace = "gitlab_achievements"

// Webhook delivery results, as reported on webhookDeliveries.
const (
	// DeliveryAccepted is a delivery whose activity was queued.
	DeliveryAccepted = "accepted"
	// DeliveryIgnored is a delivery that carried nothing this app counts:
	// an event type it does not track, or one with no countable activity.
	DeliveryIgnored = "ignored"
	// DeliveryUnparsable is a delivery that could not be read or parsed.
	DeliveryUnparsable = "unparsable"
	// DeliveryUnauthorized is a delivery with a missing or wrong token.
	DeliveryUnauthorized = "unauthorized"
	// DeliveryRejected is a delivery refused with a 503 because the queue
	// was full or shutting down, which GitLab will count against the hook.
	DeliveryRejected = "rejected"
)

// Queue outcomes, as reported on queueActivities.
const (
	// QueueAccepted is an activity taken into the queue.
	QueueAccepted = "accepted"
	// QueueRejected is an activity refused because the queue was full or
	// shutting down.
	QueueRejected = "rejected"
	// QueueProcessed is an activity the engine evaluated.
	QueueProcessed = "processed"
	// QueueFailed is an activity dropped after every retry failed.
	QueueFailed = "failed"
)

// Subsystems and labels shared by several metrics.
const (
	queueSubsystem = "queue"
	jobSubsystem   = "job"
	resultLabel    = "result"
)

// Job results, as reported on jobRuns.
const (
	jobSuccess = "success"
	jobFailure = "failure"
)

//nolint:gochecknoglobals // package-level metrics, see the package comment
var (
	registry = prometheus.NewRegistry()

	buildInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "build_info",
		Help:      "Always 1, labelled with the running build's version.",
	}, []string{"version", "goversion"})

	ready = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "ready",
		Help:      "1 once bootstrap has finished and the server is serving, 0 before.",
	})

	httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "http",
		Name:      "requests_total",
		Help:      "HTTP requests served, by route, method, and status code.",
	}, []string{"route", "method", "code"})

	httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: "http",
		Name:      "request_duration_seconds",
		Help:      "Time to serve an HTTP request, by route.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"route"})

	webhookDeliveries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "webhook",
		Name:      "deliveries_total",
		Help:      "GitLab webhook deliveries received, by event type and result.",
	}, []string{"event_type", resultLabel})

	queueActivities = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: queueSubsystem,
		Name:      "activities_total",
		Help:      "Activities through the webhook queue, by outcome: accepted, rejected, processed, or failed (dropped after every retry).",
	}, []string{"outcome"})

	queueSource atomic.Pointer[queueGauges]

	queuePending = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: queueSubsystem,
		Name:      "pending",
		Help:      "Activities accepted and waiting to be evaluated.",
	}, func() float64 {
		source := queueSource.Load()
		if source == nil {
			return 0
		}

		return float64(source.pending())
	})

	queueCapacity = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: queueSubsystem,
		Name:      "capacity",
		Help:      "Activities the queue can hold before deliveries are refused.",
	}, func() float64 {
		source := queueSource.Load()
		if source == nil {
			return 0
		}

		return float64(source.capacity)
	})

	queueEvaluation = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: queueSubsystem,
		Name:      "evaluation_duration_seconds",
		Help:      "Time to evaluate one queued activity, retries included.",
		Buckets:   []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	})

	engineEvents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "engine",
		Name:      "events_total",
		Help:      "Activities the achievement engine evaluated, by outcome: counted, duplicate, untracked, or excluded.",
	}, []string{"outcome"})

	engineTiers = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "engine",
		Name:      "tiers_awarded_total",
		Help:      "Achievement tiers newly earned.",
	})

	awardDeliveries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "awards",
		Name:      "deliveries_total",
		Help:      "Award changes pushed to GitLab, by result: confirmed, adopted, superseded, or failed.",
	}, []string{resultLabel})

	jobRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: jobSubsystem,
		Name:      "runs_total",
		Help:      "Background job runs, by job and result.",
	}, []string{jobSubsystem, resultLabel})

	jobDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: jobSubsystem,
		Name:      "duration_seconds",
		Help:      "Time a background job run took, by job.",
		Buckets:   []float64{1, 5, 15, 30, 60, 300, 900, 1800, 3600, 7200, 21600},
	}, []string{jobSubsystem})

	jobLastSuccess = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: jobSubsystem,
		Name:      "last_success_timestamp_seconds",
		Help:      "Unix time the job last finished successfully, or was scheduled if it has not yet. Alert on time() minus this.",
	}, []string{jobSubsystem})

	gitlabRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "gitlab",
		Name:      "requests_total",
		Help:      "Requests made to GitLab, by client (read, write, verify), method, and status code; code is \"error\" when no response came back.",
	}, []string{"client", "method", "code"})

	gitlabDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: "gitlab",
		Name:      "request_duration_seconds",
		Help:      "Time GitLab took to answer one request, by client.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"client"})
)

//nolint:gochecknoinits // registration has to happen once, before any metric is written
func init() {
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		buildInfo,
		ready,
		httpRequests,
		httpDuration,
		webhookDeliveries,
		queueActivities,
		queuePending,
		queueCapacity,
		queueEvaluation,
		engineEvents,
		engineTiers,
		awardDeliveries,
		jobRuns,
		jobDuration,
		jobLastSuccess,
		gitlabRequests,
		gitlabDuration,
	)
}

// Handler serves every registered metric in the Prometheus text format.
func Handler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{Registry: registry})
}

// SetBuildInfo records the running build's version.
func SetBuildInfo(version string) {
	buildInfo.WithLabelValues(version, runtime.Version()).Set(1)
}

// SetReady records whether the server has finished bootstrap and is
// serving.
func SetReady(isReady bool) {
	if isReady {
		ready.Set(1)

		return
	}

	ready.Set(0)
}

// WebhookDelivery counts one webhook delivery.
//
// eventType should be one the app subscribes to, or "other": the header it
// comes from is set by whoever sent the request, so an unchecked value
// would let any caller mint new series.
func WebhookDelivery(eventType, result string) {
	webhookDeliveries.WithLabelValues(eventType, result).Inc()
}

// TrackQueue makes the queue gauges read from the given queue at scrape
// time: how many activities are waiting, and how many it can hold.
//
// A later call replaces the earlier source rather than registering a second
// pair of gauges, so building another queue (as tests do) cannot collide
// with the first one's registration.
func TrackQueue(pending func() int, capacity int) {
	queueSource.Store(&queueGauges{pending: pending, capacity: capacity})
}

// queueGauges is the queue the pending and capacity gauges read from.
type queueGauges struct {
	pending  func() int
	capacity int
}

// QueueActivities counts n activities reaching outcome, one of the Queue
// constants, in the webhook queue.
func QueueActivities(outcome string, n int) {
	queueActivities.WithLabelValues(outcome).Add(float64(n))
}

// QueueEvaluation records how long one activity took to evaluate.
func QueueEvaluation(elapsed time.Duration) {
	queueEvaluation.Observe(elapsed.Seconds())
}

// EngineEvent counts one activity evaluated by the engine, by outcome:
// "counted", "duplicate", or "untracked".
func EngineEvent(outcome string) {
	engineEvents.WithLabelValues(outcome).Inc()
}

// TiersAwarded counts tiers newly earned.
func TiersAwarded(n int) {
	engineTiers.Add(float64(n))
}

// AwardDeliveries counts the outcome of one award reconciliation pass.
func AwardDeliveries(confirmed, adopted, superseded, failed int) {
	awardDeliveries.WithLabelValues("confirmed").Add(float64(confirmed))
	awardDeliveries.WithLabelValues("adopted").Add(float64(adopted))
	awardDeliveries.WithLabelValues("superseded").Add(float64(superseded))
	awardDeliveries.WithLabelValues("failed").Add(float64(failed))
}

// Job wraps task so every run is counted and timed under name, and its last
// success recorded.
//
// The last-success gauge is seeded with the time the job is wrapped, which
// is when it is scheduled. Without that, a job whose first in-process run is
// a full interval away (the hourly sweeps, whose initial pass bootstrap
// makes) has no series at all until it first succeeds, so a staleness alert
// on it could never fire for a process that restarted into a failing state.
//
// A run that fails because ctx was cancelled is the process shutting down,
// not the job failing, and is not counted: otherwise every rolling restart
// that interrupts a long backfill would read as a failure.
func Job(name string, task func(context.Context) error) func(context.Context) error {
	jobLastSuccess.WithLabelValues(name).SetToCurrentTime()

	return func(ctx context.Context) error {
		start := time.Now()

		err := task(ctx)
		if err != nil && ctx.Err() != nil {
			return err
		}

		jobDuration.WithLabelValues(name).Observe(time.Since(start).Seconds())

		if err != nil {
			jobRuns.WithLabelValues(name, jobFailure).Inc()

			return err
		}

		jobRuns.WithLabelValues(name, jobSuccess).Inc()
		jobLastSuccess.WithLabelValues(name).SetToCurrentTime()

		return nil
	}
}

// statusCode renders an HTTP status as a label value.
func statusCode(code int) string {
	return strconv.Itoa(code)
}
