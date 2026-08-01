// Package activity defines the normalized activity model every achievement
// decision in this app is made from.
//
// GitLab exposes the same underlying activity through several differently
// shaped APIs: a project's Events API during backfill, webhook payloads
// during live ingestion, and resource-specific endpoints (pipelines) for
// what neither of those covers. Normalizing all of them into a single Event
// type is what lets historical and live activity share one evaluation code
// path instead of each growing its own copy of the award rules.
//
// This package deliberately knows nothing about GitLab's wire types or
// about the database: producers (backfill, webhook ingestion) translate
// into Event, and a Processor (the achievement engine) consumes it.
package activity

import (
	"context"
	"time"
)

// Kind identifies what a user did, in this app's own vocabulary rather than
// GitLab's. Producers map GitLab's action names, event target types, and
// resource statuses onto these; the engine maps these onto the criteria
// keys the achievement catalog is written against.
type Kind string

const (
	// KindCommit is one or more commits pushed to a project. The Event's
	// Count carries how many, since GitLab reports a push as a single
	// event covering every commit it delivered.
	KindCommit Kind = "commit"
	// KindPush is the push itself, counted once however many commits it
	// carried and whether it added, updated, or deleted a ref. It always
	// accompanies the KindCommit event for the same push, when that push
	// carried commits at all.
	KindPush Kind = "push"
	// KindBranchCreated is a branch created by a push.
	KindBranchCreated Kind = "branch_created"
	// KindTagCreated is a tag created by a push.
	KindTagCreated Kind = "tag_created"
	// KindMergeRequestOpened is a merge request opened by its author.
	KindMergeRequestOpened Kind = "merge_request_opened"
	// KindMergeRequestMerged is a merge request merged (GitLab reports the
	// user who accepted it, not the author).
	KindMergeRequestMerged Kind = "merge_request_merged"
	// KindMergeRequestApproved is a merge request approval.
	KindMergeRequestApproved Kind = "merge_request_approved"
	// KindMergeRequestMergedFast is a merge request merged soon after it was
	// opened. It is always accompanied by a KindMergeRequestMerged event for
	// the same merge request; how soon "soon" is belongs to the producer,
	// since only it can see the two timestamps.
	KindMergeRequestMergedFast Kind = "merge_request_merged_fast"
	// KindMergeRequestClosed is a merge request closed without merging.
	KindMergeRequestClosed Kind = "merge_request_closed"
	// KindDiscussionResolved is a review discussion resolved by a user.
	// Discussions GitLab resolves on its own when a push obsoletes them are
	// not reported as this kind.
	KindDiscussionResolved Kind = "discussion_resolved"
	// KindIssueOpened is an issue opened by its author.
	KindIssueOpened Kind = "issue_opened"
	// KindIssueClosed is an issue closed.
	KindIssueClosed Kind = "issue_closed"
	// KindComment is a note left on an issue, merge request, commit, or
	// snippet. System-generated notes are not reported as this kind.
	KindComment Kind = "comment"
	// KindPipelineRun is a pipeline that ran, whatever its outcome.
	KindPipelineRun Kind = "pipeline_run"
	// KindPipelineSucceeded is a pipeline that finished successfully. It is
	// always accompanied by a KindPipelineRun event for the same pipeline.
	KindPipelineSucceeded Kind = "pipeline_succeeded"
	// KindPipelineFailed is a pipeline that finished in failure. It is
	// always accompanied by a KindPipelineRun event for the same pipeline.
	KindPipelineFailed Kind = "pipeline_failed"
	// KindJobRun is a single CI job that ran, whatever its outcome. Its
	// outcome is deliberately not reported: the pipeline the job belongs to
	// already carries one, and a failing job in a pipeline that passes on
	// retry is not a failure anyone should be scored on.
	KindJobRun Kind = "job_run"
	// KindDeployment is a deployment to an environment, whatever its
	// outcome.
	KindDeployment Kind = "deployment"
	// KindDeploymentSucceeded is a deployment that completed successfully.
	// It is always accompanied by a KindDeployment event for the same
	// deployment.
	KindDeploymentSucceeded Kind = "deployment_succeeded"
	// KindEmojiAwarded is an emoji reaction a user added. Reactions they
	// later removed are not taken back, in keeping with awards themselves.
	KindEmojiAwarded Kind = "emoji_awarded"
	// KindWikiPageCreated is a wiki page created by a user. Edits to an
	// existing page are not reported as this kind.
	KindWikiPageCreated Kind = "wiki_page_created"
)

// Event is one thing one user did, at one point in time.
type Event struct {
	// OccurredAt is when the activity happened on GitLab, not when this app
	// observed it. Backfill replays years-old events, so anything deriving
	// a date (streaks, time-of-day criteria, award timestamps) must use
	// this rather than the wall clock.
	OccurredAt time.Time
	// Kind is what happened.
	Kind Kind
	// DedupKey identifies this activity uniquely and stably across
	// observations, so the same activity seen twice (a resumed backfill
	// re-walking a page, a redelivered webhook, a reconciliation pass over
	// a window webhooks already covered) is counted once. It is derived
	// from GitLab's own identifiers, never from delivery metadata or the
	// observation time.
	//
	// Every producer observing the same underlying activity must derive the
	// same key for it, whatever shape the payload it read arrived in.
	DedupKey string
	// ActorUsername is the acting user's GitLab username, recorded
	// alongside the ID so a user row can be created without a lookup.
	ActorUsername string
	// ActorID is the acting user's GitLab user ID.
	ActorID int64
	// ProjectID is the GitLab project the activity happened in, or 0 for
	// activity not tied to a project.
	ProjectID int64
	// Count is how many occurrences this event represents, for kinds that
	// batch (a push carrying five commits is one event with Count 5).
	// Producers may leave it 0, which consumers treat as 1.
	Count int64
}

// Weight returns the number of occurrences the event should count for,
// normalizing the "unset means one" convention Count documents.
func (e Event) Weight() int64 {
	if e.Count <= 0 {
		return 1
	}

	return e.Count
}

// Processor consumes normalized activity. Implementations are expected to
// be idempotent with respect to Event.DedupKey: processing the same event
// twice must not count it twice.
type Processor interface {
	Process(ctx context.Context, event Event) error
}

// ProcessorFunc adapts a plain function to Processor.
type ProcessorFunc func(ctx context.Context, event Event) error

// Process calls f.
func (f ProcessorFunc) Process(ctx context.Context, event Event) error {
	return f(ctx, event)
}
