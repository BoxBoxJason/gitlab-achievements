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
