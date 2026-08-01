package webhook

import (
	"testing"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"

	"github.com/boxboxjason/gitlab-achievements/internal/activity"
	"github.com/boxboxjason/gitlab-achievements/internal/eventsapi"
)

// These tests are the load-bearing ones for the periodic reconciliation
// sync, and they live here rather than in eventsapi because this is the
// side that can see both producers: webhook's normalizer is unexported, and
// it is the definition the other one has to match.
//
// The sync re-reads, through the Events API, windows of activity the live
// path has already counted. Every pass therefore observes the same activity
// twice, and the only thing standing between that and a permanently
// inflated counter is the two paths deriving byte-identical dedup keys.
// GitLab never revokes an award, so there is no recovering from getting
// this wrong; a failure here is not a style regression, it is data loss in
// the direction that cannot be undone.
//
// Where the two disagree, they must disagree by producing *nothing* on the
// read side rather than a different key: an activity the Events API cannot
// describe is undercounted, which is recoverable, while one it describes
// under another name is counted twice, which is not.

// agreesOn asserts that both producers derive the same key for the kind of
// activity a case is about.
func agreesOn(t *testing.T, kind activity.Kind, live []activity.Event, historical []activity.Event) {
	t.Helper()

	liveEvent := findKind(t, live, kind)
	historicalEvent := findKind(t, historical, kind)

	if liveEvent.DedupKey != historicalEvent.DedupKey {
		t.Errorf("%s would be counted twice: the live path keys it %q, the events api %q",
			kind, liveEvent.DedupKey, historicalEvent.DedupKey)
	}
}

func TestDedupKeysAgree_Push(t *testing.T) {
	live := normalize(&gitlab.PushEvent{
		ProjectID: 7, UserID: 10, UserUsername: "alice",
		Ref:               "refs/heads/main",
		Before:            "aaaaaaa",
		After:             "bbbbbbb",
		TotalCommitsCount: 3,
	}, receivedAt)

	historical := eventsapi.NormalizeProjectEvent(&gitlab.ProjectEvent{
		ID: 900, ProjectID: 7, AuthorID: 10, AuthorUsername: "alice",
		ActionName: "pushed to",
		PushData: gitlab.ProjectEventPushData{
			Action: "pushed", RefType: "branch", Ref: "main",
			CommitFrom: "aaaaaaa", CommitTo: "bbbbbbb", CommitCount: 3,
		},
	})

	agreesOn(t, activity.KindPush, live, historical)
	agreesOn(t, activity.KindCommit, live, historical)
}

func TestDedupKeysAgree_BranchCreation(t *testing.T) {
	live := normalize(&gitlab.PushEvent{
		ProjectID: 7, UserID: 10,
		Ref:               "refs/heads/feature",
		Before:            nullSHA,
		After:             "bbbbbbb",
		TotalCommitsCount: 1,
	}, receivedAt)

	historical := eventsapi.NormalizeProjectEvent(&gitlab.ProjectEvent{
		ID: 901, ProjectID: 7, AuthorID: 10, ActionName: "pushed new",
		PushData: gitlab.ProjectEventPushData{
			Action: "created", RefType: "branch", Ref: "feature",
			CommitTo: "bbbbbbb", CommitCount: 1,
		},
	})

	agreesOn(t, activity.KindPush, live, historical)
	agreesOn(t, activity.KindBranchCreated, live, historical)
	agreesOn(t, activity.KindCommit, live, historical)
}

// A ref deletion is where the two payloads describe the same thing most
// differently: a webhook reports the null SHA as the push's "after", and an
// event reports no commit_to at all.
func TestDedupKeysAgree_BranchDeletion(t *testing.T) {
	live := normalize(&gitlab.PushEvent{
		ProjectID: 7, UserID: 10,
		Ref:    "refs/heads/stale",
		Before: "aaaaaaa",
		After:  nullSHA,
	}, receivedAt)

	historical := eventsapi.NormalizeProjectEvent(&gitlab.ProjectEvent{
		ID: 902, ProjectID: 7, AuthorID: 10, ActionName: "deleted",
		PushData: gitlab.ProjectEventPushData{
			Action: "removed", RefType: "branch", Ref: "stale", CommitFrom: "aaaaaaa",
		},
	})

	agreesOn(t, activity.KindPush, live, historical)
}

func TestDedupKeysAgree_TagPush(t *testing.T) {
	live := normalize(&gitlab.TagEvent{
		ProjectID: 7, UserID: 10,
		Ref:    "refs/tags/v1.0.0",
		Before: nullSHA,
		After:  "bbbbbbb",
	}, receivedAt)

	historical := eventsapi.NormalizeProjectEvent(&gitlab.ProjectEvent{
		ID: 903, ProjectID: 7, AuthorID: 10, ActionName: "pushed new",
		PushData: gitlab.ProjectEventPushData{
			Action: "created", RefType: "tag", Ref: "v1.0.0", CommitTo: "bbbbbbb",
		},
	})

	agreesOn(t, activity.KindPush, live, historical)
	agreesOn(t, activity.KindTagCreated, live, historical)
}

// A tag names commits an earlier branch push already delivered, so neither
// path may count them. The Events API reports a commit_count on tag pushes
// anyway, and counting it would both inflate the release and, because the
// live path counts nothing, make a user's total depend on which path
// happened to observe the tag first.
func TestDedupKeysAgree_TagPushCountsNoCommitsOnEitherPath(t *testing.T) {
	live := normalize(&gitlab.TagEvent{
		ProjectID: 7, UserID: 10, Ref: "refs/tags/v1.0.0", Before: nullSHA, After: "bbbbbbb",
	}, receivedAt)

	historical := eventsapi.NormalizeProjectEvent(&gitlab.ProjectEvent{
		ID: 904, ProjectID: 7, AuthorID: 10, ActionName: "pushed new",
		PushData: gitlab.ProjectEventPushData{
			Action: "created", RefType: "tag", Ref: "v1.0.0",
			CommitTo: "bbbbbbb", CommitCount: 40,
		},
	})

	for _, event := range append(append([]activity.Event{}, live...), historical...) {
		if event.Kind == activity.KindCommit {
			t.Errorf("expected a tag push to count no commits, got %q", event.DedupKey)
		}
	}
}

func TestDedupKeysAgree_MergeRequestActions(t *testing.T) {
	tests := []struct {
		name       string
		liveAction string
		eventName  string
		kind       activity.Kind
	}{
		{"opened", "open", "opened", activity.KindMergeRequestOpened},
		{"merged, which the events api calls accepted", "merge", "accepted", activity.KindMergeRequestMerged},
		{"closed", "close", "closed", activity.KindMergeRequestClosed},
		{"approved", "approval", "approved", activity.KindMergeRequestApproved},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			live := normalize(&gitlab.MergeEvent{
				User: &gitlab.EventUser{ID: 10, Username: "alice"},
				ObjectAttributes: gitlab.MergeEventObjectAttributes{
					ID: 3300, TargetProjectID: 7, Action: tc.liveAction,
				},
			}, receivedAt)

			historical := eventsapi.NormalizeProjectEvent(&gitlab.ProjectEvent{
				ID: 905, ProjectID: 7, AuthorID: 10, AuthorUsername: "alice",
				TargetType: "MergeRequest", TargetID: 3300, ActionName: tc.eventName,
			})

			agreesOn(t, tc.kind, live, historical)
		})
	}
}

// An approval is keyed per approver on both paths, so the second reviewer
// on a merge request is counted and neither reviewer's approval collapses
// into the other's.
func TestDedupKeysAgree_ApprovalIsScopedToTheApprover(t *testing.T) {
	live := normalize(&gitlab.MergeEvent{
		User:             &gitlab.EventUser{ID: 11, Username: "bob"},
		ObjectAttributes: gitlab.MergeEventObjectAttributes{ID: 3300, Action: "approval"},
	}, receivedAt)

	historical := eventsapi.NormalizeProjectEvent(&gitlab.ProjectEvent{
		ID: 906, AuthorID: 11, TargetType: "MergeRequest", TargetID: 3300, ActionName: "approved",
	})

	agreesOn(t, activity.KindMergeRequestApproved, live, historical)

	other := eventsapi.NormalizeProjectEvent(&gitlab.ProjectEvent{
		ID: 907, AuthorID: 12, TargetType: "MergeRequest", TargetID: 3300, ActionName: "approved",
	})

	if historical[0].DedupKey == other[0].DedupKey {
		t.Errorf("expected two approvers of one merge request to be counted separately, both keyed %q",
			historical[0].DedupKey)
	}
}

func TestDedupKeysAgree_IssueActions(t *testing.T) {
	tests := []struct {
		liveAction string
		eventName  string
		targetType string
		kind       activity.Kind
	}{
		{"open", "opened", "Issue", activity.KindIssueOpened},
		{"close", "closed", "Issue", activity.KindIssueClosed},
		// Newer instances report an issue as a work item; the identifier
		// behind it is the same, so the key has to be too.
		{"open", "opened", "WorkItem", activity.KindIssueOpened},
	}

	for _, tc := range tests {
		t.Run(tc.targetType+" "+tc.liveAction, func(t *testing.T) {
			live := normalize(&gitlab.IssueEvent{
				User: &gitlab.EventUser{ID: 10, Username: "alice"},
				ObjectAttributes: gitlab.IssueEventObjectAttributes{
					ID: 4400, ProjectID: 7, Action: tc.liveAction,
				},
			}, receivedAt)

			historical := eventsapi.NormalizeProjectEvent(&gitlab.ProjectEvent{
				ID: 908, ProjectID: 7, AuthorID: 10,
				TargetType: tc.targetType, TargetID: 4400, ActionName: tc.eventName,
			})

			agreesOn(t, tc.kind, live, historical)
		})
	}
}

func TestDedupKeysAgree_Comment(t *testing.T) {
	live := normalize(&gitlab.IssueCommentEvent{
		User: &gitlab.User{ID: 10, Username: "alice"},
		ObjectAttributes: gitlab.IssueCommentEventObjectAttributes{
			ID: 5500, AuthorID: 10, ProjectID: 7, CreatedAt: "2026-03-04 11:00:00 UTC",
		},
	}, receivedAt)

	historical := eventsapi.NormalizeProjectEvent(&gitlab.ProjectEvent{
		ID: 909, ProjectID: 7, AuthorID: 10, AuthorUsername: "alice",
		TargetType: "Note", TargetID: 5500, ActionName: "commented on",
		Note: gitlab.ProjectEventNote{ID: 5500},
	})

	agreesOn(t, activity.KindComment, live, historical)
}

func TestDedupKeysAgree_Pipeline(t *testing.T) {
	live := normalize(&gitlab.PipelineEvent{
		User:             &gitlab.EventUser{ID: 10, Username: "alice"},
		Project:          gitlab.PipelineEventProject{ID: 7},
		ObjectAttributes: gitlab.PipelineEventObjectAttributes{ID: 6600, Status: "success"},
	}, receivedAt)

	createdAt := receivedAt
	historical := eventsapi.NormalizePipeline(&gitlab.Pipeline{
		ID: 6600, ProjectID: 7, Status: "success", CreatedAt: &createdAt,
		User: &gitlab.BasicUser{ID: 10, Username: "alice"},
	})

	agreesOn(t, activity.KindPipelineRun, live, historical)
	agreesOn(t, activity.KindPipelineSucceeded, live, historical)
}
