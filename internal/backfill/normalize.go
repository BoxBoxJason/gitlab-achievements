package backfill

import (
	"fmt"
	"strings"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"

	"github.com/boxboxjason/gitlab-achievements/internal/activity"
)

// GitLab's action_name values, as they appear in Events API responses.
//
// These deliberately do not reuse client-go's EventTypeValue constants
// (CreatedEventType, MergedEventType, ...), because those describe the
// other direction: they are the vocabulary of the `action` query parameter
// you filter a request by, and client-go types them onto exactly that
// (ListProjectVisibleEventsOptions.Action is an *EventTypeValue, while the
// response's ActionName is a plain string). The two vocabularies overlap
// but disagree, and the disagreements are the cases that matter here:
//
//	filter "created" ......... response "opened"
//	filter "merged" .......... response "accepted"
//	filter "commented" ....... response "commented on"
//	filter "pushed" .......... response "pushed to" / "pushed new"
//
// Matching a response against the filter constants would silently stop
// recognizing merged merge requests and comments, so the response side gets
// named here instead.
const (
	actionOpened    = "opened"
	actionClosed    = "closed"
	actionAccepted  = "accepted"
	actionMerged    = "merged"
	actionApproved  = "approved"
	actionCommented = "commented on"
)

// push_data field values, describing what a push did to which kind of ref.
// client-go models push_data.action and push_data.ref_type as plain
// strings, with no constants of its own.
const (
	pushActionCreated = "created"
	refTypeBranch     = "branch"
	refTypeTag        = "tag"
)

// normalizeProjectEvent translates one GitLab project event into the
// normalized activities it represents, which may be none (an event this app
// tracks nothing for) or several (a push that both created a branch and
// delivered commits).
//
// Events with no author are dropped: every achievement is awarded to a
// user, so activity GitLab can't attribute to one has nothing to advance.
func normalizeProjectEvent(event *gitlab.ProjectEvent) []activity.Event {
	if event == nil || event.AuthorID == 0 {
		return nil
	}

	base := activity.Event{
		OccurredAt:    parseEventTime(event.CreatedAt),
		DedupKey:      fmt.Sprintf("project_event:%d", event.ID),
		ActorUsername: eventAuthorUsername(event),
		ActorID:       event.AuthorID,
		ProjectID:     event.ProjectID,
	}

	// A push carries no target type; everything else names the resource it
	// acted on.
	if event.TargetType == "" {
		return pushActivities(event, base)
	}

	kind, ok := targetKind(event)
	if !ok {
		return nil
	}

	return []activity.Event{activityFrom(base, kind, 1)}
}

// pushActivities derives the activities a push represents: the push
// itself, the commits it delivered, and the branch or tag it created, if
// any.
func pushActivities(event *gitlab.ProjectEvent, base activity.Event) []activity.Event {
	// An event with no target type and no ref isn't a push at all; GitLab
	// leaves the target type empty for a few other things too.
	if event.PushData.RefType == "" {
		return nil
	}

	activities := []activity.Event{activityFrom(base, activity.KindPush, 1)}

	if event.PushData.Action == pushActionCreated {
		switch event.PushData.RefType {
		case refTypeBranch:
			activities = append(activities, activityFrom(base, activity.KindBranchCreated, 1))
		case refTypeTag:
			activities = append(activities, activityFrom(base, activity.KindTagCreated, 1))
		}
	}

	// GitLab reports a push as one event covering every commit it
	// delivered, so the commit count is carried as the activity's weight
	// rather than expanded into one activity per commit: the Events API
	// doesn't list the individual SHAs, so per-commit activities couldn't
	// be given stable dedup keys anyway.
	if event.PushData.CommitCount > 0 {
		activities = append(activities, activityFrom(base, activity.KindCommit, event.PushData.CommitCount))
	}

	return activities
}

// targetKind maps a non-push event's target type and action onto the
// activity kind it represents, reporting false for the ones this app
// tracks nothing for (milestones, snippets, membership changes, reopenings).
func targetKind(event *gitlab.ProjectEvent) (activity.Kind, bool) {
	action := strings.ToLower(event.ActionName)

	switch normalizeTargetType(event.TargetType) {
	case targetMergeRequest:
		return mergeRequestKind(action)
	case targetIssue:
		return issueKind(action)
	case targetNote:
		// System notes are GitLab narrating its own state changes ("changed
		// the description", "mentioned in commit ..."), not something a
		// user wrote, so they don't count as engagement.
		if action != actionCommented || event.Note.System {
			return "", false
		}

		return activity.KindComment, true
	case targetUnknown:
		return "", false
	}

	return "", false
}

func mergeRequestKind(action string) (activity.Kind, bool) {
	switch action {
	case actionOpened:
		return activity.KindMergeRequestOpened, true
	case actionAccepted, actionMerged:
		return activity.KindMergeRequestMerged, true
	case actionApproved:
		return activity.KindMergeRequestApproved, true
	case actionClosed:
		return activity.KindMergeRequestClosed, true
	}

	return "", false
}

func issueKind(action string) (activity.Kind, bool) {
	switch action {
	case actionOpened:
		return activity.KindIssueOpened, true
	case actionClosed:
		return activity.KindIssueClosed, true
	}

	return "", false
}

// targetType is this package's canonical spelling of an event's target,
// insulating the mapping from GitLab's inconsistent casing.
type targetType string

const (
	targetMergeRequest targetType = "mergerequest"
	targetIssue        targetType = "issue"
	targetNote         targetType = "note"
	targetUnknown      targetType = ""
)

// normalizeTargetType folds GitLab's target type spellings onto a single
// value each.
//
// Target types have the same request/response split the action names do:
// client-go's EventTargetTypeValue constants spell them snake_case
// ("merge_request") because that is what the target_type filter takes,
// while responses carry Go-ish type names ("MergeRequest",
// "DiscussionNote"), and newer instances report an issue as a work item,
// which has no constant on either side. Folding case and separators means
// this matches whichever spelling an instance uses rather than betting on
// one.
func normalizeTargetType(raw string) targetType {
	folded := strings.ToLower(strings.ReplaceAll(raw, "_", ""))

	switch folded {
	case "mergerequest":
		return targetMergeRequest
	case "issue", "workitem":
		return targetIssue
	case "note", "discussionnote", "diffnote":
		return targetNote
	}

	return targetUnknown
}

// normalizePipeline translates one pipeline into the activities it
// represents: the run itself, plus its outcome once it has one.
//
// Pipelines with no triggering user (schedules, API triggers with a
// detached token) are dropped, for the same reason authorless events are.
func normalizePipeline(pipeline *gitlab.Pipeline) []activity.Event {
	if pipeline == nil || pipeline.User == nil || pipeline.User.ID == 0 {
		return nil
	}

	base := activity.Event{
		OccurredAt:    timeOrZero(pipeline.CreatedAt),
		DedupKey:      fmt.Sprintf("pipeline:%d", pipeline.ID),
		ActorUsername: pipeline.User.Username,
		ActorID:       pipeline.User.ID,
		ProjectID:     pipeline.ProjectID,
	}

	activities := []activity.Event{activityFrom(base, activity.KindPipelineRun, 1)}

	// Unlike the Events API's action names, pipeline statuses are one
	// vocabulary in both directions, so client-go's own constants apply:
	// the same values filter a request (ListProjectPipelinesOptions.Status
	// is a *BuildStateValue) and come back on the response. Every other
	// status (running, canceled, skipped, ...) still counts as a run, just
	// not as an outcome.
	switch gitlab.BuildStateValue(pipeline.Status) {
	case gitlab.Success:
		activities = append(activities, activityFrom(base, activity.KindPipelineSucceeded, 1))
	case gitlab.Failed:
		activities = append(activities, activityFrom(base, activity.KindPipelineFailed, 1))
	case gitlab.Created, gitlab.WaitingForResource, gitlab.Preparing, gitlab.Pending,
		gitlab.Running, gitlab.Canceled, gitlab.Skipped, gitlab.Manual, gitlab.Scheduled:
	}

	return activities
}

// activityFrom specializes base to one kind, scoping its dedup key to the
// kind as well as the source record so the several activities one GitLab
// record yields stay individually deduplicable.
func activityFrom(base activity.Event, kind activity.Kind, count int64) activity.Event {
	base.Kind = kind
	base.Count = count
	base.DedupKey = base.DedupKey + ":" + string(kind)

	return base
}

// eventAuthorUsername picks whichever spelling of the author's username the
// event carries; GitLab populates both, but only one on some versions.
func eventAuthorUsername(event *gitlab.ProjectEvent) string {
	if event.AuthorUsername != "" {
		return event.AuthorUsername
	}

	return event.Author.Username
}

// parseEventTime reads an event's timestamp, which the Events API returns
// as a string rather than a parsed time. An unparseable value yields the
// zero time rather than dropping the activity: when the event happened is
// worth less than the fact that it did.
//
// The offset GitLab reported is kept rather than normalized to UTC. The
// day and hour an activity falls on decide the day-based criteria
// (streaks, night owl, early bird), and those should read the clock the
// instance reports, not one shifted out from under it.
func parseEventTime(raw string) time.Time {
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}

	return parsed
}

// timeOrZero dereferences an optional timestamp, keeping the offset it
// carries for the same reason parseEventTime does.
func timeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}

	return *t
}
