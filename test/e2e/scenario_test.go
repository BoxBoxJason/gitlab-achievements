//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go/v3"
)

// fixture is what the scenario runs against: two ordinary users who are
// Maintainers of one group-owned project, so that every action in the
// scenario is somebody's to perform.
type fixture struct {
	gl        *gitlab.Client
	gitlabURL string
	token     string
	alice     *gitlab.User
	bob       *gitlab.User
	project   *gitlab.Project
	stamp     string
}

// as acts as user, through the admin token's sudo scope. That is what puts
// the user, rather than the admin, on every webhook payload and event the
// action produces.
func as(user *gitlab.User) gitlab.RequestOptionFunc {
	return gitlab.WithSudo(user.ID)
}

// newFixture creates the users, group and project the scenario runs in,
// and the namespace the app keeps its achievements in. Everything is
// suffixed with stamp, so the suite can run twice against one instance.
func newFixture(t *testing.T, gl *gitlab.Client, gitlabURL, token, stamp string) *fixture {
	t.Helper()

	f := &fixture{gl: gl, gitlabURL: gitlabURL, token: token, stamp: stamp}

	// The app listens on the host GitLab runs next to, which GitLab refuses
	// to deliver to unless told otherwise.
	_, _, err := gl.Settings.UpdateSettings(&gitlab.UpdateSettingsOptions{
		AllowLocalRequestsFromWebHooksAndServices: new(true),
	})
	must(t, err)

	f.alice = f.createUser(t, "alice")
	f.bob = f.createUser(t, "bob")

	group, _, err := gl.Groups.CreateGroup(&gitlab.CreateGroupOptions{
		Name: new("e2e " + stamp), Path: new("e2e-" + stamp),
	})
	must(t, err)

	for _, user := range []*gitlab.User{f.alice, f.bob} {
		_, _, err = gl.GroupMembers.AddGroupMember(group.ID, &gitlab.AddGroupMemberOptions{
			UserID: new(user.ID), AccessLevel: new(gitlab.MaintainerPermissions),
		})
		must(t, err)
	}

	f.project, _, err = gl.Projects.CreateProject(&gitlab.CreateProjectOptions{
		Name:                 new("playground"),
		NamespaceID:          new(group.ID),
		InitializeWithReadme: new(true),
		DefaultBranch:        new("main"),
		Visibility:           new(gitlab.PrivateVisibility),
	})
	must(t, err)

	return f
}

func (f *fixture) createUser(t *testing.T, name string) *gitlab.User {
	t.Helper()

	username := name + "-" + f.stamp

	user, _, err := f.gl.Users.CreateUser(&gitlab.CreateUserOptions{
		Email:            new(username + "@example.com"),
		Username:         new(username),
		Name:             new(name),
		Password:         new(randomHex(t)),
		SkipConfirmation: new(true),
	})
	must(t, err)

	return user
}

// expectation is one counter the scenario must have moved.
//
// exact distinguishes the counters the scenario controls precisely from
// the ones GitLab inflates on its own (a merge is itself a push, carrying
// every commit it brings in), where only a floor can be asserted. An exact
// count is the stronger check: it catches a criteria counting the same
// action twice as well as not at all.
type expectation struct {
	user     string
	criteria string
	want     int64
	exact    bool
	// webhookOnly marks the criteria the Events API has no record of, which
	// the backfill and reconcile phases therefore cannot be held to.
	webhookOnly bool
	// what describes the action behind it, for the report.
	what string
}

// run performs one of everything the catalog tracks and returns what each
// user's counters should read afterwards.
//
// The order is deliberate. CI is configured last, so the branches and the
// merge before it run no pipelines and the pipeline counts stay exact.
func (f *fixture) run(t *testing.T) []expectation {
	t.Helper()

	gl, pid := f.gl, f.project.ID
	alice, bob := f.alice, f.bob

	var exp []expectation

	expect := func(user *gitlab.User, criteria string, want int64, exact, webhookOnly bool, what string) {
		exp = append(exp, expectation{
			user: user.Username, criteria: criteria, want: want, exact: exact, webhookOnly: webhookOnly, what: what,
		})
	}

	// Git: a branch, a commit on it, and later a tag.
	_, _, err := gl.Branches.CreateBranch(pid, &gitlab.CreateBranchOptions{Branch: new("feat-a"), Ref: new("main")}, as(alice))
	must(t, err)
	expect(alice, "branches_created", 1, true, false, "create branch feat-a")

	f.commit(t, alice, "feat-a", "a.txt", "alice was here")
	expect(alice, "commits", 1, false, false, "commit on feat-a")

	// Merge requests and review.
	mr, _, err := gl.MergeRequests.CreateMergeRequest(pid, &gitlab.CreateMergeRequestOptions{
		Title: new("Add a.txt"), SourceBranch: new("feat-a"), TargetBranch: new("main"),
	}, as(alice))
	must(t, err)
	expect(alice, "merge_requests_opened", 1, true, false, "open MR !1")

	mrNote, _, err := gl.Notes.CreateMergeRequestNote(pid, mr.IID, &gitlab.CreateMergeRequestNoteOptions{Body: new("Looks good")}, as(bob))
	must(t, err)

	discussion, _, err := gl.Discussions.CreateMergeRequestDiscussion(pid, mr.IID,
		&gitlab.CreateMergeRequestDiscussionOptions{Body: new("One nit before this goes in")}, as(bob))
	must(t, err)

	f.diffNote(t, bob, mr.IID, "a.txt", "Typo on this line")
	expect(bob, "review_comments", 1, true, false, "line comment on MR !1's diff")

	// The line comment's thread stays open, so GitLab 19.5 sends no hook
	// for this resolution at all: it is only seen when the merge request is
	// merged, further down.
	_, _, err = gl.Discussions.ResolveMergeRequestDiscussion(pid, mr.IID, discussion.ID,
		&gitlab.ResolveMergeRequestDiscussionOptions{Resolved: new(true)}, as(alice))
	must(t, err)
	expect(alice, "discussions_resolved", 1, true, true, "resolve bob's MR thread, another left open")

	_, _, err = gl.AwardEmoji.CreateMergeRequestAwardEmojiOnNote(pid, mr.IID, mrNote.ID,
		&gitlab.CreateAwardEmojiOptions{Name: "thumbsup"}, as(alice))
	must(t, err)
	expect(alice, "emoji_awarded", 1, true, true, "react to bob's MR comment")

	_, _, err = gl.AwardEmoji.CreateMergeRequestAwardEmoji(pid, mr.IID, &gitlab.CreateAwardEmojiOptions{Name: "rocket"}, as(bob))
	must(t, err)

	_, _, err = gl.MergeRequestApprovals.ApproveMergeRequest(pid, mr.IID, nil, as(bob))
	must(t, err)
	expect(bob, "merge_requests_approved", 1, true, false, "approve MR !1")

	// Bob merges, so the merge and the landing go to different people.
	f.merge(t, bob, mr.IID)
	expect(bob, "merge_requests_merged", 1, true, false, "merge MR !1")
	expect(bob, "merge_requests_merged_fast", 1, true, true, "merge MR !1 within the hour")
	expect(alice, "merge_requests_landed", 1, true, false, "her MR !1 merged by bob")

	_, _, err = gl.Tags.CreateTag(pid, &gitlab.CreateTagOptions{TagName: new("v0.1.0"), Ref: new("main")}, as(alice))
	must(t, err)
	expect(alice, "tags_created", 1, true, false, "create tag v0.1.0")

	_, _, err = gl.Releases.CreateRelease(pid, &gitlab.CreateReleaseOptions{
		TagName: new("v0.1.0"), Name: new("First"), Description: new("The first one."),
	}, as(alice))
	must(t, err)
	expect(alice, "releases_published", 1, true, false, "publish release v0.1.0")

	// A second merge request, closed without merging.
	_, _, err = gl.Branches.CreateBranch(pid, &gitlab.CreateBranchOptions{Branch: new("feat-b"), Ref: new("main")}, as(bob))
	must(t, err)
	expect(bob, "branches_created", 1, true, false, "create branch feat-b")

	f.commit(t, bob, "feat-b", "b.txt", "bob was here")
	expect(bob, "commits", 1, false, false, "commit on feat-b")
	expect(bob, "pushes", 3, false, false, "merge of MR !1, branch feat-b + commit")

	mr2, _, err := gl.MergeRequests.CreateMergeRequest(pid, &gitlab.CreateMergeRequestOptions{
		Title: new("Add b.txt"), SourceBranch: new("feat-b"), TargetBranch: new("main"),
	}, as(bob))
	must(t, err)
	expect(bob, "merge_requests_opened", 1, true, false, "open MR !2")

	_, _, err = gl.MergeRequests.UpdateMergeRequest(pid, mr2.IID, &gitlab.UpdateMergeRequestOptions{StateEvent: new("close")}, as(bob))
	must(t, err)
	expect(bob, "merge_requests_closed", 1, true, false, "close MR !2")

	// Issues.
	issue, _, err := gl.Issues.CreateIssue(pid, &gitlab.CreateIssueOptions{Title: new("Something is off")}, as(alice))
	must(t, err)
	expect(alice, "issues_opened", 1, true, false, "open issue #1")

	_, _, err = gl.Notes.CreateIssueNote(pid, issue.IID, &gitlab.CreateIssueNoteOptions{Body: new("Repro attached")}, as(alice))
	must(t, err)

	_, _, err = gl.AwardEmoji.CreateIssueAwardEmoji(pid, issue.IID, &gitlab.CreateAwardEmojiOptions{Name: "eyes"}, as(bob))
	must(t, err)
	expect(bob, "emoji_awarded", 2, true, true, "react to MR !1 and issue #1")

	_, _, err = gl.Issues.UpdateIssue(pid, issue.IID, &gitlab.UpdateIssueOptions{StateEvent: new("close")}, as(bob))
	must(t, err)
	expect(bob, "issues_closed", 1, true, false, "close issue #1")

	// Comments on the two remaining things one can comment on.
	head := f.head(t, "main")

	_, _, err = gl.Commits.PostCommitComment(pid, head, &gitlab.PostCommitCommentOptions{Note: new("Nice")}, as(alice))
	must(t, err)
	expect(alice, "comments", 2, true, false, "issue #1 comment + commit comment")

	snippet, _, err := gl.ProjectSnippets.CreateSnippet(pid, &gitlab.CreateProjectSnippetOptions{
		Title:      new("scratch"),
		Visibility: new(gitlab.PrivateVisibility),
		Files:      &[]*gitlab.CreateSnippetFileOptions{{FilePath: new("s.txt"), Content: new("hi")}},
	}, as(bob))
	must(t, err)

	_, _, err = gl.Notes.CreateSnippetNote(pid, snippet.ID, &gitlab.CreateSnippetNoteOptions{Body: new("Handy")}, as(bob))
	must(t, err)
	expect(bob, "comments", 4, true, false, "MR !1 comment, thread and line comment + snippet comment")

	// Wiki.
	_, _, err = gl.Wikis.CreateWikiPage(pid, &gitlab.CreateWikiPageOptions{Title: new("Home"), Content: new("Welcome")}, as(alice))
	must(t, err)
	expect(alice, "wiki_pages_created", 1, true, false, "create wiki page")

	// Deployments, recorded through the API: moving one from running to
	// success is the transition GitLab delivers a hook for.
	deployment, _, err := gl.Deployments.CreateProjectDeployment(pid, &gitlab.CreateProjectDeploymentOptions{
		Environment: new("production"), Ref: new("main"), SHA: new(f.head(t, "main")), Tag: new(false),
		Status: new(gitlab.DeploymentStatusRunning),
	}, as(alice))
	must(t, err)

	_, _, err = gl.Deployments.UpdateProjectDeployment(pid, deployment.ID, &gitlab.UpdateProjectDeploymentOptions{
		Status: new(gitlab.DeploymentStatusSuccess),
	}, as(alice))
	must(t, err)
	expect(alice, "deployments", 1, true, true, "deploy to production")
	expect(alice, "deployments_succeeded", 1, true, true, "deployment succeeds")

	// CI, last: three pipelines on main, passing, failing and passing again,
	// run by a runner this suite impersonates.
	f.runPipelines(t, alice)
	expect(alice, "pipelines_run", 3, true, false, "three pipelines")
	expect(alice, "pipelines_succeeded", 2, true, false, "the first and the third pass")
	expect(alice, "pipelines_failed", 1, true, false, "the second fails")
	expect(alice, "pipelines_fixed", 1, true, false, "the third passes after the second failed")
	expect(alice, "jobs_run", 3, true, true, "one job per pipeline")

	// Branch, commit, tag, and the three CI commits.
	expect(alice, "pushes", 6, false, false, "branch, commit, tag, 3 CI commits")

	for _, user := range []*gitlab.User{alice, bob} {
		expect(user, "active_days", 1, true, false, "active today")
		expect(user, "activity_streak", 1, true, false, "one-day streak")
		expect(user, "projects_contributed", 1, true, false, "everything happened in one project")
	}

	return exp
}

func (f *fixture) commit(t *testing.T, user *gitlab.User, branch, path, content string) {
	t.Helper()

	_, _, err := f.gl.Commits.CreateCommit(f.project.ID, &gitlab.CreateCommitOptions{
		Branch:        new(branch),
		CommitMessage: new("Add " + path),
		Actions: []*gitlab.CommitActionOptions{{
			Action: new(gitlab.FileCreate), FilePath: new(path), Content: new(content),
		}},
	}, as(user))
	must(t, err)
}

// diffNote comments on the first line of path in a merge request's diff,
// once GitLab has worked out the diff: right after creation its refs are
// still being computed, and a position needs them.
func (f *fixture) diffNote(t *testing.T, user *gitlab.User, iid int64, path, body string) {
	t.Helper()

	for range 60 {
		mr, _, err := f.gl.MergeRequests.GetMergeRequest(f.project.ID, iid, nil)
		must(t, err)

		refs := mr.DiffRefs
		if refs.HeadSha == "" || refs.BaseSha == "" {
			time.Sleep(2 * time.Second)

			continue
		}

		_, _, err = f.gl.Discussions.CreateMergeRequestDiscussion(f.project.ID, iid, &gitlab.CreateMergeRequestDiscussionOptions{
			Body: new(body),
			Position: &gitlab.PositionOptions{
				PositionType: new("text"),
				BaseSHA:      new(refs.BaseSha), StartSHA: new(refs.StartSha), HeadSHA: new(refs.HeadSha),
				NewPath: new(path), OldPath: new(path), NewLine: new(int64(1)),
			},
		}, as(user))
		must(t, err)

		return
	}

	t.Fatalf("the diff of !%d was never ready to comment on", iid)
}

func (f *fixture) head(t *testing.T, branch string) string {
	t.Helper()

	b, _, err := f.gl.Branches.GetBranch(f.project.ID, branch)
	must(t, err)

	return b.Commit.ID
}

// merge merges a merge request once GitLab has worked out that it can be:
// right after creation the mergeability check is still pending, and an
// accept then is refused.
func (f *fixture) merge(t *testing.T, user *gitlab.User, iid int64) {
	t.Helper()

	var lastErr error

	for range 60 {
		_, _, lastErr = f.gl.MergeRequests.AcceptMergeRequest(f.project.ID, iid, nil, as(user))
		if lastErr == nil {
			return
		}

		time.Sleep(2 * time.Second)
	}

	t.Fatalf("merge of !%d never went through: %v", iid, lastErr)
}

// runPipelines adds CI configuration, pushes twice more, and plays the
// part of a runner for the three pipelines that produces: the first
// passes, the second fails, and the third passes again, fixing the build.
//
// Impersonating a runner through its API rather than running one keeps the
// suite to one container, and makes the outcomes exact. Each pipeline is
// finished before the next push, because GitLab cancels a pending pipeline
// as redundant once a newer one lands on the same branch.
func (f *fixture) runPipelines(t *testing.T, user *gitlab.User) {
	t.Helper()

	runner, _, err := f.gl.Users.CreateUserRunner(&gitlab.CreateUserRunnerOptions{
		RunnerType: new("project_type"), ProjectID: new(f.project.ID), RunUntagged: new(true),
	})
	must(t, err)

	pushes := []func(){
		func() { f.commit(t, user, "main", ".gitlab-ci.yml", "test:\n  script: [\"true\"]\n") },
		func() { f.commit(t, user, "main", "c.txt", "second pipeline") },
		func() { f.commit(t, user, "main", "d.txt", "third pipeline") },
	}

	for i, state := range []string{"success", "failed", "success"} {
		pushes[i]()

		job := f.requestJob(t, runner.Token)

		body := map[string]any{"token": job.Token, "state": state}
		if state == "failed" {
			body["failure_reason"] = "script_failure"
		}

		status := f.runnerAPI(t, http.MethodPut, fmt.Sprintf("/api/v4/jobs/%d", job.ID), body, nil)
		if status != http.StatusOK {
			t.Fatalf("finishing job %d (%d of %d) as %s: HTTP %d", job.ID, i+1, len(pushes), state, status)
		}
	}
}

type runnerJob struct {
	ID    int64  `json:"id"`
	Token string `json:"token"`
}

// requestJob asks for the next pending job the way a runner would, waiting
// for GitLab to have queued one.
func (f *fixture) requestJob(t *testing.T, runnerToken string) runnerJob {
	t.Helper()

	body := map[string]any{
		"token": runnerToken,
		"info":  map[string]any{"name": "e2e", "version": "18.0.0", "executor": "shell", "features": map[string]any{}},
	}

	for range 90 {
		var job runnerJob

		status := f.runnerAPI(t, http.MethodPost, "/api/v4/jobs/request", body, &job)
		if status == http.StatusCreated {
			return job
		}

		time.Sleep(2 * time.Second)
	}

	t.Fatal("no CI job was ever handed to the runner")

	return runnerJob{}
}

func (f *fixture) runnerAPI(t *testing.T, method, path string, body, out any) int {
	t.Helper()

	payload, err := json.Marshal(body)
	must(t, err)

	req, err := http.NewRequestWithContext(t.Context(), method, strings.TrimSuffix(f.gitlabURL, "/")+path, bytes.NewReader(payload))
	must(t, err)

	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	must(t, err)
	defer resp.Body.Close()

	if out != nil && resp.StatusCode < http.StatusMultipleChoices {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}

	return resp.StatusCode
}

func must(t *testing.T, err error) {
	t.Helper()

	if err != nil {
		t.Fatal(err)
	}
}
