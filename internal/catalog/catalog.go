// Package catalog defines the achievement definitions this app maintains in
// GitLab.
//
// The catalog is built from stacking templates ported from the VS Code
// Achievements extension this project grew out of: one criteria, one
// difficulty curve, and a run of tiers generated off it (Committer I, II,
// III, ...). See Template and Curve for the shape and the pacing.
//
// What differs from the extension is what an achievement is. GitLab's
// Achievements API has no notion of a tier, a level, or EXP, so every tier
// is a separate GitLab achievement object, and the progression only exists
// in this app's database and in the achievements' names.
package catalog

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

//go:embed assets/*.png
var avatarAssets embed.FS

// The criteria keys the catalog is written against: the shared vocabulary
// tying a normalized activity kind (see internal/activity) to the counter
// its occurrences accumulate in, and to the tiers earned off that counter.
//
// These are the subset of the VS Code extension's criteria a GitLab server
// can observe, plus the ones with no extension counterpart at all that the
// hooks this app registers hand it for free (deployments, jobs, emoji
// reactions, wiki pages). Editor-local criteria (lines of code, files
// created, tabs, extensions, debugger sessions, time spent) have no
// server-side equivalent and are deliberately absent; see templates for the
// git criteria that are missing for subtler reasons, and for the hooks
// whose payloads name no user to credit.
//
// Not every criteria is observable by both activity producers. The
// historical backfill reads GitLab's Events API, which reports pushes,
// merge requests, issues, notes and wiki pages but not deployments, jobs,
// emoji, discussion resolutions, or fast merges. Those advance from live
// webhook deliveries only, so on a freshly bootstrapped instance they start
// from zero however long the instance has existed.
const (
	CriteriaCommits                 = "commits"
	CriteriaPushes                  = "pushes"
	CriteriaBranchesCreated         = "branches_created"
	CriteriaTagsCreated             = "tags_created"
	CriteriaReleasesPublished       = "releases_published"
	CriteriaMergeRequestsOpened     = "merge_requests_opened"
	CriteriaMergeRequestsMerged     = "merge_requests_merged"
	CriteriaMergeRequestsLanded     = "merge_requests_landed"
	CriteriaMergeRequestsApproved   = "merge_requests_approved"
	CriteriaMergeRequestsClosed     = "merge_requests_closed"
	CriteriaMergeRequestsMergedFast = "merge_requests_merged_fast"
	CriteriaComments                = "comments"
	CriteriaReviewComments          = "review_comments"
	CriteriaDiscussionsResolved     = "discussions_resolved"
	CriteriaIssuesOpened            = "issues_opened"
	CriteriaIssuesClosed            = "issues_closed"
	CriteriaPipelinesRun            = "pipelines_run"
	CriteriaPipelinesSucceeded      = "pipelines_succeeded"
	CriteriaPipelinesFailed         = "pipelines_failed"
	CriteriaPipelinesFixed          = "pipelines_fixed"
	CriteriaJobsRun                 = "jobs_run"
	CriteriaDeployments             = "deployments"
	CriteriaDeploymentsSucceeded    = "deployments_succeeded"
	CriteriaEmojiAwarded            = "emoji_awarded"
	CriteriaWikiPagesCreated        = "wiki_pages_created"
	CriteriaProjectsContributed     = "projects_contributed"
	CriteriaActiveDays              = "active_days"
	CriteriaActivityStreak          = "activity_streak"
	CriteriaNightOwlDays            = "night_owl_days"
	CriteriaEarlyBirdDays           = "early_bird_days"
)

// Categories group related criteria for display, in the order they are
// meant to be shown.
const (
	CategoryGit           = "git"
	CategoryMergeRequests = "merge_requests"
	CategoryIssues        = "issues"
	CategoryCICD          = "ci_cd"
	CategoryCollaboration = "collaboration"
	CategoryEngagement    = "engagement"
)

// Criteria describes one criteria as a whole, apart from its tiers: what to
// call the stack, which group it belongs to, and the image every tier
// shares, empty when it has none.
type Criteria struct {
	Key        string
	Category   string
	Title      string
	AvatarPath string
}

// AllCriteria lists every criteria in the catalog, in catalog order.
func AllCriteria() []Criteria {
	criteria := make([]Criteria, 0, len(templates))

	for _, template := range templates {
		criteria = append(criteria, Criteria{
			Key:        template.CriteriaKey,
			Category:   template.Category,
			Title:      strings.TrimSpace(fmt.Sprintf(template.Title, "")),
			AvatarPath: template.AvatarPath,
		})
	}

	return criteria
}

// OpenAvatar opens an embedded avatar asset by the path a definition
// records for it.
func OpenAvatar(path string) (fs.File, error) {
	f, err := avatarAssets.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open avatar asset %q: %w", path, err)
	}

	return f, nil
}

// Entry describes one tier of one achievement criteria: what GitLab shows
// (Name, Description, avatar) and the local progress threshold (Tier,
// Threshold) that earns it, keyed by CriteriaKey, the identifier the
// achievement rule engine reports progress against.
//
// Exp is the one field with no GitLab counterpart at all. A GitLab
// achievement carries no points, level, or tier, so the reward a tier is
// worth is only ever written to this app's own database, where the engine
// adds it to the holder's total.
type Entry struct {
	CriteriaKey string
	Name        string
	Description string
	AvatarPath  string
	Tier        int64
	Threshold   int64
	Exp         int64
}

// HasAvatar reports whether entry has an avatar image configured.
func (e Entry) HasAvatar() bool {
	return e.AvatarPath != ""
}

// Avatar opens entry's avatar image. The caller must close the returned
// file, and must only call this when HasAvatar reports true.
func (e Entry) Avatar() (fs.File, error) {
	f, err := OpenAvatar(e.AvatarPath)
	if err != nil {
		return nil, fmt.Errorf("%s tier %d: %w", e.CriteriaKey, e.Tier, err)
	}

	return f, nil
}

// V1 returns every achievement the app maintains, one Entry per tier of
// per criteria.
//
// It is generated from the templates rather than written out by hand, so
// adding a criteria means adding one template and adjusting nothing else,
// and the whole set stays on the same difficulty pacing. Bootstrap
// reconciles the result idempotently, so the size of the catalog costs a
// burst of GraphQL calls on the very first run and nothing thereafter.
func V1() []Entry {
	entries := make([]Entry, 0, len(templates)*(maxTier+1))

	for _, template := range templates {
		entries = append(entries, template.Expand()...)
	}

	return entries
}
