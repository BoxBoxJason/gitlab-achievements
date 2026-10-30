package catalog

import (
	"fmt"
	"strings"
)

// Template describes a whole stacking achievement: one criteria, one
// difficulty curve, and the range of tiers to generate from it. Expand
// turns it into the per-tier Entry values bootstrap pushes to GitLab.
//
// This is the shape the VS Code Achievements extension's
// StackingAchievementTemplate takes, minus the parts GitLab has no room
// for. GitLab achievements are flat objects with a name, a description and
// an avatar: there is no tier or level field, no category, no hidden flag,
// and no prerequisite links. So a ten-tier template is ten separate GitLab
// achievements, and the tier only survives in the name and in this app's
// own database. EXP survives the same way: GitLab has nowhere to put it, so
// ExpCurve's output is stored locally and totalled per user by the engine.
type Template struct {
	// Curve maps a tier index to the threshold it requires.
	Curve Curve
	// ExpCurve maps a tier index to the EXP earning it is worth. Leave it
	// nil for StandardInfernalCurve, which is the extension's default
	// expFunction and what every template in this catalog uses: a tier's
	// reward should track how hard tiers get in general, not how hard this
	// particular criteria's own curve gets, or an easy-curve criteria would
	// pay out an order of magnitude more than a hard-curve one for the same
	// effort.
	ExpCurve Curve
	// CriteriaKey is the counter the tiers are earned off.
	CriteriaKey string
	// Category groups related criteria for display. GitLab has no notion of
	// one, so like the tier it only lives in this app.
	Category string
	// Title names the achievement, with a %s where the tier's numeral goes
	// ("Committer %s" becomes "Committer I", "Committer II", ...).
	Title string
	// Description describes the tier, with a %d where its threshold goes.
	Description string
	// First describes a tier whose threshold is 1, which Description's
	// plural can't ("Author 1 commits."). Every template sets one, whether
	// or not its curve currently starts at 1, so retuning a curve can't
	// bring the plural back.
	First string
	// AvatarPath is the embedded asset shown for every tier of this
	// criteria, or empty for no avatar. Tiers deliberately share one image
	// rather than each having their own: a criteria at eleven tiers would
	// otherwise need eleven pieces of art before it could ship at all.
	AvatarPath string
	// MinTier and MaxTier bound which tier indexes to generate, inclusive.
	// The index feeds Curve; the name always numbers from I regardless, so
	// raising MinTier drops the easy tiers without renaming the rest.
	MinTier int64
	MaxTier int64
}

// Expand generates one Entry per tier in the template's range.
//
// Both curves are read at the tier's index, not at its displayed number, so
// a template that raises MinTier to drop its easy tiers keeps the
// thresholds and rewards the surviving tiers already had rather than
// sliding the whole progression down.
func (t Template) Expand() []Entry {
	entries := make([]Entry, 0, t.MaxTier-t.MinTier+1)
	expCurve := t.expCurve()

	for index := t.MinTier; index <= t.MaxTier; index++ {
		tier := index - t.MinTier + 1
		threshold := t.Curve(index)

		description := fmt.Sprintf(t.Description, threshold)
		if threshold == 1 && t.First != "" {
			description = t.First
		}

		entries = append(entries, Entry{
			CriteriaKey: t.CriteriaKey,
			Name:        fmt.Sprintf(t.Title, roman(tier)),
			Description: description,
			AvatarPath:  t.AvatarPath,
			Tier:        tier,
			Threshold:   threshold,
			Exp:         expCurve(index),
		})
	}

	return entries
}

// expCurve returns the template's EXP curve, falling back to the catalog
// default for the templates that don't set one (which is all of them).
func (t Template) expCurve() Curve {
	if t.ExpCurve == nil {
		return StandardInfernalCurve
	}

	return t.ExpCurve
}

// maxTier is how deep every stacking template goes, matching the VS Code
// extension's default. The top tiers are deliberately out of reach on most
// instances: they exist so that the one person who has been committing to
// the same GitLab since 2015 still has something left to earn.
const maxTier = 10

// templates is the achievement catalog: every criteria this app can
// observe on a GitLab instance, and the curve its tiers follow.
//
// The criteria are the subset of the VS Code extension's that a GitLab
// server can actually see, plus what the hooks this app registers offer
// that the extension never had a counterpart for. Anything that needs to
// watch an editor (lines of code, files created, per-language breakdowns,
// pastes, tabs, extensions, themes, debugger sessions, terminal tasks, time
// spent) has no server-side equivalent and is not represented here. Two git
// criteria are missing for the same reason even though they sound
// observable: an amend is indistinguishable from an ordinary commit once
// pushed, and GitLab's Events API doesn't report whether a push was forced.
//
// Three of the event types the hooks subscribe to are absent for a different
// reason: nobody to credit. Vulnerability payloads carry no user at all
// (release payloads don't either, but the releases API names the author,
// so a release hook is followed by a lookup), member payloads name the
// member rather than whoever added them, and feature flag payloads carry a user but no identifier for the
// change, so repeated toggles of one flag can't be told apart from a
// redelivery of the first. Milestone events have no payload type in
// client-go to parse at all. The hooks stay subscribed to them so that
// adding a criteria later is a code change rather than a re-registration
// across every project.
//
//nolint:gochecknoglobals // a package-level lookup table, read-only after init
var templates = []Template{
	//////////////////////// GIT ////////////////////////
	{
		CriteriaKey: CriteriaCommits,
		Category:    CategoryGit,
		Title:       "Committer %s",
		Description: "Author %d commits.",
		First:       "Author your first commit.",
		AvatarPath:  "assets/commit.png",
		Curve:       StandardHardCurve,
		MaxTier:     maxTier,
	},
	{
		CriteriaKey: CriteriaPushes,
		Category:    CategoryGit,
		Title:       "Product Shipping %s",
		Description: "Push %d times.",
		First:       "Push for the first time.",
		AvatarPath:  "assets/push.png",
		Curve:       StandardInfernalCurve,
		MaxTier:     maxTier,
	},
	{
		CriteriaKey: CriteriaBranchesCreated,
		Category:    CategoryGit,
		Title:       "Friend of the Trees %s",
		Description: "Create %d branches.",
		First:       "Create your first branch.",
		AvatarPath:  "assets/branch.png",
		Curve:       StandardInfernalCurve,
		MaxTier:     maxTier,
	},
	{
		CriteriaKey: CriteriaTagsCreated,
		Category:    CategoryGit,
		Title:       "Tagger %s",
		Description: "Create %d tags.",
		First:       "Create your first tag.",
		AvatarPath:  "assets/tag.png",
		Curve:       StandardInfernalCurve,
		MaxTier:     maxTier,
	},
	{
		CriteriaKey: CriteriaReleasesPublished,
		Category:    CategoryGit,
		Title:       "Release Manager %s",
		Description: "Publish %d releases.",
		First:       "Publish your first release.",
		AvatarPath:  "assets/release.png",
		Curve:       StandardInfernalCurve,
		MaxTier:     maxTier,
	},

	//////////////////////// MERGE REQUESTS & REVIEW ////////////////////////
	{
		CriteriaKey: CriteriaMergeRequestsOpened,
		Category:    CategoryMergeRequests,
		Title:       "Merge Request Opener %s",
		Description: "Open %d merge requests.",
		First:       "Open your first merge request.",
		AvatarPath:  "assets/mropen.png",
		Curve:       StandardHardCurve,
		MaxTier:     maxTier,
	},
	{
		CriteriaKey: CriteriaMergeRequestsMerged,
		Category:    CategoryMergeRequests,
		Title:       "Merger %s",
		Description: "Merge %d merge requests.",
		First:       "Merge your first merge request.",
		AvatarPath:  "assets/merge.png",
		Curve:       StandardInfernalCurve,
		MaxTier:     maxTier,
	},
	{
		// Merger credits whoever clicked merge, which on most teams is a
		// reviewer or a maintainer. This is the other half: the author whose
		// work went in.
		CriteriaKey: CriteriaMergeRequestsLanded,
		Category:    CategoryMergeRequests,
		Title:       "Patch Accepted %s",
		Description: "Have %d of your merge requests merged.",
		First:       "Have your first merge request merged.",
		AvatarPath:  "assets/landed.png",
		Curve:       StandardHardCurve,
		MaxTier:     maxTier,
	},
	{
		CriteriaKey: CriteriaMergeRequestsApproved,
		Category:    CategoryMergeRequests,
		Title:       "Stamp of Approval %s",
		Description: "Approve %d merge requests.",
		First:       "Approve your first merge request.",
		AvatarPath:  "assets/stamp.png",
		Curve:       StandardInfernalCurve,
		MaxTier:     maxTier,
	},
	{
		CriteriaKey: CriteriaMergeRequestsClosed,
		Category:    CategoryMergeRequests,
		Title:       "Second Thoughts %s",
		Description: "Close %d merge requests without merging them.",
		First:       "Close a merge request without merging it.",
		AvatarPath:  "assets/reject.png",
		Curve:       StandardInfernalCurve,
		MaxTier:     maxTier,
	},
	{
		CriteriaKey: CriteriaMergeRequestsMergedFast,
		Category:    CategoryMergeRequests,
		Title:       "Rubber Stamp %s",
		Description: "Merge %d merge requests within an hour of them being opened.",
		First:       "Merge a merge request within an hour of it being opened.",
		AvatarPath:  "assets/stopwatch.png",
		Curve:       StandardInfernalCurve,
		MaxTier:     maxTier,
	},
	{
		CriteriaKey: CriteriaComments,
		Category:    CategoryMergeRequests,
		Title:       "Commentator %s",
		Description: "Leave %d comments.",
		First:       "Leave your first comment.",
		AvatarPath:  "assets/comment.png",
		Curve:       StandardMediumCurve,
		MaxTier:     maxTier,
	},
	{
		// Commentator counts small talk on an issue the same as a remark on
		// line 40 of somebody's change. This counts only the latter.
		CriteriaKey: CriteriaReviewComments,
		Category:    CategoryMergeRequests,
		Title:       "Code Reviewer %s",
		Description: "Leave %d comments on merge request diffs.",
		First:       "Leave your first comment on a merge request diff.",
		AvatarPath:  "assets/review.png",
		Curve:       StandardMediumCurve,
		MaxTier:     maxTier,
	},
	{
		CriteriaKey: CriteriaDiscussionsResolved,
		Category:    CategoryMergeRequests,
		Title:       "Loose Ends %s",
		Description: "Resolve %d review discussions.",
		First:       "Resolve your first review discussion.",
		AvatarPath:  "assets/resolved.png",
		Curve:       StandardHardCurve,
		MaxTier:     maxTier,
	},

	//////////////////////// ISSUES ////////////////////////
	{
		CriteriaKey: CriteriaIssuesOpened,
		Category:    CategoryIssues,
		Title:       "Issue Opener %s",
		Description: "Open %d issues.",
		First:       "Open your first issue.",
		AvatarPath:  "assets/bug.png",
		Curve:       StandardHardCurve,
		MaxTier:     maxTier,
	},
	{
		CriteriaKey: CriteriaIssuesClosed,
		Category:    CategoryIssues,
		Title:       "Problem Solver %s",
		Description: "Close %d issues.",
		First:       "Close your first issue.",
		AvatarPath:  "assets/fix.png",
		Curve:       StandardHardCurve,
		MaxTier:     maxTier,
	},

	//////////////////////// CI/CD ////////////////////////
	{
		CriteriaKey: CriteriaPipelinesRun,
		Category:    CategoryCICD,
		Title:       "Pipeline Operator %s",
		Description: "Trigger %d pipelines.",
		First:       "Trigger your first pipeline.",
		AvatarPath:  "assets/pipes.png",
		Curve:       StandardMediumCurve,
		MaxTier:     maxTier,
	},
	{
		CriteriaKey: CriteriaPipelinesSucceeded,
		Category:    CategoryCICD,
		Title:       "All Green %s",
		Description: "Trigger %d pipelines that pass.",
		First:       "Trigger a pipeline that passes.",
		AvatarPath:  "assets/allgreen.png",
		Curve:       StandardMediumCurve,
		MaxTier:     maxTier,
	},
	{
		CriteriaKey: CriteriaPipelinesFailed,
		Category:    CategoryCICD,
		Title:       "Firefighter %s",
		Description: "Trigger %d pipelines that fail.",
		First:       "Trigger a pipeline that fails.",
		AvatarPath:  "assets/firefighter.png",
		Curve:       StandardInfernalCurve,
		MaxTier:     maxTier,
	},
	{
		CriteriaKey: CriteriaJobsRun,
		Category:    CategoryCICD,
		Title:       "Assembly Line %s",
		Description: "Run %d CI jobs.",
		First:       "Run your first CI job.",
		AvatarPath:  "assets/gears.png",
		// Jobs are the most numerous thing on this list by an order of
		// magnitude, since one pipeline is many jobs. The medium curve is
		// what keeps its early tiers from being handed out several at a
		// time by a single pipeline.
		Curve:   StandardMediumCurve,
		MaxTier: maxTier,
	},
	{
		CriteriaKey: CriteriaDeployments,
		Category:    CategoryCICD,
		Title:       "Ship It %s",
		Description: "Deploy %d times.",
		First:       "Deploy for the first time.",
		AvatarPath:  "assets/rocket.png",
		Curve:       StandardHardCurve,
		MaxTier:     maxTier,
	},
	{
		CriteriaKey: CriteriaDeploymentsSucceeded,
		Category:    CategoryCICD,
		Title:       "Stuck the Landing %s",
		Description: "Complete %d deployments successfully.",
		First:       "Complete a deployment successfully.",
		AvatarPath:  "assets/bullseye.png",
		Curve:       StandardHardCurve,
		MaxTier:     maxTier,
	},

	//////////////////////// COLLABORATION ////////////////////////
	{
		CriteriaKey: CriteriaEmojiAwarded,
		Category:    CategoryCollaboration,
		Title:       "Reaction Time %s",
		Description: "React to %d issues, merge requests, or comments.",
		First:       "React to an issue, a merge request, or a comment.",
		AvatarPath:  "assets/thumbsup.png",
		Curve:       StandardMediumCurve,
		MaxTier:     maxTier,
	},
	{
		CriteriaKey: CriteriaWikiPagesCreated,
		Category:    CategoryCollaboration,
		Title:       "Librarian %s",
		Description: "Create %d wiki pages.",
		First:       "Create your first wiki page.",
		AvatarPath:  "assets/librarian.png",
		Curve:       StandardInfernalCurve,
		MaxTier:     maxTier,
	},
	{
		// Breadth rather than volume: one project with ten thousand commits
		// is one project. It is what rewards the people who wander into
		// other teams' repositories to fix things.
		CriteriaKey: CriteriaProjectsContributed,
		Category:    CategoryCollaboration,
		Title:       "Globetrotter %s",
		Description: "Contribute to %d projects.",
		First:       "Contribute to a project.",
		AvatarPath:  "assets/globe.png",
		Curve:       StandardInfernalCurve,
		MaxTier:     maxTier,
	},

	//////////////////////// ENGAGEMENT ////////////////////////
	{
		CriteriaKey: CriteriaActiveDays,
		Category:    CategoryEngagement,
		Title:       "Regular %s",
		Description: "Be active on %d separate days.",
		First:       "Be active for a day.",
		AvatarPath:  "assets/mug.png",
		Curve:       StandardHardCurve,
		MaxTier:     maxTier,
	},
	{
		CriteriaKey: CriteriaActivityStreak,
		Category:    CategoryEngagement,
		Title:       "Connection Streak %s",
		Description: "Stay active %d days in a row.",
		First:       "Start a streak of active days.",
		AvatarPath:  "assets/streak.png",
		// Streaks are the one criteria whose milestones people already
		// have names for, so the tiers are the week/fortnight/month/year
		// marks rather than points on a curve.
		//nolint:mnd // the day counts are the catalog's data, not constants
		Curve:   Steps(1, 2, 3, 5, 7, 14, 30, 60, 100, 180, 365),
		MaxTier: maxTier,
	},
	{
		CriteriaKey: CriteriaNightOwlDays,
		Category:    CategoryEngagement,
		Title:       "Night Owl %s",
		Description: "Be active after midnight on %d separate days.",
		First:       "Be active after midnight.",
		AvatarPath:  "assets/owl.png",
		Curve:       StandardInfernalCurve,
		MaxTier:     maxTier,
	},
	{
		CriteriaKey: CriteriaEarlyBirdDays,
		Category:    CategoryEngagement,
		Title:       "Early Bird %s",
		Description: "Be active before dawn on %d separate days.",
		First:       "Be active before dawn.",
		AvatarPath:  "assets/bird.png",
		Curve:       StandardInfernalCurve,
		MaxTier:     maxTier,
	},
}

// romanNumerals maps a tier number onto its numeral, largest first, so
// roman can subtract its way down.
//
//nolint:gochecknoglobals // a package-level lookup table, read-only after init
var romanNumerals = []struct {
	symbol string
	value  int64
}{
	{"M", 1000}, {"CM", 900}, {"D", 500}, {"CD", 400},
	{"C", 100}, {"XC", 90}, {"L", 50}, {"XL", 40},
	{"X", 10}, {"IX", 9}, {"V", 5}, {"IV", 4}, {"I", 1},
}

// roman renders a tier number as a Roman numeral, the form this project
// names tiers in (Committer I, II, III). The VS Code extension numbers its
// tiers in Arabic digits instead; the difference is cosmetic, and Roman
// numerals read better in a GitLab achievement name, which has no separate
// tier field to put a number in.
func roman(tier int64) string {
	if tier < 1 {
		return ""
	}

	var out strings.Builder

	remaining := tier

	for _, numeral := range romanNumerals {
		for remaining >= numeral.value {
			out.WriteString(numeral.symbol)

			remaining -= numeral.value
		}
	}

	return out.String()
}
