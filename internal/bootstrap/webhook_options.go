package bootstrap

import (
	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// The desired configuration of a hook this app owns, as four literals:
// group and project hooks are separate GitLab APIs, and each takes a
// distinct type for registering and for updating.
//
// Every event GitLab offers on a hook is enabled, not only the ones the
// achievement catalog reads today. Turning an event on later means editing
// every hook on the instance and, until that sweep runs, silently missing
// the activity in between; leaving them all on costs nothing but deliveries
// the receiver already ignores by design. The one thing this must not do is
// leave a flag unset: an omitted field is not "off" but "unspecified", and
// what GitLab defaults it to is not something to rely on.
//
// The field sets differ only where the APIs do. Group hooks alone carry
// project, subgroup, and member events, because those describe things that
// happen to a group's contents; project hooks alone carry deploy token
// events. Neither has an equivalent on the other side.
//
// TestHookOptions_* pins all of this: that add and edit agree, and that no
// flag either API offers is left unset. That is the guard against the
// obvious failure here, which is updating one of the four and not the rest.

// addGroupHookOptions is the configuration a newly registered group hook
// gets.
func addGroupHookOptions(webhookURL, secret string) *gitlab.AddGroupHookOptions {
	return &gitlab.AddGroupHookOptions{
		Name:                      new(gitlabAchievementsWebhookName),
		URL:                       &webhookURL,
		Token:                     &secret,
		EnableSSLVerification:     new(true),
		PushEvents:                new(true),
		TagPushEvents:             new(true),
		MergeRequestsEvents:       new(true),
		IssuesEvents:              new(true),
		ConfidentialIssuesEvents:  new(true),
		NoteEvents:                new(true),
		ConfidentialNoteEvents:    new(true),
		PipelineEvents:            new(true),
		JobEvents:                 new(true),
		DeploymentEvents:          new(true),
		ReleasesEvents:            new(true),
		MilestoneEvents:           new(true),
		WikiPageEvents:            new(true),
		FeatureFlagEvents:         new(true),
		EmojiEvents:               new(true),
		VulnerabilityEvents:       new(true),
		ResourceAccessTokenEvents: new(true),
		ProjectEvents:             new(true),
		SubGroupEvents:            new(true),
		MemberEvents:              new(true),
	}
}

// editGroupHookOptions re-applies that same configuration to a group hook
// that already exists, healing any drift.
func editGroupHookOptions(webhookURL, secret string) *gitlab.EditGroupHookOptions {
	return &gitlab.EditGroupHookOptions{
		Name:                      new(gitlabAchievementsWebhookName),
		URL:                       &webhookURL,
		Token:                     &secret,
		EnableSSLVerification:     new(true),
		PushEvents:                new(true),
		TagPushEvents:             new(true),
		MergeRequestsEvents:       new(true),
		IssuesEvents:              new(true),
		ConfidentialIssuesEvents:  new(true),
		NoteEvents:                new(true),
		ConfidentialNoteEvents:    new(true),
		PipelineEvents:            new(true),
		JobEvents:                 new(true),
		DeploymentEvents:          new(true),
		ReleasesEvents:            new(true),
		MilestoneEvents:           new(true),
		WikiPageEvents:            new(true),
		FeatureFlagEvents:         new(true),
		EmojiEvents:               new(true),
		VulnerabilityEvents:       new(true),
		ResourceAccessTokenEvents: new(true),
		ProjectEvents:             new(true),
		SubGroupEvents:            new(true),
		MemberEvents:              new(true),
	}
}

// addProjectHookOptions is the configuration a newly registered project
// hook gets.
func addProjectHookOptions(webhookURL, secret string) *gitlab.AddProjectHookOptions {
	return &gitlab.AddProjectHookOptions{
		Name:                      new(gitlabAchievementsWebhookName),
		URL:                       &webhookURL,
		Token:                     &secret,
		EnableSSLVerification:     new(true),
		PushEvents:                new(true),
		TagPushEvents:             new(true),
		MergeRequestsEvents:       new(true),
		IssuesEvents:              new(true),
		ConfidentialIssuesEvents:  new(true),
		NoteEvents:                new(true),
		ConfidentialNoteEvents:    new(true),
		PipelineEvents:            new(true),
		JobEvents:                 new(true),
		DeploymentEvents:          new(true),
		ReleasesEvents:            new(true),
		MilestoneEvents:           new(true),
		WikiPageEvents:            new(true),
		FeatureFlagEvents:         new(true),
		EmojiEvents:               new(true),
		VulnerabilityEvents:       new(true),
		ResourceAccessTokenEvents: new(true),
		ResourceDeployTokenEvents: new(true),
	}
}

// editProjectHookOptions re-applies that same configuration to a project
// hook that already exists, healing any drift.
func editProjectHookOptions(webhookURL, secret string) *gitlab.EditProjectHookOptions {
	return &gitlab.EditProjectHookOptions{
		Name:                      new(gitlabAchievementsWebhookName),
		URL:                       &webhookURL,
		Token:                     &secret,
		EnableSSLVerification:     new(true),
		PushEvents:                new(true),
		TagPushEvents:             new(true),
		MergeRequestsEvents:       new(true),
		IssuesEvents:              new(true),
		ConfidentialIssuesEvents:  new(true),
		NoteEvents:                new(true),
		ConfidentialNoteEvents:    new(true),
		PipelineEvents:            new(true),
		JobEvents:                 new(true),
		DeploymentEvents:          new(true),
		ReleasesEvents:            new(true),
		MilestoneEvents:           new(true),
		WikiPageEvents:            new(true),
		FeatureFlagEvents:         new(true),
		EmojiEvents:               new(true),
		VulnerabilityEvents:       new(true),
		ResourceAccessTokenEvents: new(true),
		ResourceDeployTokenEvents: new(true),
	}
}
