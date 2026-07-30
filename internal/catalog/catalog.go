// Package catalog defines the achievement definitions this app maintains in
// GitLab.
package catalog

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed assets/*.png
var avatarAssets embed.FS

// Entry describes one tier of one achievement criteria: what GitLab shows
// (Name, Description, avatar) and the local progress threshold (Tier,
// Threshold) that earns it, keyed by CriteriaKey, the identifier the
// achievement rule engine reports progress against.
type Entry struct {
	CriteriaKey string
	Name        string
	Description string
	AvatarPath  string
	Tier        int64
	Threshold   int64
}

// HasAvatar reports whether entry has an avatar image configured.
func (e Entry) HasAvatar() bool {
	return e.AvatarPath != ""
}

// Avatar opens entry's avatar image. The caller must close the returned
// file, and must only call this when HasAvatar reports true.
func (e Entry) Avatar() (fs.File, error) {
	f, err := avatarAssets.Open(e.AvatarPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open avatar asset %q for %s tier %d: %w", e.AvatarPath, e.CriteriaKey, e.Tier, err)
	}

	return f, nil
}

const (
	tier1 = 1
	tier2 = 2

	committerIThreshold  = 10
	committerIIThreshold = 100
	mrOpenerIThreshold   = 10
)

// V1 returns a placeholder catalog: a couple of tiered achievements, enough
// to exercise bootstrap's create/update reconciliation end to end. It is
// not the final v1 catalog, see the achievement catalog issue, which will
// replace this with the full set (git activity, merge requests, code
// review, issues, CI/CD, engagement streaks).
func V1() []Entry {
	return []Entry{
		{CriteriaKey: "commits", Tier: tier1, Threshold: committerIThreshold, Name: "Committer I", Description: "Made 10 commits.", AvatarPath: "assets/committer_i.png"},
		{CriteriaKey: "commits", Tier: tier2, Threshold: committerIIThreshold, Name: "Committer II", Description: "Made 100 commits.", AvatarPath: "assets/committer_ii.png"},
		{CriteriaKey: "merge_requests_opened", Tier: tier1, Threshold: mrOpenerIThreshold, Name: "Merge Request Opener I", Description: "Opened 10 merge requests.", AvatarPath: "assets/mr_opener_i.png"},
	}
}
