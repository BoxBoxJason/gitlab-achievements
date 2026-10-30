package engine

import (
	"github.com/boxboxjason/gitlab-achievements/internal/activity"
	"github.com/boxboxjason/gitlab-achievements/internal/catalog"
)

// kindCriteria maps each normalized activity kind onto the cumulative
// catalog criteria it advances. A kind may advance several (a successful
// pipeline advances both "pipelines run" and "pipelines succeeded"), and a
// kind with no entry here is ignored entirely.
//
// The day-derived criteria (see dayCriteria) are deliberately absent: they
// aren't advanced by a kind but by the fact that any activity happened on a
// given day, so they are recomputed rather than looked up here.
//
//nolint:gochecknoglobals // a package-level lookup table, read-only after init
var kindCriteria = map[activity.Kind][]string{
	activity.KindCommit:                 {catalog.CriteriaCommits},
	activity.KindPush:                   {catalog.CriteriaPushes},
	activity.KindBranchCreated:          {catalog.CriteriaBranchesCreated},
	activity.KindTagCreated:             {catalog.CriteriaTagsCreated},
	activity.KindReleasePublished:       {catalog.CriteriaReleasesPublished},
	activity.KindMergeRequestOpened:     {catalog.CriteriaMergeRequestsOpened},
	activity.KindMergeRequestMerged:     {catalog.CriteriaMergeRequestsMerged},
	activity.KindMergeRequestMergedFast: {catalog.CriteriaMergeRequestsMergedFast},
	activity.KindMergeRequestLanded:     {catalog.CriteriaMergeRequestsLanded},
	activity.KindMergeRequestApproved:   {catalog.CriteriaMergeRequestsApproved},
	activity.KindMergeRequestClosed:     {catalog.CriteriaMergeRequestsClosed},
	activity.KindIssueOpened:            {catalog.CriteriaIssuesOpened},
	activity.KindIssueClosed:            {catalog.CriteriaIssuesClosed},
	activity.KindComment:                {catalog.CriteriaComments},
	activity.KindReviewComment:          {catalog.CriteriaReviewComments},
	activity.KindDiscussionResolved:     {catalog.CriteriaDiscussionsResolved},
	activity.KindPipelineRun:            {catalog.CriteriaPipelinesRun},
	activity.KindPipelineSucceeded:      {catalog.CriteriaPipelinesSucceeded},
	activity.KindPipelineFailed:         {catalog.CriteriaPipelinesFailed},
	activity.KindPipelineFixed:          {catalog.CriteriaPipelinesFixed},
	activity.KindJobRun:                 {catalog.CriteriaJobsRun},
	activity.KindDeployment:             {catalog.CriteriaDeployments},
	activity.KindDeploymentSucceeded:    {catalog.CriteriaDeploymentsSucceeded},
	activity.KindEmojiAwarded:           {catalog.CriteriaEmojiAwarded},
	activity.KindWikiPageCreated:        {catalog.CriteriaWikiPagesCreated},
}

// criteriaFor returns the criteria keys kind advances, or nil if this
// engine tracks nothing for it.
func criteriaFor(kind activity.Kind) []string {
	return kindCriteria[kind]
}

// absentKinds are the kinds whose actor was not there when they happened,
// and which so leave the day-derived criteria alone. A merge request landing
// is credited to its author, but somebody else merged it, possibly at 3am
// on a day the author never logged in: counting that would hand out active
// days, streaks and night-owl days for other people's work.
//
//nolint:gochecknoglobals // a package-level lookup table, read-only after init
var absentKinds = map[activity.Kind]bool{
	activity.KindMergeRequestLanded: true,
}

// presentFor reports whether kind means its actor was active when it
// happened.
func presentFor(kind activity.Kind) bool {
	return !absentKinds[kind]
}
