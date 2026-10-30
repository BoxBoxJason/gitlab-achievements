// Package eventsapi translates what GitLab's REST APIs report about past
// activity into this app's normalized activity model.
//
// It is the producer behind both read-side paths: the one-time historical
// backfill and the periodic reconciliation sync. Both observe activity
// through the Events API (plus the Pipelines API for what events don't
// cover), so both need the same translation, and having it in one place is
// what keeps a reconciliation run from judging activity by different rules
// than the walk that first counted it.
//
// # Agreeing with the webhook path
//
// The single most important property here is that the dedup keys produced
// match, byte for byte, the ones internal/webhook derives for the same
// underlying activity. Live ingestion and these APIs describe the same
// events through differently shaped payloads, and the reconciliation sync
// exists precisely to re-read windows that webhooks have already covered.
// Without matching keys, every reconciliation pass would count the window
// again, and since GitLab awards are never revoked, inflated counters
// cannot be undone.
//
// The keys are therefore reconstructed from the identifiers GitLab reports
// on both sides rather than from the event record's own ID:
//
//	push .......... push:<project>:<ref>:<after>       (push_data.commit_to)
//	tag push ...... tag_push:<project>:<ref>:<after>
//	merge request . merge_request:<id>:<action>        (target_id)
//	issue ......... issue:<id>:<action>                (target_id)
//	comment ....... note:<id>                          (note.id)
//	review comment  note:<id>:review_comment           (a DiffNote's note.id)
//	pipeline ...... pipeline:<id>
//	build fixed ... pipeline:<id>:pipeline_fixed
//	landed merge .. merge_request:<id>:landed          (merge requests API)
//	release ....... release:<project>:<tag>            (releases API)
//	wiki page ..... wiki_page:<project path>:<slug>    (wiki_page.slug)
//
// A key here is a compatibility surface with the processed-event log as
// much as with the other producer, so changing one silently re-counts
// whatever the old spelling had already counted. Change them together with
// the webhook package's, or not at all.
//
// # What the Events API cannot report
//
// Jobs, deployments, emoji reactions, resolved discussions and fast merges
// have no Events API representation at all, so nothing here
// produces them; they are webhook-only. Merge requests landing for their
// author aren't in it either, but the merge requests API reports them, so
// NormalizeMergedMergeRequest reads them from there. A reconciliation sync consequently
// heals everything else and leaves those to the live path, which is the
// safe direction to be wrong in: a missed heal undercounts, whereas a
// mismatched key would overcount permanently.
package eventsapi

import (
	"fmt"
	"strings"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go/v3"

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

// Dedup-key action names, which are a vocabulary of their own: the webhook
// path spells them in neither GitLab dialect (its payloads say "open",
// "close", "merge"), so the key's spelling is fixed independently of both,
// and these constants are what pins this producer to it.
const (
	keyActionOpened   = "opened"
	keyActionClosed   = "closed"
	keyActionMerged   = "merged"
	keyActionApproved = "approved"
)

// push_data field values, describing what a push did to which kind of ref.
// client-go models push_data.action and push_data.ref_type as plain
// strings, with no constants of its own.
const (
	pushActionCreated = "created"
	refTypeBranch     = "branch"
	refTypeTag        = "tag"
)

// Ref prefixes GitLab reports in full on webhook payloads and in short form
// (just the branch or tag name) in an event's push_data, restored here so
// the two spell the same push the same way.
const (
	branchRefPrefix = "refs/heads/"
	tagRefPrefix    = "refs/tags/"
	qualifiedRef    = "refs/"
)

// nullSHA is what a webhook payload carries where a ref did not exist
// before the push, or no longer exists after it. An event's push_data
// leaves the corresponding field empty instead, so it is substituted to
// keep a branch deletion's key identical on both paths.
const nullSHA = "0000000000000000000000000000000000000000"

// NormalizeProjectEvent translates one GitLab project event into the
// normalized activities it represents, which may be none (an event this app
// tracks nothing for) or several (a push that both created a branch and
// delivered commits).
//
// Events with no author are dropped: every achievement is awarded to a
// user, so activity GitLab can't attribute to one has nothing to advance.
func NormalizeProjectEvent(event *gitlab.ProjectEvent) []activity.Event {
	if event == nil || event.AuthorID == 0 {
		return nil
	}

	base := activity.Event{
		OccurredAt:     ParseEventTime(event.CreatedAt),
		ActorUsername:  eventAuthorUsername(event),
		ActorAvatarURL: event.Author.AvatarURL,
		ActorID:        event.AuthorID,
		ProjectID:      event.ProjectID,
	}

	// A push is recognized by its push_data rather than by its target type.
	// Older instances leave a push's target type empty, but GitLab 19 reports
	// it as "Project", and either way the ref type is only ever set on a
	// push.
	if event.PushData.RefType != "" {
		return pushActivities(event, base)
	}

	return targetActivities(event, base)
}

// pushActivities derives the activities a push represents: the push
// itself, the commits it delivered, and the branch or tag it created, if
// any.
func pushActivities(event *gitlab.ProjectEvent, base activity.Event) []activity.Event {
	base.DedupKey = pushDedupKey(event)

	activities := []activity.Event{activityFrom(base, activity.KindPush, 1)}
	created := event.PushData.Action == pushActionCreated

	// A tag push deliberately counts no commits, matching the live path: a
	// tag names commits some earlier branch push already delivered and was
	// already counted for, so counting them again would inflate every
	// tagged release by its whole history.
	if event.PushData.RefType == refTypeTag {
		if created {
			activities = append(activities, activityFrom(base, activity.KindTagCreated, 1))
		}

		return activities
	}

	if created && event.PushData.RefType == refTypeBranch {
		activities = append(activities, activityFrom(base, activity.KindBranchCreated, 1))
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

// pushDedupKey rebuilds the key the webhook path derives for the same push,
// out of the pieces an event's push_data reports under other names and in
// shorter forms.
//
// A ref that already arrives fully qualified is left alone: the short form
// is what every instance checked reports, but prefixing one twice would
// silently split one push across two keys, and tolerating both spellings
// costs a comparison.
func pushDedupKey(event *gitlab.ProjectEvent) string {
	prefix, keyKind := branchRefPrefix, "push"
	if event.PushData.RefType == refTypeTag {
		prefix, keyKind = tagRefPrefix, "tag_push"
	}

	ref := event.PushData.Ref
	if !strings.HasPrefix(ref, qualifiedRef) {
		ref = prefix + ref
	}

	// Empty is what push_data reports where a webhook payload reports the
	// null SHA, which is every ref deletion.
	after := event.PushData.CommitTo
	if after == "" {
		after = nullSHA
	}

	return fmt.Sprintf("%s:%d:%s:%s", keyKind, event.ProjectID, ref, after)
}

// targetActivities derives the activity a non-push event represents: one
// action on one merge request, issue, or note.
//
// Unlike a push, these are keyed on the resource acted upon rather than on
// the event, and carry no per-kind suffix, because that is how the webhook
// path keys them: one merge request action yields one activity, so there is
// nothing to tell apart within it.
func targetActivities(event *gitlab.ProjectEvent, base activity.Event) []activity.Event {
	kind, key, ok := targetKind(event)
	if !ok {
		return nil
	}

	base.Kind = kind
	base.DedupKey = key
	base.Count = 1

	if kind == activity.KindComment && reviewNote(event) {
		return []activity.Event{base, activityFrom(base, activity.KindReviewComment, 1)}
	}

	return []activity.Event{base}
}

// reviewNote reports whether a note event is a comment on a line of a merge
// request's diff. GitLab reports those as DiffNotes, which is also what a
// comment on a commit's diff is, so what the note was left on tells the two
// apart:
// the webhook path can only recognize the merge request ones.
func reviewNote(event *gitlab.ProjectEvent) bool {
	folded := strings.ToLower(strings.ReplaceAll(event.TargetType, "_", ""))

	return folded == "diffnote" && normalizeTargetType(event.Note.NoteableType) == targetMergeRequest
}

// targetKind maps a non-push event's target type and action onto the
// activity kind it represents and the dedup key the webhook path gives it,
// reporting false for the ones this app tracks nothing for (milestones,
// snippets, membership changes, reopenings).
func targetKind(event *gitlab.ProjectEvent) (activity.Kind, string, bool) {
	action := strings.ToLower(event.ActionName)

	switch normalizeTargetType(event.TargetType) {
	case targetMergeRequest:
		return mergeRequestKind(event, action)
	case targetIssue:
		kind, keyAction, ok := issueKind(action)
		if !ok {
			return "", "", false
		}

		return kind, fmt.Sprintf("issue:%d:%s", event.TargetID, keyAction), true
	case targetNote:
		// System notes are GitLab narrating its own state changes ("changed
		// the description", "mentioned in commit ..."), not something a
		// user wrote, so they don't count as engagement.
		if action != actionCommented || event.Note.System {
			return "", "", false
		}

		// The note's own ID rather than the event's target ID: the two
		// agree, but the webhook path keys on the note, and this has to
		// spell the same key from whichever field is the note's identity.
		return activity.KindComment, fmt.Sprintf("note:%d", noteID(event)), true
	case targetUnknown:
		return "", "", false
	}

	return "", "", false
}

// noteID picks the comment's identifier, falling back to the event's target
// ID on an instance that reports the note object without an ID of its own.
func noteID(event *gitlab.ProjectEvent) int64 {
	if event.Note.ID != 0 {
		return event.Note.ID
	}

	return event.TargetID
}

// mergeRequestKind maps a merge request event's action onto its activity
// kind and dedup key.
//
// An approval's key is scoped to the approver, matching the live path, so
// that a second reviewer approving the same merge request counts separately
// rather than colliding with the first.
func mergeRequestKind(event *gitlab.ProjectEvent, action string) (activity.Kind, string, bool) {
	key := func(keyAction string) string {
		return fmt.Sprintf("merge_request:%d:%s", event.TargetID, keyAction)
	}

	switch action {
	case actionOpened:
		return activity.KindMergeRequestOpened, key(keyActionOpened), true
	case actionAccepted, actionMerged:
		return activity.KindMergeRequestMerged, key(keyActionMerged), true
	case actionApproved:
		return activity.KindMergeRequestApproved,
			fmt.Sprintf("%s:%d", key(keyActionApproved), event.AuthorID), true
	case actionClosed:
		return activity.KindMergeRequestClosed, key(keyActionClosed), true
	}

	return "", "", false
}

// issueKind maps an issue event's action onto its activity kind and the
// action name its dedup key carries.
func issueKind(action string) (activity.Kind, string, bool) {
	switch action {
	case actionOpened:
		return activity.KindIssueOpened, keyActionOpened, true
	case actionClosed:
		return activity.KindIssueClosed, keyActionClosed, true
	}

	return "", "", false
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

// NormalizePipeline translates one pipeline into the activities it
// represents: the run itself, plus its outcome once it has one.
//
// Pipelines with no triggering user (schedules, API triggers with a
// detached token) are dropped, for the same reason authorless events are.
func NormalizePipeline(pipeline *gitlab.Pipeline) []activity.Event {
	base, ok := pipelineBase(pipeline)
	if !ok {
		return nil
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

// actionCreated is the action_name the Events API gives a wiki page being
// created, where it says "opened" for an issue or a merge request.
const actionCreated = "created"

// NormalizeWikiPageEvent counts a wiki page a user created, from a project
// event whose project lives at projectPath.
//
// It is apart from NormalizeProjectEvent because the key needs something the
// event doesn't carry. The live path keys a page on its project's path and
// slug, the only identifiers the wiki hook has, so this has to spell the
// same key from the path the caller listed the project under. A project
// renamed between the hook and a reconciliation pass re-reading it would
// count the page twice; that is a rename inside the look-back window, and
// the price of agreeing with a hook that names no project ID.
//
// Like the live path, it ties the page to no project, so the two paths
// agree on what it adds to the projects a user has contributed to.
func NormalizeWikiPageEvent(event *gitlab.ProjectEvent, projectPath string) []activity.Event {
	if event == nil || event.AuthorID == 0 || projectPath == "" || event.WikiPage == nil || event.WikiPage.Slug == "" {
		return nil
	}

	folded := strings.ToLower(strings.ReplaceAll(event.TargetType, "_", ""))
	if !strings.HasPrefix(folded, "wikipage") || strings.ToLower(event.ActionName) != actionCreated {
		return nil
	}

	return []activity.Event{{
		OccurredAt:     ParseEventTime(event.CreatedAt),
		Kind:           activity.KindWikiPageCreated,
		DedupKey:       fmt.Sprintf("wiki_page:%s:%s", projectPath, event.WikiPage.Slug),
		ActorUsername:  eventAuthorUsername(event),
		ActorAvatarURL: event.Author.AvatarURL,
		ActorID:        event.AuthorID,
		Count:          1,
	}}
}

// NormalizeMergedMergeRequest credits a merged merge request to its author.
//
// The Events API can't: its merge event is the merger's, and names the
// merge request without its author. The merge requests API names both, so
// this is what backfill and reconciliation read the landing from, with the
// key the webhook path gives the same merge.
//
// A merge request that isn't merged, or names no author, yields nothing.
func NormalizeMergedMergeRequest(merged *gitlab.BasicMergeRequest) []activity.Event {
	if merged == nil || merged.MergedAt == nil || merged.Author == nil || merged.Author.ID == 0 {
		return nil
	}

	return []activity.Event{{
		OccurredAt:     *merged.MergedAt,
		Kind:           activity.KindMergeRequestLanded,
		DedupKey:       fmt.Sprintf("merge_request:%d:landed", merged.ID),
		ActorUsername:  merged.Author.Username,
		ActorAvatarURL: merged.Author.AvatarURL,
		ActorID:        merged.Author.ID,
		ProjectID:      merged.ProjectID,
		Count:          1,
	}}
}

// NormalizeRelease credits a release to whoever created it.
//
// The Events API has no record of releases at all, so backfill and
// reconciliation list them per project through the releases API, which is
// also the only place the author is named: the release hook leaves it out.
// A release is keyed on its project and tag because that is what both the
// API and the hook identify it by; neither reports an ID.
//
// It is dated by creation rather than by released_at, which whoever creates
// a release may set to any moment they like, past or future.
func NormalizeRelease(projectID int64, release *gitlab.Release) []activity.Event {
	if release == nil || release.Author.ID == 0 || release.TagName == "" {
		return nil
	}

	return []activity.Event{{
		OccurredAt:     TimeOrZero(release.CreatedAt),
		Kind:           activity.KindReleasePublished,
		DedupKey:       fmt.Sprintf("release:%d:%s", projectID, release.TagName),
		ActorUsername:  release.Author.Username,
		ActorAvatarURL: release.Author.AvatarURL,
		ActorID:        release.Author.ID,
		ProjectID:      projectID,
		Count:          1,
	}}
}

// NormalizePipelineFix reports a passing pipeline as having fixed the build,
// given how the pipeline before it on the same ref ended. The caller finds
// that out, since one pipeline says nothing about its predecessors: a walk
// over a project's pipelines in order already knows, and anyone else asks.
//
// Only a pass that follows a failure counts. A pipeline retried from
// failure into success keeps its ID, so it fixes nothing by this measure
// unless the one before it had failed too: retrying a flaky job is not
// fixing the build.
func NormalizePipelineFix(pipeline *gitlab.Pipeline, previous gitlab.BuildStateValue) []activity.Event {
	if previous != gitlab.Failed || pipeline == nil || gitlab.BuildStateValue(pipeline.Status) != gitlab.Success {
		return nil
	}

	base, ok := pipelineBase(pipeline)
	if !ok {
		return nil
	}

	return []activity.Event{activityFrom(base, activity.KindPipelineFixed, 1)}
}

// pipelineBase is what every activity a pipeline yields has in common,
// reporting false for a pipeline with no triggering user to credit.
func pipelineBase(pipeline *gitlab.Pipeline) (activity.Event, bool) {
	if pipeline == nil || pipeline.User == nil || pipeline.User.ID == 0 {
		return activity.Event{}, false
	}

	return activity.Event{
		OccurredAt:     TimeOrZero(pipeline.CreatedAt),
		DedupKey:       fmt.Sprintf("pipeline:%d", pipeline.ID),
		ActorUsername:  pipeline.User.Username,
		ActorAvatarURL: pipeline.User.AvatarURL,
		ActorID:        pipeline.User.ID,
		ProjectID:      pipeline.ProjectID,
	}, true
}

// activityFrom specializes base to one kind, scoping its dedup key to the
// kind as well as the source record so the several activities one GitLab
// record yields stay individually deduplicable. It matches the helper the
// webhook normalizer uses, and must keep matching it.
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

// ParseEventTime reads an event's timestamp, which the Events API returns
// as a string rather than a parsed time. An unparseable value yields the
// zero time rather than dropping the activity: when the event happened is
// worth less than the fact that it did.
//
// The offset GitLab reported is kept rather than normalized to UTC. The
// day and hour an activity falls on decide the day-based criteria
// (streaks, night owl, early bird), and those should read the clock the
// instance reports, not one shifted out from under it.
func ParseEventTime(raw string) time.Time {
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}

	return parsed
}

// TimeOrZero dereferences an optional timestamp, keeping the offset it
// carries for the same reason ParseEventTime does.
func TimeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}

	return *t
}
