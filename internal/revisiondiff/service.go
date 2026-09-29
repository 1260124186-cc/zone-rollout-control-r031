// Package revisiondiff provides read-only previews of the differences
// between two immutable zone revisions that belong to the same plan.
//
// A preview never mutates state: it does not create plans, leases, probes,
// assessments, or any other entity. Revision contents are immutable, so a
// preview explanation computed for a pair of revisions stays reproducible as
// later revisions and snapshots evolve.
package revisiondiff

import (
	"context"
	"strings"

	"solo-0014-zone-rollout-control/internal/domain"
	"solo-0014-zone-rollout-control/internal/store"
)

type PreviewRequest struct {
	PlanID           string
	BaseRevisionID   string
	TargetRevisionID string
	BaseSerial       uint32
	TargetSerial     uint32
	Category         domain.DiffCategory
}

type Service struct {
	store *store.Store
}

func New(stateStore *store.Store) *Service {
	return &Service{store: stateStore}
}

// Preview resolves both revisions inside a single read transaction and
// builds a deterministic diff. Missing, stale, or cross-plan references
// fail explicitly without touching state.
func (s *Service) Preview(ctx context.Context, request PreviewRequest) (domain.RevisionDiff, error) {
	planID := strings.TrimSpace(request.PlanID)
	baseID := strings.TrimSpace(request.BaseRevisionID)
	targetID := strings.TrimSpace(request.TargetRevisionID)
	if planID == "" {
		return domain.RevisionDiff{}, domain.Validation("plan id is required", nil)
	}
	if baseID == "" {
		return domain.RevisionDiff{}, domain.Validation("base_revision_id is required", nil)
	}
	if targetID == "" {
		return domain.RevisionDiff{}, domain.Validation("target_revision_id is required", nil)
	}
	if request.Category != "" &&
		request.Category != domain.DiffAdded &&
		request.Category != domain.DiffRemoved &&
		request.Category != domain.DiffChanged {
		return domain.RevisionDiff{}, domain.Validation("diff category is not supported", map[string]any{
			"category": string(request.Category),
		})
	}

	var result domain.RevisionDiff
	err := s.store.Read(ctx, func(state *domain.State) error {
		if _, ok := state.Plans[planID]; !ok {
			return domain.NotFound("plan", planID)
		}
		base, ok := state.Revisions[baseID]
		if !ok {
			return domain.NotFound("revision", baseID)
		}
		target, ok := state.Revisions[targetID]
		if !ok {
			return domain.NotFound("revision", targetID)
		}
		if base.PlanID != planID || target.PlanID != planID {
			return domain.NewError(domain.CodeCrossPlanReference, "revisions must belong to the requested plan", map[string]any{
				"plan_id":            planID,
				"base_revision_id":   baseID,
				"base_plan_id":       base.PlanID,
				"target_revision_id": targetID,
				"target_plan_id":     target.PlanID,
			})
		}
		if request.BaseSerial != 0 && base.Serial != request.BaseSerial {
			return staleSerial("base_revision_id", baseID, request.BaseSerial, base.Serial)
		}
		if request.TargetSerial != 0 && target.Serial != request.TargetSerial {
			return staleSerial("target_revision_id", targetID, request.TargetSerial, target.Serial)
		}
		diff, err := domain.BuildRevisionDiff(base, target, request.Category)
		if err != nil {
			return err
		}
		result = diff
		return nil
	})
	return result, err
}

func staleSerial(field string, revisionID string, expected uint32, actual uint32) *domain.DomainError {
	return domain.NewError(domain.CodeStaleReference, "revision reference does not match its serial", map[string]any{
		"field":       field,
		"revision_id": revisionID,
		"expected":    expected,
		"actual":      actual,
	})
}
