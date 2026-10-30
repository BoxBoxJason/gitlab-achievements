package engine

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/activity"
	"github.com/boxboxjason/gitlab-achievements/internal/catalog"
	"github.com/boxboxjason/gitlab-achievements/internal/db"
	"github.com/boxboxjason/gitlab-achievements/internal/exclusion"
)

// evaluateProjects records the project an activity happened in and, only
// when the user had never been active there before, recounts how many
// projects they have contributed to. It returns how many tiers were newly
// awarded.
//
// Activity tied to no project (a wiki page, whose payload names its project
// by path only) is left out rather than guessed at.
func evaluateProjects(txn *gorm.DB, userID int64, event activity.Event) (int, error) {
	projectID := event.ProjectID
	if projectID <= 0 {
		return 0, nil
	}

	known, err := knownProject(txn, userID, projectID)
	if err != nil || known {
		return 0, err
	}

	err = txn.Create(&db.ActivityProject{UserID: userID, ProjectID: projectID}).Error
	if err != nil {
		return 0, fmt.Errorf("failed to record project %d for user %d: %w", projectID, userID, err)
	}

	var count int64

	err = txn.Model(&db.ActivityProject{}).Where("user_id = ?", userID).Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("failed to count projects for user %d: %w", userID, err)
	}

	err = setCounter(txn, userID, catalog.CriteriaProjectsContributed, count)
	if err != nil {
		return 0, err
	}

	return awardReachedTiers(txn, userID, catalog.CriteriaProjectsContributed, count, event.OccurredAt)
}

// knownProject reports whether the user already has projectID in their set.
func knownProject(txn *gorm.DB, userID, projectID int64) (bool, error) {
	var seen int64

	err := txn.Model(&db.ActivityProject{}).
		Where("user_id = ? AND project_id = ?", userID, projectID).
		Count(&seen).Error
	if err != nil {
		return false, fmt.Errorf("failed to check project %d for user %d: %w", projectID, userID, err)
	}

	return seen > 0, nil
}

// evaluateRepeat gives activity the engine has already counted the one
// thing it can still contribute: the project it happened in.
//
// Every other criteria would count it again, but a project set doesn't
// care how often it hears of a project, so a repeat is as good as a first
// sighting. That is what lets a forced backfill fill the set in on an
// instance whose history was walked before this criteria existed: every
// event it re-reads is a repeat by then. It returns how many tiers were
// newly awarded.
//
// The common case, a repeat in a project the user is already known in,
// costs two reads and no write. Only a project new to the set takes the
// participation claim a first sighting would.
func evaluateRepeat(txn *gorm.DB, event activity.Event) (int, error) {
	if event.ProjectID <= 0 {
		return 0, nil
	}

	user, found, err := knownParticipant(txn, event.ActorID)
	if err != nil || !found {
		return 0, err
	}

	known, err := knownProject(txn, user.ID, event.ProjectID)
	if err != nil || known {
		return 0, err
	}

	err = claimParticipant(txn, &user)
	if err != nil {
		return 0, err
	}

	awarded, err := evaluateProjects(txn, user.ID, event)
	if err != nil || awarded == 0 {
		return 0, err
	}

	_, err = recomputeExp(txn, user.ID)
	if err != nil {
		return 0, err
	}

	return awarded, nil
}

// knownParticipant returns the local row of a user who takes part,
// reporting false for one this app has never seen or who is excluded.
func knownParticipant(txn *gorm.DB, gitlabUserID int64) (db.User, bool, error) {
	var user db.User

	err := txn.Where(&db.User{GitLabUserID: gitlabUserID}).First(&user).Error

	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return user, false, nil
	case err != nil:
		return user, false, fmt.Errorf("failed to resolve user %d: %w", gitlabUserID, err)
	}

	return user, user.ExcludedReason == string(exclusion.Included), nil
}
