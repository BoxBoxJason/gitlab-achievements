package api

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"time"

	"go.uber.org/zap"

	"github.com/boxboxjason/gitlab-achievements/internal/catalog"
	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

// iconMaxAge is how long a browser may cache an achievement icon. The icons
// are compiled into the binary, so they change only with a release, and the
// ETag lets a browser revalidate cheaply once the day is up.
const iconMaxAge = "public, max-age=86400"

// icon is one criteria's image, read out of the binary once.
type icon struct {
	etag string
	data []byte
}

// loadIcons reads every criteria's image out of the binary.
//
// They are read once, when the API is built, rather than per request: the
// set is a few dozen small PNGs fixed at compile time, and the icon route
// is served without authentication (see routes), so a request for one
// should cost no more than copying bytes already in memory. An image the
// catalog names but the binary does not hold is left out and logged, and
// then answers 404 like a criteria with no image.
func loadIcons() map[string]icon {
	icons := map[string]icon{}

	for _, criteria := range catalog.AllCriteria() {
		if criteria.AvatarPath == "" {
			continue
		}

		data, err := readAvatar(criteria.AvatarPath)
		if err != nil {
			zap.L().Warn("the catalog names an achievement icon the binary does not hold",
				zap.String("criteria_key", criteria.Key),
				zap.Error(err),
			)

			continue
		}

		sum := sha256.Sum256(data)
		icons[criteria.Key] = icon{
			data: data,
			etag: `"` + hex.EncodeToString(sum[:16]) + `"`,
		}
	}

	return icons
}

// readAvatar reads one embedded image whole.
func readAvatar(path string) ([]byte, error) {
	file, err := catalog.OpenAvatar(path)
	if err != nil {
		return nil, fmt.Errorf("failed to load an achievement icon: %w", err)
	}

	defer file.Close() //nolint:errcheck // an embedded file has nothing to release

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read avatar asset %q: %w", path, err)
	}

	return data, nil
}

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
// what GitLab has no field for, the category and the stack's title, and
// whether there is an icon: the icon route serves the binary's copy, so
// the binary is the one that knows whether it has one to serve.
func (a *API) handleAchievements(resp http.ResponseWriter, req *http.Request) {
	var definitions []db.AchievementDefinition

	err := a.store.conn.WithContext(req.Context()).Order("criteria_key ASC, tier ASC").Find(&definitions).Error
	if err != nil {
		a.fail(resp, req, fmt.Errorf("failed to load achievement definitions: %w", err))

		return
	}

	a.writeJSON(resp, buildCatalog(definitions, a.icons))
}

// buildCatalog groups definitions by criteria, in catalog order, giving an
// icon URL to the criteria icons holds an image for.
func buildCatalog(definitions []db.AchievementDefinition, icons map[string]icon) Catalog {
	byCriteria := map[string][]AchievementTier{}

	for _, definition := range definitions {
		byCriteria[definition.CriteriaKey] = append(byCriteria[definition.CriteriaKey], AchievementTier{
			Name:        definition.Name,
			Description: definition.Description,
			Tier:        definition.Tier,
			Threshold:   definition.Threshold,
			ExpReward:   definition.ExpReward,
		})
	}

	known := catalog.AllCriteria()
	criteria := make([]AchievementCriteria, 0, len(byCriteria))

	for _, entry := range known {
		tiers, ok := byCriteria[entry.Key]
		if !ok {
			continue
		}

		delete(byCriteria, entry.Key)

		_, hasIcon := icons[entry.Key]
		criteria = append(criteria, criteriaOf(entry.Key, entry.Category, entry.Title, tiers, hasIcon))
	}

	// A criteria the database holds but the catalog in code no longer
	// does is still reported, uncategorized, until bootstrap cleans it up:
	// users may hold tiers of it, and those show on their records. It has
	// no icon, since the binary no longer knows it.
	leftover := make([]AchievementCriteria, 0, len(byCriteria))
	for key, tiers := range byCriteria {
		leftover = append(leftover, criteriaOf(key, "", key, tiers, false))
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

// handleAchievementIcon serves the art every tier of a criteria is drawn
// from.
//
// It is the art bootstrap uploads to GitLab as each tier's avatar, without
// the tier marker drawn over it there: the frontend shows the tier beside
// the icon instead. It is served from the copy compiled into this binary,
// so a frontend need not reach GitLab for it. http.ServeContent answers a
// revalidation carrying the ETag with a bodiless 304.
func (a *API) handleAchievementIcon(resp http.ResponseWriter, req *http.Request) {
	image, ok := a.icons[req.PathValue("criteria")]
	if !ok {
		writeError(resp, http.StatusNotFound, "no icon for this criteria")

		return
	}

	resp.Header().Set("Content-Type", "image/png")
	resp.Header().Set("Cache-Control", iconMaxAge)
	resp.Header().Set("ETag", image.etag)
	resp.Header().Set("X-Content-Type-Options", "nosniff")

	http.ServeContent(resp, req, "", time.Time{}, bytes.NewReader(image.data))
}
