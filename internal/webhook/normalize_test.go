package webhook

import (
	"testing"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"

	"github.com/boxboxjason/gitlab-achievements/internal/activity"
)

// receivedAt is the fixed "now" the tests normalize against, so the
// fallback dating is assertable.
//
//nolint:gochecknoglobals // a test fixture, read-only
var receivedAt = time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)

func kindsOf(events []activity.Event) []activity.Kind {
	kinds := make([]activity.Kind, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}

	return kinds
}

func findKind(t *testing.T, events []activity.Event, kind activity.Kind) activity.Event {
	t.Helper()

	for _, event := range events {
		if event.Kind == kind {
			return event
		}
	}

	t.Fatalf("expected a %q activity, got %v", kind, kindsOf(events))

	return activity.Event{}
}

func TestNormalize_PushCountsThePushAndItsCommits(t *testing.T) {
	events := normalize(&gitlab.PushEvent{
		UserID: 10, UserUsername: "alice", ProjectID: 1,
		Ref:               "refs/heads/main",
		Before:            "aaaa",
		After:             "bbbb",
		TotalCommitsCount: 3,
	}, receivedAt)

	if len(events) != 2 {
		t.Fatalf("expected a push and its commits, got %v", kindsOf(events))
	}

	commits := findKind(t, events, activity.KindCommit)
	if commits.Weight() != 3 {
		t.Errorf("expected the push's 3 commits to be carried as the activity's weight, got %d", commits.Weight())
	}

	push := findKind(t, events, activity.KindPush)
	if push.ActorID != 10 || push.ActorUsername != "alice" || push.ProjectID != 1 {
		t.Errorf("expected the pushing user and project to be carried, got %+v", push)
	}

	if !push.OccurredAt.Equal(receivedAt) {
		t.Errorf("expected a push with no timestamp of its own to be dated on receipt, got %v", push.OccurredAt)
	}
}

func TestNormalize_PushReportsTheTrueCommitCountRatherThanTheTruncatedArray(t *testing.T) {
	// GitLab caps the commits array on large pushes but always reports the
	// real total, so the total is what must be counted.
	events := normalize(&gitlab.PushEvent{
		UserID: 10, UserUsername: "alice", ProjectID: 1,
		Ref: "refs/heads/main", Before: "aaaa", After: "bbbb",
		Commits:           []*gitlab.PushEventCommit{{ID: "c1"}, {ID: "c2"}},
		TotalCommitsCount: 87,
	}, receivedAt)

	if got := findKind(t, events, activity.KindCommit).Weight(); got != 87 {
		t.Errorf("expected the reported total of 87 commits, got %d", got)
	}
}

func TestNormalize_PushCreatingABranchCountsTheBranch(t *testing.T) {
	events := normalize(&gitlab.PushEvent{
		UserID: 10, UserUsername: "alice", ProjectID: 1,
		Ref: "refs/heads/feature", Before: nullSHA, After: "bbbb",
		TotalCommitsCount: 1,
	}, receivedAt)

	findKind(t, events, activity.KindBranchCreated)
}

func TestNormalize_PushDeletingABranchCreatesNothing(t *testing.T) {
	events := normalize(&gitlab.PushEvent{
		UserID: 10, UserUsername: "alice", ProjectID: 1,
		Ref: "refs/heads/feature", Before: "bbbb", After: nullSHA,
	}, receivedAt)

	for _, event := range events {
		if event.Kind == activity.KindBranchCreated {
			t.Errorf("expected deleting a branch not to count as creating one, got %v", kindsOf(events))
		}
	}
}

func TestNormalize_PushesToDifferentRefsAreCountedSeparately(t *testing.T) {
	// Creating two branches at the same commit yields two pushes that are
	// identical except for the ref, so the ref has to be part of the key.
	first := normalize(&gitlab.PushEvent{
		UserID: 10, ProjectID: 1, Ref: "refs/heads/a", Before: nullSHA, After: "bbbb",
	}, receivedAt)
	second := normalize(&gitlab.PushEvent{
		UserID: 10, ProjectID: 1, Ref: "refs/heads/b", Before: nullSHA, After: "bbbb",
	}, receivedAt)

	if first[0].DedupKey == second[0].DedupKey {
		t.Errorf("expected pushes to different refs to have distinct dedup keys, both were %q", first[0].DedupKey)
	}
}

func TestNormalize_RedeliveredPushDedupesToTheSameKeys(t *testing.T) {
	event := &gitlab.PushEvent{
		UserID: 10, ProjectID: 1, Ref: "refs/heads/main", Before: "aaaa", After: "bbbb",
		TotalCommitsCount: 2,
	}

	first := normalize(event, receivedAt)
	second := normalize(event, receivedAt.Add(time.Hour))

	for i := range first {
		if first[i].DedupKey != second[i].DedupKey {
			t.Errorf("expected a redelivery to reuse the dedup key, got %q then %q", first[i].DedupKey, second[i].DedupKey)
		}
	}
}

func TestNormalize_AuthorlessPushIsDropped(t *testing.T) {
	events := normalize(&gitlab.PushEvent{ProjectID: 1, Ref: "refs/heads/main", After: "bbbb"}, receivedAt)
	if len(events) != 0 {
		t.Errorf("expected activity GitLab can't attribute to a user to be dropped, got %v", kindsOf(events))
	}
}

func TestNormalize_TagPushCountsTheTagButNotItsCommits(t *testing.T) {
	events := normalize(&gitlab.TagEvent{
		UserID: 10, UserUsername: "alice", ProjectID: 1,
		Ref: "refs/tags/v1.0.0", Before: nullSHA, After: "bbbb",
		// A tag names commits an earlier branch push already delivered.
		TotalCommitsCount: 400,
	}, receivedAt)

	findKind(t, events, activity.KindTagCreated)
	findKind(t, events, activity.KindPush)

	for _, event := range events {
		if event.Kind == activity.KindCommit {
			t.Error("expected a tag push not to re-count the commits it points at")
		}
	}
}

func mergeEvent(action string) *gitlab.MergeEvent {
	return &gitlab.MergeEvent{
		User: &gitlab.EventUser{ID: 10, Username: "alice"},
		ObjectAttributes: gitlab.MergeEventObjectAttributes{
			ID: 55, TargetProjectID: 1, Action: action,
			CreatedAt: "2026-03-01 09:00:00 UTC",
			UpdatedAt: "2026-03-02 10:30:00 UTC",
		},
	}
}

func TestNormalize_MergeRequestActions(t *testing.T) {
	cases := map[string]activity.Kind{
		"open":  activity.KindMergeRequestOpened,
		"merge": activity.KindMergeRequestMerged,
		"close": activity.KindMergeRequestClosed,
	}

	for action, want := range cases {
		events := normalize(mergeEvent(action), receivedAt)
		if len(events) != 1 || events[0].Kind != want {
			t.Errorf("action %q: expected %q, got %v", action, want, kindsOf(events))
		}
	}
}

func TestNormalize_MergeRequestUpdatesAreNotActivity(t *testing.T) {
	for _, action := range []string{"update", "reopen", "unapproval", ""} {
		events := normalize(mergeEvent(action), receivedAt)
		if len(events) != 0 {
			t.Errorf("action %q: expected no activity, got %v", action, kindsOf(events))
		}
	}
}

func TestNormalize_MergeRequestApprovalIsCountedOncePerApprover(t *testing.T) {
	// GitLab sends both "approval" (this user approved) and "approved" (the
	// merge request is now fully approved) for the approval that satisfies
	// the last rule. They are one user action and must collapse to one key.
	perUser := normalize(mergeEvent(actionApproval), receivedAt)
	fullyApproved := normalize(mergeEvent(actionApproved), receivedAt)

	if len(perUser) != 1 || perUser[0].Kind != activity.KindMergeRequestApproved {
		t.Fatalf("expected an approval activity, got %v", kindsOf(perUser))
	}

	if perUser[0].DedupKey != fullyApproved[0].DedupKey {
		t.Errorf("expected GitLab's paired approval deliveries to share a key, got %q and %q",
			perUser[0].DedupKey, fullyApproved[0].DedupKey)
	}
}

func TestNormalize_ASecondApproverIsCountedSeparately(t *testing.T) {
	alice := normalize(mergeEvent(actionApproval), receivedAt)

	bobEvent := mergeEvent(actionApproval)
	bobEvent.User = &gitlab.EventUser{ID: 11, Username: "bob"}
	bob := normalize(bobEvent, receivedAt)

	if alice[0].DedupKey == bob[0].DedupKey {
		t.Errorf("expected each approver's approval to count, both keyed %q", alice[0].DedupKey)
	}
}

func TestNormalize_MergeRequestIsAttributedToWhoActedNotWhoAuthored(t *testing.T) {
	event := mergeEvent("merge")
	event.ObjectAttributes.AuthorID = 99

	events := normalize(event, receivedAt)
	if events[0].ActorID != 10 {
		t.Errorf("expected the merging user (10), not the author (99), got %d", events[0].ActorID)
	}
}

func TestNormalize_MergeRequestOpenUsesItsCreationTime(t *testing.T) {
	events := normalize(mergeEvent("open"), receivedAt)

	want := time.Date(2026, time.March, 1, 9, 0, 0, 0, time.UTC)
	if !events[0].OccurredAt.Equal(want) {
		t.Errorf("expected the merge request's creation time %v, got %v", want, events[0].OccurredAt)
	}
}

func TestNormalize_MergeRequestMergeUsesItsUpdateTime(t *testing.T) {
	events := normalize(mergeEvent("merge"), receivedAt)

	want := time.Date(2026, time.March, 2, 10, 30, 0, 0, time.UTC)
	if !events[0].OccurredAt.Equal(want) {
		t.Errorf("expected the merge time %v, got %v", want, events[0].OccurredAt)
	}
}

func TestNormalize_IssueActions(t *testing.T) {
	cases := map[string]activity.Kind{
		"open":  activity.KindIssueOpened,
		"close": activity.KindIssueClosed,
	}

	for action, want := range cases {
		events := normalize(&gitlab.IssueEvent{
			User: &gitlab.EventUser{ID: 10, Username: "alice"},
			ObjectAttributes: gitlab.IssueEventObjectAttributes{
				ID: 88, ProjectID: 1, Action: action,
				CreatedAt: "2026-03-01 09:00:00 UTC", UpdatedAt: "2026-03-02 10:00:00 UTC",
			},
		}, receivedAt)

		if len(events) != 1 || events[0].Kind != want {
			t.Errorf("action %q: expected %q, got %v", action, want, kindsOf(events))
		}
	}
}

func TestNormalize_PipelineRunAndOutcome(t *testing.T) {
	cases := map[string][]activity.Kind{
		"success": {activity.KindPipelineRun, activity.KindPipelineSucceeded},
		"failed":  {activity.KindPipelineRun, activity.KindPipelineFailed},
		"running": {activity.KindPipelineRun},
		"skipped": {activity.KindPipelineRun},
	}

	for status, want := range cases {
		events := normalize(&gitlab.PipelineEvent{
			User:             &gitlab.EventUser{ID: 10, Username: "alice"},
			Project:          gitlab.PipelineEventProject{ID: 1},
			ObjectAttributes: gitlab.PipelineEventObjectAttributes{ID: 500, Status: status},
		}, receivedAt)

		if len(events) != len(want) {
			t.Errorf("status %q: expected %v, got %v", status, want, kindsOf(events))
		}
	}
}

func TestNormalize_PipelineKeysMatchTheBackfillsForTheSamePipeline(t *testing.T) {
	// Pipelines are the one resource both paths can identify the same way,
	// so a pipeline seen by history and live ingestion must count once. The
	// backfill derives "pipeline:<id>:<kind>" for the same record.
	events := normalize(&gitlab.PipelineEvent{
		User:             &gitlab.EventUser{ID: 10, Username: "alice"},
		Project:          gitlab.PipelineEventProject{ID: 1},
		ObjectAttributes: gitlab.PipelineEventObjectAttributes{ID: 500, Status: "success"},
	}, receivedAt)

	run := findKind(t, events, activity.KindPipelineRun)
	if run.DedupKey != "pipeline:500:pipeline_run" {
		t.Errorf("expected the key the backfill derives, got %q", run.DedupKey)
	}
}

func TestNormalize_PipelineTransitionsOfOneRunShareTheirKeys(t *testing.T) {
	running := normalize(&gitlab.PipelineEvent{
		User:             &gitlab.EventUser{ID: 10},
		ObjectAttributes: gitlab.PipelineEventObjectAttributes{ID: 500, Status: "running"},
	}, receivedAt)
	succeeded := normalize(&gitlab.PipelineEvent{
		User:             &gitlab.EventUser{ID: 10},
		ObjectAttributes: gitlab.PipelineEventObjectAttributes{ID: 500, Status: "success"},
	}, receivedAt)

	if running[0].DedupKey != succeeded[0].DedupKey {
		t.Errorf("expected every transition of one pipeline to count the run once, got %q and %q",
			running[0].DedupKey, succeeded[0].DedupKey)
	}
}

func TestNormalize_CommentsOnEveryNoteableCount(t *testing.T) {
	attrs := func(id int64) gitlab.CommitCommentEventObjectAttributes {
		return gitlab.CommitCommentEventObjectAttributes{
			ID: id, AuthorID: 10, ProjectID: 1, CreatedAt: "2026-03-01 09:00:00 UTC",
		}
	}

	events := normalize(&gitlab.CommitCommentEvent{
		User: &gitlab.User{ID: 10, Username: "alice"}, ProjectID: 1, ObjectAttributes: attrs(1),
	}, receivedAt)
	if len(events) != 1 || events[0].Kind != activity.KindComment {
		t.Errorf("expected a commit comment to count, got %v", kindsOf(events))
	}

	merge := normalize(&gitlab.MergeCommentEvent{
		User: &gitlab.EventUser{ID: 10, Username: "alice"}, ProjectID: 1,
		ObjectAttributes: gitlab.MergeCommentEventObjectAttributes{
			ID: 2, AuthorID: 10, ProjectID: 1, CreatedAt: "2026-03-01 09:00:00 UTC",
		},
	}, receivedAt)
	if len(merge) != 1 || merge[0].Kind != activity.KindComment {
		t.Errorf("expected a merge request comment to count, got %v", kindsOf(merge))
	}
}

func TestNormalize_SystemNotesAreNotEngagement(t *testing.T) {
	events := normalize(&gitlab.IssueCommentEvent{
		User: &gitlab.User{ID: 10, Username: "alice"}, ProjectID: 1,
		ObjectAttributes: gitlab.IssueCommentEventObjectAttributes{
			ID: 3, AuthorID: 10, ProjectID: 1, System: true,
		},
	}, receivedAt)

	if len(events) != 0 {
		t.Errorf("expected GitLab's own state narration not to count as a comment, got %v", kindsOf(events))
	}
}

func mergedAfter(gap time.Duration) *gitlab.MergeEvent {
	opened := time.Date(2026, time.March, 1, 9, 0, 0, 0, time.UTC)

	return &gitlab.MergeEvent{
		User: &gitlab.EventUser{ID: 10, Username: "alice"},
		ObjectAttributes: gitlab.MergeEventObjectAttributes{
			ID: 55, TargetProjectID: 1, Action: "merge",
			CreatedAt: opened.Format(time.RFC3339),
			UpdatedAt: opened.Add(gap).Format(time.RFC3339),
		},
	}
}

func TestNormalize_MergeWithinTheWindowAlsoCountsAsFast(t *testing.T) {
	events := normalize(mergedAfter(30*time.Minute), receivedAt)

	if len(events) != 2 {
		t.Fatalf("expected the merge and the fast merge, got %v", kindsOf(events))
	}

	fast := findKind(t, events, activity.KindMergeRequestMergedFast)
	if fast.DedupKey != "merge_request:55:merged_fast" {
		t.Errorf("expected the fast merge to be keyed apart from the merge, got %q", fast.DedupKey)
	}

	if fast.ActorID != 10 {
		t.Errorf("expected the fast merge to be credited to whoever merged, got user %d", fast.ActorID)
	}
}

func TestNormalize_SlowMergeIsOnlyAMerge(t *testing.T) {
	events := normalize(mergedAfter(3*time.Hour), receivedAt)

	if len(events) != 1 || events[0].Kind != activity.KindMergeRequestMerged {
		t.Errorf("expected a merge request merged hours later not to count as fast, got %v", kindsOf(events))
	}
}

func TestNormalize_FastMergeNeedsBothTimestampsToParse(t *testing.T) {
	// parseEventTime substitutes the delivery time for a spelling it doesn't
	// know, and two substitutions are equal, which would score every merge on
	// such an instance as instantaneous.
	event := mergedAfter(30 * time.Minute)
	event.ObjectAttributes.CreatedAt = "last Tuesday"

	events := normalize(event, receivedAt)

	if len(events) != 1 || events[0].Kind != activity.KindMergeRequestMerged {
		t.Errorf("expected an unreadable opening time to yield no fast merge, got %v", kindsOf(events))
	}
}

func resolvedNote(resolvedBy int64, byPush bool) *gitlab.MergeCommentEvent {
	return &gitlab.MergeCommentEvent{
		User: &gitlab.EventUser{ID: 10, Username: "alice"}, ProjectID: 1,
		ObjectAttributes: gitlab.MergeCommentEventObjectAttributes{
			ID: 7, AuthorID: 20, ProjectID: 1, DiscussionID: "abc123",
			CreatedAt:  "2026-03-01 09:00:00 UTC",
			ResolvedAt: "2026-03-02 11:00:00 UTC", ResolvedByID: resolvedBy, ResolvedByPush: byPush,
		},
	}
}

func TestNormalize_ResolvingADiscussionCountsForTheResolver(t *testing.T) {
	events := normalize(resolvedNote(10, false), receivedAt)

	resolved := findKind(t, events, activity.KindDiscussionResolved)
	if resolved.ActorID != 10 || resolved.ActorUsername != "alice" {
		t.Errorf("expected the resolution credited to who resolved it, got %d/%q",
			resolved.ActorID, resolved.ActorUsername)
	}

	if resolved.DedupKey != "discussion:abc123:resolved" {
		t.Errorf("expected the resolution keyed on the discussion, got %q", resolved.DedupKey)
	}

	// The comment itself still counts, and still belongs to its author
	// rather than to whoever resolved the thread.
	comment := findKind(t, events, activity.KindComment)
	if comment.ActorID != 20 {
		t.Errorf("expected the comment to stay with its author, got user %d", comment.ActorID)
	}
}

func TestNormalize_DiscussionResolvedByAPushIsNobodysAchievement(t *testing.T) {
	events := normalize(resolvedNote(10, true), receivedAt)

	for _, event := range events {
		if event.Kind == activity.KindDiscussionResolved {
			t.Error("expected a discussion GitLab resolved on its own not to count")
		}
	}
}

func TestNormalize_UnresolvedNoteResolvesNothing(t *testing.T) {
	events := normalize(resolvedNote(0, false), receivedAt)

	if len(events) != 1 || events[0].Kind != activity.KindComment {
		t.Errorf("expected an unresolved note to be a comment and nothing more, got %v", kindsOf(events))
	}
}

func TestNormalize_ResolutionsOfOneDiscussionShareTheirKey(t *testing.T) {
	// GitLab redelivers the note as the thread is edited afterwards, each
	// delivery still carrying the resolution.
	first := normalize(resolvedNote(10, false), receivedAt)

	later := resolvedNote(10, false)
	later.ObjectAttributes.ID = 8

	second := normalize(later, receivedAt)

	if findKind(t, first, activity.KindDiscussionResolved).DedupKey !=
		findKind(t, second, activity.KindDiscussionResolved).DedupKey {
		t.Error("expected one discussion to be resolved once however many of its notes are delivered")
	}
}

func TestNormalize_JobIsCountedOncePerBuild(t *testing.T) {
	jobEvent := func(status string) *gitlab.JobEvent {
		return &gitlab.JobEvent{
			User: &gitlab.EventUser{ID: 10, Username: "alice"}, ProjectID: 1,
			BuildID: 900, BuildStatus: status, BuildCreatedAtISO: "2026-03-01T09:00:00Z",
		}
	}

	running := normalize(jobEvent("running"), receivedAt)
	if len(running) != 1 || running[0].Kind != activity.KindJobRun {
		t.Fatalf("expected a job run, got %v", kindsOf(running))
	}

	failed := normalize(jobEvent("failed"), receivedAt)
	if len(failed) != 1 {
		t.Errorf("expected a job's outcome not to be scored separately, got %v", kindsOf(failed))
	}

	if running[0].DedupKey != failed[0].DedupKey {
		t.Errorf("expected every transition of one job to count it once, got %q and %q",
			running[0].DedupKey, failed[0].DedupKey)
	}

	want := time.Date(2026, time.March, 1, 9, 0, 0, 0, time.UTC)
	if !running[0].OccurredAt.Equal(want) {
		t.Errorf("expected the job's creation time %v, got %v", want, running[0].OccurredAt)
	}
}

func TestNormalize_UserlessJobIsDropped(t *testing.T) {
	// Scheduled pipelines and API triggers with a detached token run jobs
	// nobody can be credited for.
	events := normalize(&gitlab.JobEvent{ProjectID: 1, BuildID: 900}, receivedAt)
	if len(events) != 0 {
		t.Errorf("expected a job with no user to yield nothing, got %v", kindsOf(events))
	}
}

func TestNormalize_DeploymentRunAndOutcome(t *testing.T) {
	cases := map[string][]activity.Kind{
		"success":  {activity.KindDeployment, activity.KindDeploymentSucceeded},
		"running":  {activity.KindDeployment},
		"failed":   {activity.KindDeployment},
		"canceled": {activity.KindDeployment},
	}

	for status, want := range cases {
		events := normalize(&gitlab.DeploymentEvent{
			User: &gitlab.EventUser{ID: 10, Username: "alice"},
			Project: gitlab.DeploymentEventProject{ID: 1}, DeploymentID: 42, Status: status,
			StatusChangedAt: "2026-03-01 09:00:00 UTC",
		}, receivedAt)

		if len(events) != len(want) {
			t.Errorf("status %q: expected %v, got %v", status, want, kindsOf(events))
		}
	}
}

func TestNormalize_DeploymentTransitionsShareTheirKeys(t *testing.T) {
	deployment := func(status string) *gitlab.DeploymentEvent {
		return &gitlab.DeploymentEvent{
			User: &gitlab.EventUser{ID: 10}, Project: gitlab.DeploymentEventProject{ID: 1},
			DeploymentID: 42, Status: status,
		}
	}

	running := normalize(deployment("running"), receivedAt)
	succeeded := normalize(deployment("success"), receivedAt)

	if running[0].DedupKey != succeeded[0].DedupKey {
		t.Errorf("expected every transition of one deployment to count it once, got %q and %q",
			running[0].DedupKey, succeeded[0].DedupKey)
	}
}

func TestNormalize_EmojiAwardCountsAndRevokeDoesNot(t *testing.T) {
	emoji := func(action string) *gitlab.EmojiEvent {
		return &gitlab.EmojiEvent{
			User: gitlab.EventUser{ID: 10, Username: "alice"}, ProjectID: 1,
			ObjectAttributes: gitlab.EmojiEventObjectAttributes{
				ID: 77, Name: "thumbsup", Action: action, CreatedAt: "2026-03-01 09:00:00 UTC",
			},
		}
	}

	awarded := normalize(emoji("award"), receivedAt)
	if len(awarded) != 1 || awarded[0].Kind != activity.KindEmojiAwarded {
		t.Fatalf("expected an awarded reaction to count, got %v", kindsOf(awarded))
	}

	if awarded[0].DedupKey != "emoji:77" {
		t.Errorf("expected the reaction keyed on its own ID, got %q", awarded[0].DedupKey)
	}

	if events := normalize(emoji("revoke"), receivedAt); len(events) != 0 {
		t.Errorf("expected a withdrawn reaction not to take anything back, got %v", kindsOf(events))
	}
}

func TestNormalize_WikiPageCreationCountsAndEditsDoNot(t *testing.T) {
	wiki := func(action, slug string) *gitlab.WikiPageEvent {
		return &gitlab.WikiPageEvent{
			User:    &gitlab.EventUser{ID: 10, Username: "alice"},
			Project: gitlab.WikiPageEventProject{PathWithNamespace: "group/project"},
			ObjectAttributes: gitlab.WikiPageEventObjectAttributes{
				Title: "Runbook", Slug: slug, Action: action,
			},
		}
	}

	created := normalize(wiki("create", "runbook"), receivedAt)
	if len(created) != 1 || created[0].Kind != activity.KindWikiPageCreated {
		t.Fatalf("expected a created wiki page to count, got %v", kindsOf(created))
	}

	if created[0].DedupKey != "wiki_page:group/project:runbook" {
		t.Errorf("expected the page keyed on its project and slug, got %q", created[0].DedupKey)
	}

	for _, action := range []string{"update", "delete"} {
		if events := normalize(wiki(action, "runbook"), receivedAt); len(events) != 0 {
			t.Errorf("action %q: expected no activity for want of a stable key, got %v", action, kindsOf(events))
		}
	}

	if events := normalize(wiki("create", ""), receivedAt); len(events) != 0 {
		t.Errorf("expected a slugless page to yield nothing, got %v", kindsOf(events))
	}
}

func TestNormalize_UnhandledPayloadYieldsNothing(t *testing.T) {
	if events := normalize(&gitlab.ReleaseEvent{}, receivedAt); len(events) != 0 {
		t.Errorf("expected an event type this app tracks nothing for to yield nothing, got %v", kindsOf(events))
	}

	if events := normalize(nil, receivedAt); len(events) != 0 {
		t.Errorf("expected a nil payload to yield nothing, got %v", kindsOf(events))
	}
}

func TestParseEventTime_AcceptsGitLabsWebhookSpellings(t *testing.T) {
	cases := map[string]time.Time{
		"2026-03-01 09:00:00 UTC":  time.Date(2026, time.March, 1, 9, 0, 0, 0, time.UTC),
		"2026-03-01T09:00:00Z":     time.Date(2026, time.March, 1, 9, 0, 0, 0, time.UTC),
		"2026-03-01T09:00:00.000Z": time.Date(2026, time.March, 1, 9, 0, 0, 0, time.UTC),
	}

	for raw, want := range cases {
		got := parseEventTime(raw, receivedAt)
		if !got.Equal(want) {
			t.Errorf("%q: expected %v, got %v", raw, want, got)
		}
	}
}

func TestParseEventTime_FallsBackRatherThanDroppingTheActivity(t *testing.T) {
	for _, raw := range []string{"", "not a timestamp"} {
		if got := parseEventTime(raw, receivedAt); !got.Equal(receivedAt) {
			t.Errorf("%q: expected the fallback %v, got %v", raw, receivedAt, got)
		}
	}
}

func TestParseEventTime_KeepsTheOffsetGitLabReported(t *testing.T) {
	// The day and hour an activity falls on decide the day-based criteria,
	// so the instance's clock must not be shifted out from under them.
	got := parseEventTime("2026-03-01T23:30:00+02:00", receivedAt)

	if got.Hour() != 23 {
		t.Errorf("expected the reported hour 23 to survive, got %d", got.Hour())
	}
}
