package eventsapi

import (
	"testing"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go/v3"

	"github.com/boxboxjason/gitlab-achievements/internal/activity"
)

// kindsOf reduces normalized activity to the kinds and weights a case
// cares about, so table expectations stay readable.
type kindWeight struct {
	kind   activity.Kind
	weight int64
}

func kindsOf(activities []activity.Event) []kindWeight {
	out := make([]kindWeight, 0, len(activities))
	for _, event := range activities {
		out = append(out, kindWeight{kind: event.Kind, weight: event.Weight()})
	}

	return out
}

func TestNormalizeProjectEvent(t *testing.T) {
	tests := []struct {
		name  string
		event *gitlab.ProjectEvent
		want  []kindWeight
	}{
		{
			name: "push to an existing branch counts its commits",
			event: &gitlab.ProjectEvent{
				ID: 1, AuthorID: 10, ActionName: "pushed to",
				PushData: gitlab.ProjectEventPushData{Action: "pushed", RefType: "branch", CommitCount: 4},
			},
			want: []kindWeight{{activity.KindPush, 1}, {activity.KindCommit, 4}},
		},
		{
			name: "push creating a branch counts both the branch and its commits",
			event: &gitlab.ProjectEvent{
				ID: 2, AuthorID: 10, ActionName: "pushed new",
				PushData: gitlab.ProjectEventPushData{Action: "created", RefType: "branch", CommitCount: 2},
			},
			want: []kindWeight{{activity.KindPush, 1}, {activity.KindBranchCreated, 1}, {activity.KindCommit, 2}},
		},
		{
			name: "push creating a tag",
			event: &gitlab.ProjectEvent{
				ID: 3, AuthorID: 10, ActionName: "pushed new",
				PushData: gitlab.ProjectEventPushData{Action: "created", RefType: "tag"},
			},
			want: []kindWeight{{activity.KindPush, 1}, {activity.KindTagCreated, 1}},
		},
		{
			name: "deleting a branch is still a push, but nothing was created",
			event: &gitlab.ProjectEvent{
				ID: 4, AuthorID: 10, ActionName: "deleted",
				PushData: gitlab.ProjectEventPushData{Action: "removed", RefType: "branch"},
			},
			want: []kindWeight{{activity.KindPush, 1}},
		},
		{
			name: "push GitLab 19 reports against the project as its target",
			event: &gitlab.ProjectEvent{
				ID: 17, AuthorID: 10, ActionName: "pushed new", TargetType: "Project",
				PushData: gitlab.ProjectEventPushData{Action: "created", RefType: "branch", CommitCount: 1},
			},
			want: []kindWeight{{activity.KindPush, 1}, {activity.KindBranchCreated, 1}, {activity.KindCommit, 1}},
		},
		{
			name:  "an event with neither a target type nor a ref is not a push",
			event: &gitlab.ProjectEvent{ID: 16, AuthorID: 10, ActionName: "joined"},
			want:  []kindWeight{},
		},
		{
			name:  "merge request opened",
			event: &gitlab.ProjectEvent{ID: 5, AuthorID: 10, TargetType: "MergeRequest", ActionName: "opened"},
			want:  []kindWeight{{activity.KindMergeRequestOpened, 1}},
		},
		{
			name:  "merge request merged, which GitLab calls accepted",
			event: &gitlab.ProjectEvent{ID: 6, AuthorID: 10, TargetType: "MergeRequest", ActionName: "accepted"},
			want:  []kindWeight{{activity.KindMergeRequestMerged, 1}},
		},
		{
			name:  "merge request approved",
			event: &gitlab.ProjectEvent{ID: 7, AuthorID: 10, TargetType: "MergeRequest", ActionName: "approved"},
			want:  []kindWeight{{activity.KindMergeRequestApproved, 1}},
		},
		{
			name:  "merge request closed without merging",
			event: &gitlab.ProjectEvent{ID: 8, AuthorID: 10, TargetType: "MergeRequest", ActionName: "closed"},
			want:  []kindWeight{{activity.KindMergeRequestClosed, 1}},
		},
		{
			name:  "issue opened",
			event: &gitlab.ProjectEvent{ID: 9, AuthorID: 10, TargetType: "Issue", ActionName: "opened"},
			want:  []kindWeight{{activity.KindIssueOpened, 1}},
		},
		{
			name:  "work item closed, the newer spelling of an issue",
			event: &gitlab.ProjectEvent{ID: 10, AuthorID: 10, TargetType: "WorkItem", ActionName: "closed"},
			want:  []kindWeight{{activity.KindIssueClosed, 1}},
		},
		{
			name:  "comment on a discussion",
			event: &gitlab.ProjectEvent{ID: 11, AuthorID: 10, TargetType: "DiscussionNote", ActionName: "commented on"},
			want:  []kindWeight{{activity.KindComment, 1}},
		},
		{
			name: "system note is GitLab talking to itself, not engagement",
			event: &gitlab.ProjectEvent{
				ID: 12, AuthorID: 10, TargetType: "Note", ActionName: "commented on",
				Note: gitlab.ProjectEventNote{System: true},
			},
			want: []kindWeight{},
		},
		{
			name:  "reopening is not tracked",
			event: &gitlab.ProjectEvent{ID: 13, AuthorID: 10, TargetType: "Issue", ActionName: "reopened"},
			want:  []kindWeight{},
		},
		{
			name:  "milestone activity is not tracked",
			event: &gitlab.ProjectEvent{ID: 14, AuthorID: 10, TargetType: "Milestone", ActionName: "opened"},
			want:  []kindWeight{},
		},
		{
			name:  "authorless activity has nobody to award",
			event: &gitlab.ProjectEvent{ID: 15, TargetType: "Issue", ActionName: "opened"},
			want:  []kindWeight{},
		},
		{
			name:  "nil event",
			event: nil,
			want:  []kindWeight{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := kindsOf(NormalizeProjectEvent(tc.event))

			if len(got) != len(tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}

			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("activity %d: expected %v, got %v", i, tc.want[i], got[i])
				}
			}
		})
	}
}

func TestNormalizeProjectEvent_DedupKeysAreStableAndPerKind(t *testing.T) {
	event := &gitlab.ProjectEvent{
		ID: 42, AuthorID: 10, ActionName: "pushed new",
		PushData: gitlab.ProjectEventPushData{Action: "created", RefType: "branch", CommitCount: 2},
	}

	first := NormalizeProjectEvent(event)
	second := NormalizeProjectEvent(event)

	if len(first) != 3 {
		t.Fatalf("expected 3 activities, got %d", len(first))
	}

	keys := map[string]bool{}
	for _, event := range first {
		if keys[event.DedupKey] {
			t.Errorf("expected the activities of one push to be separately deduplicable, %q repeated", event.DedupKey)
		}

		keys[event.DedupKey] = true
	}

	for i := range first {
		if first[i].DedupKey != second[i].DedupKey {
			t.Errorf("expected a stable dedup key across observations, got %q then %q", first[i].DedupKey, second[i].DedupKey)
		}
	}
}

func TestNormalizeProjectEvent_CarriesActorAndTimestamp(t *testing.T) {
	event := &gitlab.ProjectEvent{
		ID: 1, AuthorID: 10, ProjectID: 7, AuthorUsername: "alice",
		ActionName: "opened", TargetType: "Issue",
		CreatedAt: "2024-05-03T10:11:12.000Z",
	}

	activities := NormalizeProjectEvent(event)
	if len(activities) != 1 {
		t.Fatalf("expected 1 activity, got %d", len(activities))
	}

	got := activities[0]

	if got.ActorID != 10 || got.ActorUsername != "alice" || got.ProjectID != 7 {
		t.Errorf("expected the actor and project to be carried over, got %+v", got)
	}

	want := time.Date(2024, time.May, 3, 10, 11, 12, 0, time.UTC)
	if !got.OccurredAt.Equal(want) {
		t.Errorf("expected %s, got %s", want, got.OccurredAt)
	}
}

func TestNormalizeProjectEvent_FallsBackToTheEmbeddedAuthorUsername(t *testing.T) {
	event := &gitlab.ProjectEvent{
		ID: 1, AuthorID: 10, ActionName: "opened", TargetType: "Issue",
		Author: gitlab.BasicUser{ID: 10, Username: "bob"},
	}

	activities := NormalizeProjectEvent(event)
	if len(activities) != 1 {
		t.Fatalf("expected 1 activity, got %d", len(activities))
	}

	if activities[0].ActorUsername != "bob" {
		t.Errorf("expected the embedded author's username, got %q", activities[0].ActorUsername)
	}
}

func TestNormalizeProjectEvent_CarriesTheAuthorsAvatar(t *testing.T) {
	const avatar = "https://gitlab.example.com/uploads/bob.png"

	event := &gitlab.ProjectEvent{
		ID: 1, AuthorID: 10, ActionName: "opened", TargetType: "Issue",
		Author: gitlab.BasicUser{ID: 10, Username: "bob", AvatarURL: avatar},
	}

	activities := NormalizeProjectEvent(event)
	if len(activities) != 1 || activities[0].ActorAvatarURL != avatar {
		t.Fatalf("expected the author's avatar on the activity, got %+v", activities)
	}

	pipeline := NormalizePipeline(&gitlab.Pipeline{ID: 1, User: &gitlab.BasicUser{ID: 10, AvatarURL: avatar}})
	if len(pipeline) == 0 || pipeline[0].ActorAvatarURL != avatar {
		t.Fatalf("expected the triggering user's avatar on the pipeline, got %+v", pipeline)
	}
}

func TestNormalizeProjectEvent_UnparseableTimestampKeepsTheActivity(t *testing.T) {
	event := &gitlab.ProjectEvent{
		ID: 1, AuthorID: 10, ActionName: "opened", TargetType: "Issue",
		CreatedAt: "not a timestamp",
	}

	activities := NormalizeProjectEvent(event)
	if len(activities) != 1 {
		t.Fatalf("expected the activity to survive an unreadable timestamp, got %d", len(activities))
	}

	if !activities[0].OccurredAt.IsZero() {
		t.Errorf("expected the zero time, got %s", activities[0].OccurredAt)
	}
}

func TestNormalizePipeline(t *testing.T) {
	createdAt := time.Date(2024, time.May, 3, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		pipeline *gitlab.Pipeline
		want     []activity.Kind
	}{
		{
			name: "successful pipeline counts as both a run and a success",
			pipeline: &gitlab.Pipeline{
				ID: 1, ProjectID: 7, Status: "success", CreatedAt: &createdAt,
				User: &gitlab.BasicUser{ID: 10, Username: "alice"},
			},
			want: []activity.Kind{activity.KindPipelineRun, activity.KindPipelineSucceeded},
		},
		{
			name: "failed pipeline",
			pipeline: &gitlab.Pipeline{
				ID: 2, Status: "failed", User: &gitlab.BasicUser{ID: 10},
			},
			want: []activity.Kind{activity.KindPipelineRun, activity.KindPipelineFailed},
		},
		{
			name: "canceled pipeline still ran",
			pipeline: &gitlab.Pipeline{
				ID: 3, Status: "canceled", User: &gitlab.BasicUser{ID: 10},
			},
			want: []activity.Kind{activity.KindPipelineRun},
		},
		{
			name:     "pipeline with no triggering user has nobody to award",
			pipeline: &gitlab.Pipeline{ID: 4, Status: "success"},
			want:     []activity.Kind{},
		},
		{
			name:     "nil pipeline",
			pipeline: nil,
			want:     []activity.Kind{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			activities := NormalizePipeline(tc.pipeline)

			if len(activities) != len(tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, kindsOf(activities))
			}

			for i, kind := range tc.want {
				if activities[i].Kind != kind {
					t.Errorf("activity %d: expected %q, got %q", i, kind, activities[i].Kind)
				}
			}
		})
	}
}

func TestNormalizePipeline_CarriesActorAndTimestamp(t *testing.T) {
	createdAt := time.Date(2024, time.May, 3, 10, 0, 0, 0, time.UTC)
	pipeline := &gitlab.Pipeline{
		ID: 1, ProjectID: 7, Status: "success", CreatedAt: &createdAt,
		User: &gitlab.BasicUser{ID: 10, Username: "alice"},
	}

	activities := NormalizePipeline(pipeline)

	if activities[0].ActorID != 10 || activities[0].ActorUsername != "alice" || activities[0].ProjectID != 7 {
		t.Errorf("expected the triggering user and project to be carried over, got %+v", activities[0])
	}

	if !activities[0].OccurredAt.Equal(createdAt) {
		t.Errorf("expected %s, got %s", createdAt, activities[0].OccurredAt)
	}
}

func TestNormalizeProjectEvent_KeyShapes(t *testing.T) {
	tests := []struct {
		name  string
		event *gitlab.ProjectEvent
		kind  activity.Kind
		want  string
	}{
		{
			name: "a push is keyed on the ref it moved and the commit it moved it to",
			event: &gitlab.ProjectEvent{
				ID: 1, ProjectID: 7, AuthorID: 10, ActionName: "pushed to",
				PushData: gitlab.ProjectEventPushData{
					Action: "pushed", RefType: "branch", Ref: "main", CommitTo: "abc",
				},
			},
			kind: activity.KindPush,
			want: "push:7:refs/heads/main:abc:push",
		},
		{
			name: "a tag push is keyed apart from a branch push",
			event: &gitlab.ProjectEvent{
				ID: 2, ProjectID: 7, AuthorID: 10, ActionName: "pushed new",
				PushData: gitlab.ProjectEventPushData{
					Action: "created", RefType: "tag", Ref: "v1", CommitTo: "abc",
				},
			},
			kind: activity.KindTagCreated,
			want: "tag_push:7:refs/tags/v1:abc:tag_created",
		},
		{
			name: "a ref an instance already qualified is not qualified twice",
			event: &gitlab.ProjectEvent{
				ID: 3, ProjectID: 7, AuthorID: 10, ActionName: "pushed to",
				PushData: gitlab.ProjectEventPushData{
					Action: "pushed", RefType: "branch", Ref: "refs/heads/main", CommitTo: "abc",
				},
			},
			kind: activity.KindPush,
			want: "push:7:refs/heads/main:abc:push",
		},
		{
			name: "a merge request is keyed on the merge request, not on the event",
			event: &gitlab.ProjectEvent{
				ID: 4, ProjectID: 7, AuthorID: 10,
				TargetType: "MergeRequest", TargetID: 3300, ActionName: "accepted",
			},
			kind: activity.KindMergeRequestMerged,
			want: "merge_request:3300:merged",
		},
		{
			name: "an issue is keyed on the issue",
			event: &gitlab.ProjectEvent{
				ID: 5, ProjectID: 7, AuthorID: 10,
				TargetType: "Issue", TargetID: 4400, ActionName: "opened",
			},
			kind: activity.KindIssueOpened,
			want: "issue:4400:opened",
		},
		{
			name: "a comment is keyed on the note",
			event: &gitlab.ProjectEvent{
				ID: 6, ProjectID: 7, AuthorID: 10,
				TargetType: "Note", TargetID: 5500, ActionName: "commented on",
				Note: gitlab.ProjectEventNote{ID: 5500},
			},
			kind: activity.KindComment,
			want: "note:5500",
		},
		{
			name: "a note reported without an id of its own falls back to the target",
			event: &gitlab.ProjectEvent{
				ID: 7, ProjectID: 7, AuthorID: 10,
				TargetType: "Note", TargetID: 5501, ActionName: "commented on",
			},
			kind: activity.KindComment,
			want: "note:5501",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, got := range NormalizeProjectEvent(tc.event) {
				if got.Kind != tc.kind {
					continue
				}

				if got.DedupKey != tc.want {
					t.Errorf("expected %q, got %q", tc.want, got.DedupKey)
				}

				return
			}

			t.Fatalf("expected a %q activity, got %v", tc.kind, kindsOf(NormalizeProjectEvent(tc.event)))
		})
	}
}

func TestNormalizeProjectEvent_TagPushCountsNoCommits(t *testing.T) {
	activities := NormalizeProjectEvent(&gitlab.ProjectEvent{
		ID: 1, ProjectID: 7, AuthorID: 10, ActionName: "pushed new",
		PushData: gitlab.ProjectEventPushData{
			Action: "created", RefType: "tag", Ref: "v1", CommitTo: "abc", CommitCount: 40,
		},
	})

	for _, got := range activities {
		if got.Kind == activity.KindCommit {
			t.Errorf("expected a tag to count no commits, it names ones an earlier push delivered")
		}
	}
}

func TestNormalizeMergedMergeRequest_LandsForTheAuthor(t *testing.T) {
	mergedAt := time.Date(2026, time.March, 4, 12, 30, 0, 0, time.UTC)

	activities := NormalizeMergedMergeRequest(&gitlab.BasicMergeRequest{
		ID: 55, ProjectID: 1, MergedAt: &mergedAt,
		Author:    &gitlab.BasicUser{ID: 99, Username: "carol", AvatarURL: "https://gitlab.example.com/c.png"},
		MergeUser: &gitlab.BasicUser{ID: 10, Username: "alice"},
	})

	if len(activities) != 1 {
		t.Fatalf("expected one landing, got %v", kindsOf(activities))
	}

	got := activities[0]
	if got.Kind != activity.KindMergeRequestLanded || got.ActorID != 99 || got.ActorUsername != "carol" {
		t.Errorf("expected the landing credited to carol, got %+v", got)
	}

	// The webhook path's key for the same merge.
	if got.DedupKey != "merge_request:55:landed" {
		t.Errorf("expected the webhook path's key, got %q", got.DedupKey)
	}

	if !got.OccurredAt.Equal(mergedAt) || got.ProjectID != 1 {
		t.Errorf("expected the merge time and project, got %v in %d", got.OccurredAt, got.ProjectID)
	}
}

func TestNormalizeMergedMergeRequest_NeedsAMergeAndAnAuthor(t *testing.T) {
	mergedAt := time.Date(2026, time.March, 4, 12, 30, 0, 0, time.UTC)

	cases := map[string]*gitlab.BasicMergeRequest{
		"nil":        nil,
		"not merged": {ID: 55, Author: &gitlab.BasicUser{ID: 99}},
		"no author":  {ID: 55, MergedAt: &mergedAt},
		"author 0":   {ID: 55, MergedAt: &mergedAt, Author: &gitlab.BasicUser{}},
	}

	for name, mr := range cases {
		if got := NormalizeMergedMergeRequest(mr); len(got) != 0 {
			t.Errorf("%s: expected nothing, got %v", name, kindsOf(got))
		}
	}
}

func diffNoteEvent(noteableType string) *gitlab.ProjectEvent {
	return &gitlab.ProjectEvent{
		ID: 910, ProjectID: 7, AuthorID: 10, AuthorUsername: "alice",
		TargetType: "DiffNote", TargetID: 5501, ActionName: "commented on",
		Note: gitlab.ProjectEventNote{ID: 5501, NoteableType: noteableType},
	}
}

func TestNormalizeProjectEvent_AMergeRequestDiffNoteIsAReviewComment(t *testing.T) {
	activities := NormalizeProjectEvent(diffNoteEvent("MergeRequest"))

	if len(activities) != 2 || activities[0].Kind != activity.KindComment || activities[1].Kind != activity.KindReviewComment {
		t.Fatalf("expected a comment and a review comment, got %v", kindsOf(activities))
	}

	if activities[1].DedupKey != "note:5501:review_comment" {
		t.Errorf("expected the review keyed on the note, got %q", activities[1].DedupKey)
	}
}

func TestNormalizeProjectEvent_ACommitDiffNoteIsOnlyAComment(t *testing.T) {
	// The webhook path can't tell a commit's diff notes from its other
	// notes, so neither path counts them as reviews.
	activities := NormalizeProjectEvent(diffNoteEvent("Commit"))

	if len(activities) != 1 || activities[0].Kind != activity.KindComment {
		t.Errorf("expected only a comment, got %v", kindsOf(activities))
	}
}

func TestNormalizeProjectEvent_ADiscussionNoteIsNoReview(t *testing.T) {
	event := diffNoteEvent("MergeRequest")
	event.TargetType = "DiscussionNote"

	if activities := NormalizeProjectEvent(event); len(activities) != 1 {
		t.Errorf("expected only a comment, got %v", kindsOf(activities))
	}
}
