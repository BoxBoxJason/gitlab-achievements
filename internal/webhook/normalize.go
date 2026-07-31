package webhook

import (
	"fmt"
	"strings"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"

	"github.com/boxboxjason/gitlab-achievements/internal/activity"
)

// Merge request and issue webhook actions, as they appear in a payload's
// object_attributes.action.
//
// These are a third vocabulary alongside the two backfill's normalizer
// documents: webhook actions are spelled in the imperative ("open",
// "close", "merge") where the Events API reports them in the past tense
// ("opened", "closed", "accepted"), so the two cannot share constants.
const (
	actionOpen = "open"
	// actionClose is a merge request or issue closed. Reopening and closing
	// again repeats it, which the dedup key deliberately collapses.
	actionClose = "close"
	actionMerge = "merge"
	// actionApproval is one user approving a merge request, and
	// actionApproved the merge request becoming fully approved. GitLab sends
	// both for the approval that satisfies the last rule, so the two are
	// mapped onto one activity with one dedup key rather than counted twice.
	actionApproval = "approval"
	actionApproved = "approved"
)

// Emoji and wiki page actions, as they appear in a payload's
// object_attributes.action.
//
// Unlike deployment and pipeline statuses, which client-go models as typed
// enums this package switches on, these two are plain strings on both the
// payload struct and the API, with no constants anywhere in client-go to
// borrow.
const (
	// emojiActionAward is a reaction being added. The paired "revoke" is
	// deliberately not tracked: an award, once earned, is never taken back,
	// so a criteria that could fall would be meaningless.
	emojiActionAward = "award"
	// wikiActionCreate is a wiki page being created. The paired "update" and
	// "delete" are not tracked, for want of a stable identifier: a wiki
	// payload carries no revision ID, so two edits of one page and a
	// redelivery of the first edit are indistinguishable.
	wikiActionCreate = "create"
)

// fastMergeWindow is how soon after being opened a merge request has to be
// merged to count as fast. An hour is short enough that hitting it means
// the review was quick rather than that the day was, and long enough not to
// be an accident of how fast someone can click.
const fastMergeWindow = time.Hour

// nullSHA is what GitLab reports as a push's "before" when the ref did not
// exist yet, which is how a branch or tag creation is recognized.
const nullSHA = "0000000000000000000000000000000000000000"

// branchRefPrefix scopes the branch-creation check to branch pushes; tag
// creations arrive as their own event type.
const branchRefPrefix = "refs/heads/"

// normalize translates one parsed webhook payload into the normalized
// activities it represents, which may be none (an event this app tracks
// nothing for) or several (a push that both created a branch and delivered
// commits).
//
// receivedAt dates the activities whose payload carries no usable timestamp
// of its own, which is every push: GitLab's push payloads report the
// commits' authoring times but not when the push happened, and authoring
// time is both attacker-controlled and frequently much older than the push.
func normalize(event any, receivedAt time.Time) []activity.Event {
	switch payload := event.(type) {
	case *gitlab.PushEvent:
		return normalizePush(payload, receivedAt)
	case *gitlab.TagEvent:
		return normalizeTagPush(payload, receivedAt)
	case *gitlab.MergeEvent:
		return normalizeMergeRequest(payload, receivedAt)
	case *gitlab.IssueEvent:
		return normalizeIssue(payload, receivedAt)
	case *gitlab.PipelineEvent:
		return normalizePipeline(payload, receivedAt)
	case *gitlab.JobEvent:
		return normalizeJob(payload, receivedAt)
	case *gitlab.DeploymentEvent:
		return normalizeDeployment(payload, receivedAt)
	case *gitlab.EmojiEvent:
		return normalizeEmoji(payload, receivedAt)
	case *gitlab.WikiPageEvent:
		return normalizeWikiPage(payload, receivedAt)
	}

	return normalizeComment(event, receivedAt)
}

// normalizeComment handles the four event types GitLab splits comments
// into, kept apart from normalize so neither type switch grows past the
// point of being readable.
func normalizeComment(event any, receivedAt time.Time) []activity.Event {
	switch payload := event.(type) {
	case *gitlab.CommitCommentEvent:
		if payload == nil {
			return nil
		}

		attrs := payload.ObjectAttributes

		return normalizeNote(note{
			id: attrs.ID, authorID: attrs.AuthorID, username: userName(payload.User),
			projectID: payload.ProjectID, createdAt: attrs.CreatedAt, system: attrs.System,
		}, receivedAt)
	case *gitlab.IssueCommentEvent:
		if payload == nil {
			return nil
		}

		attrs := payload.ObjectAttributes

		return normalizeNote(note{
			id: attrs.ID, authorID: attrs.AuthorID, username: userName(payload.User),
			projectID: payload.ProjectID, createdAt: attrs.CreatedAt, system: attrs.System,
		}, receivedAt)
	case *gitlab.MergeCommentEvent:
		if payload == nil {
			return nil
		}

		attrs := payload.ObjectAttributes

		activities := normalizeNote(note{
			id: attrs.ID, authorID: attrs.AuthorID, username: eventUserName(payload.User),
			projectID: payload.ProjectID, createdAt: attrs.CreatedAt, system: attrs.System,
		}, receivedAt)

		// Merge request notes are the only ones that can be resolved, and
		// the only comment payload carrying the resolution fields.
		return append(activities, normalizeResolution(payload, receivedAt)...)
	case *gitlab.SnippetCommentEvent:
		if payload == nil {
			return nil
		}

		attrs := payload.ObjectAttributes

		return normalizeNote(note{
			id: attrs.ID, authorID: attrs.AuthorID, username: eventUserName(payload.User),
			projectID: payload.ProjectID, createdAt: attrs.CreatedAt, system: attrs.System,
		}, receivedAt)
	}

	return nil
}

// normalizePush derives the activities a branch push represents: the push
// itself, the commits it delivered, and the branch it created, if any.
//
// Pushes GitLab can't attribute to a user are dropped, as every achievement
// is awarded to one.
//
// A branch deletion counts as a push, carrying no commits and creating
// nothing. It is one (`git push --delete`), and counting it is what the
// historical backfill does with the same event, which is the property that
// matters: the two paths have to agree on what a push is, or a user's total
// would depend on which of them happened to observe it.
func normalizePush(event *gitlab.PushEvent, receivedAt time.Time) []activity.Event {
	if event == nil || event.UserID == 0 {
		return nil
	}

	base := activity.Event{
		OccurredAt: receivedAt,
		// The ref is part of the key because creating two branches at the
		// same commit produces two pushes that are otherwise identical.
		//
		// Keying on the resulting SHA does collapse a few genuinely
		// distinct pushes: deleting the same branch twice, or force-pushing
		// a ref back to a commit it already pointed at. Both are rare, and
		// undercounting them is the right way to be wrong here, since the
		// same key is what makes an ordinary redelivery count once.
		DedupKey:      fmt.Sprintf("push:%d:%s:%s", event.ProjectID, event.Ref, event.After),
		ActorUsername: event.UserUsername,
		ActorID:       event.UserID,
		ProjectID:     event.ProjectID,
	}

	activities := []activity.Event{activityFrom(base, activity.KindPush, 1)}

	if event.Before == nullSHA && event.After != nullSHA && strings.HasPrefix(event.Ref, branchRefPrefix) {
		activities = append(activities, activityFrom(base, activity.KindBranchCreated, 1))
	}

	// The commit count is carried as the activity's weight rather than
	// expanded into one activity per commit: GitLab truncates the commits
	// array on large pushes but always reports the true total here, so
	// per-commit activities would silently undercount them.
	if event.TotalCommitsCount > 0 {
		activities = append(activities, activityFrom(base, activity.KindCommit, event.TotalCommitsCount))
	}

	return activities
}

// normalizeTagPush derives the activities a tag push represents: the push
// itself and the tag it created.
//
// Unlike a branch push this deliberately counts no commits. A tag names
// commits that some earlier branch push already delivered and was already
// counted for, so counting them again here would inflate every tagged
// release by its whole history.
func normalizeTagPush(event *gitlab.TagEvent, receivedAt time.Time) []activity.Event {
	if event == nil || event.UserID == 0 {
		return nil
	}

	base := activity.Event{
		OccurredAt:    receivedAt,
		DedupKey:      fmt.Sprintf("tag_push:%d:%s:%s", event.ProjectID, event.Ref, event.After),
		ActorUsername: event.UserUsername,
		ActorID:       event.UserID,
		ProjectID:     event.ProjectID,
	}

	activities := []activity.Event{activityFrom(base, activity.KindPush, 1)}

	if event.Before == nullSHA && event.After != nullSHA {
		activities = append(activities, activityFrom(base, activity.KindTagCreated, 1))
	}

	return activities
}

// normalizeMergeRequest maps a merge request event's action onto the
// activity it represents, for the actions the catalog tracks.
//
// The acting user is taken from the event's top-level user rather than the
// merge request's author: GitLab reports who performed the action, and a
// merge request is routinely merged, closed, or approved by someone other
// than the person who opened it.
func normalizeMergeRequest(event *gitlab.MergeEvent, receivedAt time.Time) []activity.Event {
	if event == nil || event.User == nil || event.User.ID == 0 {
		return nil
	}

	attrs := event.ObjectAttributes

	kind, suffix, ok := mergeRequestKind(attrs.Action)
	if !ok {
		return nil
	}

	// An approval is scoped to the approver, so a second reviewer approving
	// the same merge request is counted separately, while GitLab's paired
	// "approval" and "approved" deliveries for one person collapse into one.
	key := fmt.Sprintf("merge_request:%d:%s", attrs.ID, suffix)
	if kind == activity.KindMergeRequestApproved {
		key = fmt.Sprintf("merge_request:%d:%s:%d", attrs.ID, suffix, event.User.ID)
	}

	occurredAt := attrs.UpdatedAt
	if kind == activity.KindMergeRequestOpened {
		occurredAt = attrs.CreatedAt
	}

	base := activity.Event{
		OccurredAt:    parseEventTime(occurredAt, receivedAt),
		Kind:          kind,
		DedupKey:      key,
		ActorUsername: event.User.Username,
		ActorID:       event.User.ID,
		ProjectID:     attrs.TargetProjectID,
		Count:         1,
	}

	activities := []activity.Event{base}

	// The fast merge is credited to whoever merged rather than to the
	// author, matching the merge itself: GitLab reports the acting user, and
	// how quickly a merge request cleared review is the reviewer's doing at
	// least as much as the author's.
	if kind == activity.KindMergeRequestMerged && mergedFast(attrs.CreatedAt, attrs.UpdatedAt) {
		fast := base
		fast.Kind = activity.KindMergeRequestMergedFast
		fast.DedupKey = fmt.Sprintf("merge_request:%d:merged_fast", attrs.ID)

		activities = append(activities, fast)
	}

	return activities
}

// mergedFast reports whether a merge request was merged within
// fastMergeWindow of being opened.
//
// Both timestamps have to parse for this to be true, which is why it reads
// them itself rather than taking the times the caller already derived.
// parseEventTime substitutes the delivery time for a spelling it doesn't
// recognize, and two substitutions are identical, so a GitLab reporting
// timestamps in some form this app doesn't know would otherwise have every
// one of its merges scored as instantaneous.
func mergedFast(createdAt, mergedAt string) bool {
	created, createdKnown := parseTimestamp(createdAt)
	if !createdKnown {
		return false
	}

	merged, mergedKnown := parseTimestamp(mergedAt)
	if !mergedKnown {
		return false
	}

	elapsed := merged.Sub(created)

	return elapsed >= 0 && elapsed <= fastMergeWindow
}

// mergeRequestKind maps a merge request action onto the activity kind it
// represents, along with the stable name used in its dedup key. The dedup
// suffix is named separately from the action so that the two actions
// meaning "approved" share one key.
func mergeRequestKind(action string) (activity.Kind, string, bool) {
	switch action {
	case actionOpen:
		return activity.KindMergeRequestOpened, "opened", true
	case actionMerge:
		return activity.KindMergeRequestMerged, "merged", true
	case actionClose:
		return activity.KindMergeRequestClosed, "closed", true
	case actionApproval, actionApproved:
		return activity.KindMergeRequestApproved, "approved", true
	}

	return "", "", false
}

// normalizeIssue maps an issue event's action onto the activity it
// represents.
func normalizeIssue(event *gitlab.IssueEvent, receivedAt time.Time) []activity.Event {
	if event == nil || event.User == nil || event.User.ID == 0 {
		return nil
	}

	attrs := event.ObjectAttributes

	var (
		kind   activity.Kind
		suffix string
	)

	switch attrs.Action {
	case actionOpen:
		kind, suffix = activity.KindIssueOpened, "opened"
	case actionClose:
		kind, suffix = activity.KindIssueClosed, "closed"
	default:
		return nil
	}

	occurredAt := attrs.UpdatedAt
	if kind == activity.KindIssueOpened {
		occurredAt = attrs.CreatedAt
	}

	return []activity.Event{{
		OccurredAt:    parseEventTime(occurredAt, receivedAt),
		Kind:          kind,
		DedupKey:      fmt.Sprintf("issue:%d:%s", attrs.ID, suffix),
		ActorUsername: event.User.Username,
		ActorID:       event.User.ID,
		ProjectID:     attrs.ProjectID,
		Count:         1,
	}}
}

// normalizePipeline derives the activities a pipeline represents: the run
// itself, plus its outcome once it has one.
//
// Pipeline events arrive on every status transition, all carrying the same
// pipeline ID, so the run is counted once however many transitions were
// delivered. The dedup key deliberately matches the one the historical
// backfill derives for the same pipeline, so a pipeline seen by both paths
// is counted once.
//
// Outcomes are keyed per outcome rather than per pipeline, which means a
// pipeline that reaches more than one terminal state over its life counts
// for each. That is a retried pipeline: retrying keeps the pipeline's ID
// and moves it back out of its terminal state, so one that failed and was
// retried into success counts as both a failure and a success. Keying the
// outcome per pipeline instead would make it whichever terminal state
// happened to arrive first, which is a worse answer: the failure did
// happen, and so did the eventual success.
func normalizePipeline(event *gitlab.PipelineEvent, receivedAt time.Time) []activity.Event {
	if event == nil || event.User == nil || event.User.ID == 0 {
		return nil
	}

	attrs := event.ObjectAttributes

	base := activity.Event{
		OccurredAt:    parseEventTime(attrs.CreatedAt, receivedAt),
		DedupKey:      fmt.Sprintf("pipeline:%d", attrs.ID),
		ActorUsername: event.User.Username,
		ActorID:       event.User.ID,
		ProjectID:     event.Project.ID,
	}

	activities := []activity.Event{activityFrom(base, activity.KindPipelineRun, 1)}

	switch gitlab.BuildStateValue(attrs.Status) {
	case gitlab.Success:
		activities = append(activities, activityFrom(base, activity.KindPipelineSucceeded, 1))
	case gitlab.Failed:
		activities = append(activities, activityFrom(base, activity.KindPipelineFailed, 1))
	case gitlab.Created, gitlab.WaitingForResource, gitlab.Preparing, gitlab.Pending,
		gitlab.Running, gitlab.Canceled, gitlab.Skipped, gitlab.Manual, gitlab.Scheduled:
	}

	return activities
}

// note is the common shape of the four comment event types, which GitLab
// splits by what was commented on but which carry the same note fields.
// They are nominally distinct types, and two of them type the user as
// *gitlab.User where the others use *gitlab.EventUser, so there is no
// interface to reach them through.
type note struct {
	username  string
	createdAt string
	id        int64
	authorID  int64
	projectID int64
	system    bool
}

// normalizeNote counts a user-written comment, whatever it was left on.
//
// System notes are GitLab narrating its own state changes ("changed the
// description", "mentioned in commit ..."), not something a user wrote, so
// they don't count as engagement.
func normalizeNote(comment note, receivedAt time.Time) []activity.Event {
	if comment.system || comment.authorID == 0 {
		return nil
	}

	return []activity.Event{{
		OccurredAt:    parseEventTime(comment.createdAt, receivedAt),
		Kind:          activity.KindComment,
		DedupKey:      fmt.Sprintf("note:%d", comment.id),
		ActorUsername: comment.username,
		ActorID:       comment.authorID,
		ProjectID:     comment.projectID,
		Count:         1,
	}}
}

// normalizeResolution counts a review discussion a user resolved.
//
// It is keyed on the discussion rather than the note, so resolving a thread
// counts once however many notes it holds, and so the several deliveries
// GitLab sends as a thread is edited after resolution collapse into one.
//
// Discussions GitLab resolved on its own don't count: pushing a commit that
// obsoletes a thread resolves it without anyone deciding anything, which is
// what ResolvedByPush marks.
func normalizeResolution(event *gitlab.MergeCommentEvent, receivedAt time.Time) []activity.Event {
	attrs := event.ObjectAttributes

	if attrs.ResolvedAt == "" || attrs.ResolvedByID == 0 || attrs.ResolvedByPush {
		return nil
	}

	// A note that can be resolved always belongs to a discussion, but the
	// note's own ID identifies the same thread if an instance ever omits it.
	key := fmt.Sprintf("discussion:%s:resolved", attrs.DiscussionID)
	if attrs.DiscussionID == "" {
		key = fmt.Sprintf("discussion:note:%d:resolved", attrs.ID)
	}

	return []activity.Event{{
		OccurredAt:    parseEventTime(attrs.ResolvedAt, receivedAt),
		Kind:          activity.KindDiscussionResolved,
		DedupKey:      key,
		ActorUsername: resolverUsername(event),
		ActorID:       attrs.ResolvedByID,
		ProjectID:     attrs.ProjectID,
		Count:         1,
	}}
}

// resolverUsername names the resolver, which GitLab reports only as an ID.
//
// The delivery's acting user is that person in the ordinary case, because
// resolving the thread is what produced the delivery. When the two disagree
// the delivery is about something else happening to a thread resolved
// earlier, and the payload names no username for the resolver at all, so
// this reports none: the engine keys the user on their GitLab ID and fills
// the name in the next time they do something.
func resolverUsername(event *gitlab.MergeCommentEvent) string {
	if event.User == nil || event.User.ID != event.ObjectAttributes.ResolvedByID {
		return ""
	}

	return event.User.Username
}

// normalizeJob counts one CI job that ran.
//
// Job events arrive on every status transition, all carrying the same build
// ID, so the job is counted once however many transitions were delivered.
// The outcome is deliberately not derived: the pipeline the job belongs to
// already reports one, and counting a job's failure separately would score
// people on flaky infrastructure and on the failing halves of retries.
func normalizeJob(event *gitlab.JobEvent, receivedAt time.Time) []activity.Event {
	if event == nil || event.User == nil || event.User.ID == 0 {
		return nil
	}

	return []activity.Event{{
		OccurredAt:    parseEventTime(jobCreatedAt(event), receivedAt),
		Kind:          activity.KindJobRun,
		DedupKey:      fmt.Sprintf("job:%d", event.BuildID),
		ActorUsername: event.User.Username,
		ActorID:       event.User.ID,
		ProjectID:     event.ProjectID,
		Count:         1,
	}}
}

// jobCreatedAt prefers the RFC 3339 spelling of a job's creation time,
// which GitLab added alongside the legacy space-separated one rather than
// in place of it.
func jobCreatedAt(event *gitlab.JobEvent) string {
	if event.BuildCreatedAtISO != "" {
		return event.BuildCreatedAtISO
	}

	return event.BuildCreatedAt
}

// normalizeDeployment derives the activities a deployment represents: the
// deployment itself, plus its outcome once it succeeded.
//
// Like pipelines, deployment events arrive on every status transition
// carrying one deployment ID, so the deployment is counted once. Unlike
// pipelines, the failures are not counted: a pipeline that fails is usually
// a test catching something, which is the system working, whereas a
// deployment that fails is nobody's achievement.
func normalizeDeployment(event *gitlab.DeploymentEvent, receivedAt time.Time) []activity.Event {
	if event == nil || event.User == nil || event.User.ID == 0 {
		return nil
	}

	base := activity.Event{
		OccurredAt:    parseEventTime(event.StatusChangedAt, receivedAt),
		DedupKey:      fmt.Sprintf("deployment:%d", event.DeploymentID),
		ActorUsername: event.User.Username,
		ActorID:       event.User.ID,
		ProjectID:     event.Project.ID,
	}

	activities := []activity.Event{activityFrom(base, activity.KindDeployment, 1)}

	// Deployment statuses are one vocabulary in both directions, the same
	// values filtering a request (ListProjectDeploymentsOptions.Status is a
	// *DeploymentStatusValue) and coming back on the payload, so client-go's
	// own constants apply. Every other status still counts as a deployment,
	// just not as a successful one.
	switch gitlab.DeploymentStatusValue(event.Status) {
	case gitlab.DeploymentStatusSuccess:
		activities = append(activities, activityFrom(base, activity.KindDeploymentSucceeded, 1))
	case gitlab.DeploymentStatusCreated, gitlab.DeploymentStatusRunning,
		gitlab.DeploymentStatusFailed, gitlab.DeploymentStatusCanceled:
	}

	return activities
}

// normalizeEmoji counts a reaction a user added to an issue, merge request,
// comment, snippet, or commit.
//
// The acting user is read from the payload's top-level user rather than
// from the award's own user_id. They are the same person, since a reaction
// is only ever added by whoever it belongs to, but only the top-level one
// carries a username.
func normalizeEmoji(event *gitlab.EmojiEvent, receivedAt time.Time) []activity.Event {
	if event == nil || event.User.ID == 0 {
		return nil
	}

	attrs := event.ObjectAttributes

	if attrs.Action != emojiActionAward {
		return nil
	}

	return []activity.Event{{
		OccurredAt:    parseEventTime(attrs.CreatedAt, receivedAt),
		Kind:          activity.KindEmojiAwarded,
		DedupKey:      fmt.Sprintf("emoji:%d", attrs.ID),
		ActorUsername: event.User.Username,
		ActorID:       event.User.ID,
		ProjectID:     event.ProjectID,
		Count:         1,
	}}
}

// normalizeWikiPage counts a wiki page a user created.
//
// Only creations count. GitLab's wiki payload identifies a page by its slug
// and carries no revision ID, so an edit and a redelivery of that edit are
// the same payload, and counting edits would either count one edit many
// times or a hundred edits once. A creation has no such ambiguity: a slug
// can only be created once.
//
// The activity carries no project ID because the payload carries none. It
// names the project by path, which the dedup key uses instead so that two
// projects with a page of the same name stay distinct.
func normalizeWikiPage(event *gitlab.WikiPageEvent, receivedAt time.Time) []activity.Event {
	if event == nil || event.User == nil || event.User.ID == 0 {
		return nil
	}

	attrs := event.ObjectAttributes

	if attrs.Action != wikiActionCreate || attrs.Slug == "" {
		return nil
	}

	return []activity.Event{{
		OccurredAt:    receivedAt,
		Kind:          activity.KindWikiPageCreated,
		DedupKey:      fmt.Sprintf("wiki_page:%s:%s", event.Project.PathWithNamespace, attrs.Slug),
		ActorUsername: event.User.Username,
		ActorID:       event.User.ID,
		Count:         1,
	}}
}

func userName(user *gitlab.User) string {
	if user == nil {
		return ""
	}

	return user.Username
}

func eventUserName(user *gitlab.EventUser) string {
	if user == nil {
		return ""
	}

	return user.Username
}

// activityFrom specializes base to one kind, scoping its dedup key to the
// kind as well as the source record so the several activities one payload
// yields stay individually deduplicable. It matches the helper the
// historical backfill uses, which is what lets the two paths agree on a key
// for the same underlying record.
func activityFrom(base activity.Event, kind activity.Kind, count int64) activity.Event {
	base.Kind = kind
	base.Count = count
	base.DedupKey = base.DedupKey + ":" + string(kind)

	return base
}

// webhookTimeLayouts are the timestamp spellings GitLab uses across webhook
// payloads. Unlike the REST API, which is consistently RFC 3339, webhook
// payloads carry a space-separated form with either a zone abbreviation or
// a numeric offset.
//
// Three layouts cover every spelling seen, because two of them are broader
// than they look: RFC 3339 accepts the fractional seconds some resources
// report and others omit, and Go's MST reference matches any zone
// abbreviation, "UTC" included. Layouts for those cases would never be
// reached.
//
//nolint:gochecknoglobals // a package-level lookup table, read-only after init
var webhookTimeLayouts = []string{
	time.RFC3339,
	"2006-01-02 15:04:05 MST",
	"2006-01-02 15:04:05 -0700",
}

// parseEventTime reads a payload timestamp, falling back to fallback when
// the field is absent or in a spelling none of the known layouts match:
// when an activity happened is worth less than the fact that it did, and
// the delivery is near enough to the event for the day-based criteria.
//
// The offset GitLab reported is kept rather than normalized to UTC. The day
// and hour an activity falls on decide the day-based criteria (streaks,
// night owl, early bird), and those should read the clock the instance
// reports, not one shifted out from under it.
func parseEventTime(raw string, fallback time.Time) time.Time {
	parsed, ok := parseTimestamp(raw)
	if !ok {
		return fallback
	}

	return parsed
}

// parseTimestamp reads a payload timestamp, reporting whether any known
// layout matched. It is what parseEventTime is built on, and what the
// callers that must not silently accept a substituted time use directly.
func parseTimestamp(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}

	for _, layout := range webhookTimeLayouts {
		parsed, err := time.Parse(layout, raw)
		if err == nil {
			return parsed, true
		}
	}

	return time.Time{}, false
}
