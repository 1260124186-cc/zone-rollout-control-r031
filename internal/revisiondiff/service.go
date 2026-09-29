package revisiondiff

import (
	"context"
	"strconv"

	"solo-0014-zone-rollout-control/internal/domain"
	"solo-0014-zone-rollout-control/internal/store"
)

type Service struct {
	store *store.Store
}

func New(stateStore *store.Store) *Service {
	return &Service{store: stateStore}
}

func (s *Service) Compare(
	ctx context.Context,
	planID string,
	fromRevisionID string,
	toRevisionID string,
	fromSerial uint32,
	toSerial uint32,
	categories []domain.DiffCategory,
) (domain.RevisionDiff, error) {
	if err := domain.ValidateDiffCategories(categories); err != nil {
		return domain.RevisionDiff{}, err
	}

	var result domain.RevisionDiff
	err := s.store.Read(ctx, func(state *domain.State) error {
		if _, ok := state.Plans[planID]; !ok {
			return domain.NotFound("plan", planID)
		}
		from, err := findRevisionForDiff(state, planID, fromRevisionID, fromSerial)
		if err != nil {
			return err
		}
		to, err := findRevisionForDiff(state, planID, toRevisionID, toSerial)
		if err != nil {
			return err
		}

		diff, err := domain.BuildRevisionDiff(planID, from, to)
		if err != nil {
			return err
		}
		result = domain.FilterRevisionDiff(diff, categories)
		return nil
	})
	return result, err
}

func findRevisionForDiff(
	state *domain.State,
	planID string,
	revisionID string,
	serial uint32,
) (domain.ZoneRevision, error) {
	if revisionID != "" {
		revision, ok := state.Revisions[revisionID]
		if !ok {
			return domain.ZoneRevision{}, domain.NotFound("revision", revisionID)
		}
		if revision.PlanID != planID {
			return domain.ZoneRevision{}, domain.NewError(domain.CodeNotFound, "revision was not found in this plan", map[string]any{
				"revision_id": revisionID,
				"plan_id":     planID,
				"owner_plan":  revision.PlanID,
			})
		}
		return revision, nil
	}
	for _, revision := range state.Revisions {
		if revision.PlanID == planID && revision.Serial == serial {
			return revision, nil
		}
	}
	return domain.ZoneRevision{}, domain.NotFound("revision", serialReference(serial))
}

func serialReference(serial uint32) string {
	return "serial:" + strconv.FormatUint(uint64(serial), 10)
}
