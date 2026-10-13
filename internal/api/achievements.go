package api

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"

	"go.uber.org/zap"

	"github.com/boxboxjason/gitlab-achievements/internal/catalog"
	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

// iconMaxAge is how long a browser may cache an achievement icon. The icons
// are compiled into the binary, so they change only with a release.
const iconMaxAge = "public, max-age=86400"

// AchievementTier is one tier of a criteria in the catalog.
type AchievementTier struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Tier        int64  `json:"tier"`
	Threshold   int64  `json:"threshold"`
	ExpReward   int64  `json:"exp_reward"`
}

// AchievementCriteria is one criteria in the catalog, with every tier it
// stacks.
type AchievementCriteria struct {
	CriteriaKey string            `json:"criteria_key"`
	Category    string            `json:"category"`
	Title       string            `json:"title"`
	IconURL     string            `json:"icon_url,omitempty"`
	Tiers       []AchievementTier `json:"tiers"`
}

// Catalog is every achievement this app awards, grouped by criteria, so a
// frontend can show what there is still to earn and how far away it is.
type Catalog struct {
	Criteria []AchievementCriteria `json:"criteria"`
}

// handleAchievements serves the catalog.
//
// The tiers come from the definitions bootstrap created, which are what
// GitLab actually holds, rather than from the catalog in code: the two only
// differ between a catalog change and the next start, and in that window
// the database is the one telling the truth. The catalog in code supplies
// what GitLab has no field for, the category and the stack's title.
func (a *API) handleAchievements(resp http.ResponseWriter, req *http.Request) {
	var definitions []db.AchievementDefinition

	err := a.store.conn.WithContext(req.Context()).Order("criteria_key ASC, tier ASC").Find(&definitions).Error
	if err != nil {
		a.fail(resp, req, fmt.Errorf("failed to load achievement definitions: %w", err))

		return
	}

	a.writeJSON(resp, buildCatalog(definitions))
}

// buildCatalog groups definitions by criteria, in catalog order.
func buildCatalog(definitions []db.AchievementDefinition) Catalog {
	byCriteria := map[string][]AchievementTier{}
	icons := map[string]bool{}

	for _, definition := range definitions {
		byCriteria[definition.CriteriaKey] = append(byCriteria[definition.CriteriaKey], AchievementTier{
			Name:        definition.Name,
			Description: definition.Description,
			Tier:        definition.Tier,
			Threshold:   definition.Threshold,
			ExpReward:   definition.ExpReward,
		})

		if definition.AvatarPath != "" {
			icons[definition.CriteriaKey] = true
		}
	}

	known := catalog.AllCriteria()
	criteria := make([]AchievementCriteria, 0, len(byCriteria))

	for _, entry := range known {
		tiers, ok := byCriteria[entry.Key]
		if !ok {
			continue
		}

		delete(byCriteria, entry.Key)

		criteria = append(criteria, criteriaOf(entry.Key, entry.Category, entry.Title, tiers, icons[entry.Key]))
	}

	// A criteria the database holds but the catalog in code no longer
	// does is still reported, uncategorized, until bootstrap cleans it up:
	// users may hold tiers of it, and those show on their records.
	leftover := make([]AchievementCriteria, 0, len(byCriteria))
	for key, tiers := range byCriteria {
		leftover = append(leftover, criteriaOf(key, "", key, tiers, icons[key]))
	}

	slices.SortFunc(leftover, func(a, b AchievementCriteria) int {
		return cmp.Compare(a.CriteriaKey, b.CriteriaKey)
	})

	return Catalog{Criteria: append(criteria, leftover...)}
}

// criteriaOf assembles one catalog row.
func criteriaOf(key, category, title string, tiers []AchievementTier, hasIcon bool) AchievementCriteria {
	row := AchievementCriteria{
		CriteriaKey: key,
		Category:    category,
		Title:       title,
		Tiers:       tiers,
	}

	if hasIcon {
		row.IconURL = PathPrefix + "achievements/" + url.PathEscape(key) + "/icon"
	}

	return row
}

// handleAchievementIcon serves the image every tier of a criteria shares.
//
// The image is the same one bootstrap uploaded to GitLab as the
// achievement's avatar, served from the copy compiled into this binary, so
// a frontend need not reach GitLab for it.
func (a *API) handleAchievementIcon(resp http.ResponseWriter, req *http.Request) {
	var definition db.AchievementDefinition

	err := a.store.conn.WithContext(req.Context()).
		Where(&db.AchievementDefinition{CriteriaKey: req.PathValue("criteria")}).
		Where("avatar_path <> ?", "").
		First(&definition).Error
	if err != nil {
		writeError(resp, http.StatusNotFound, "no icon for this criteria")

		return
	}

	icon, err := catalog.OpenAvatar(definition.AvatarPath)
	if err != nil {
		zap.L().Warn("an achievement definition names an icon the binary does not hold",
			zap.String("criteria_key", definition.CriteriaKey),
			zap.Error(err),
		)
		writeError(resp, http.StatusNotFound, "no icon for this criteria")

		return
	}

	defer icon.Close() //nolint:errcheck // an embedded file has nothing to release

	resp.Header().Set("Content-Type", "image/png")
	resp.Header().Set("Cache-Control", iconMaxAge)
	resp.Header().Set("X-Content-Type-Options", "nosniff")

	_, err = io.Copy(resp, icon)
	if err != nil && !errors.Is(err, http.ErrHandlerTimeout) {
		zap.L().Debug("failed to write an achievement icon", zap.Error(err))
	}
}
