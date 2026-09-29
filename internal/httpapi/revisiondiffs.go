package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"solo-0014-zone-rollout-control/internal/domain"
	"solo-0014-zone-rollout-control/internal/revisiondiff"
)

func (s *Server) previewRevisionDiff(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	request := revisiondiff.PreviewRequest{
		PlanID:           r.PathValue("id"),
		BaseRevisionID:   strings.TrimSpace(query.Get("base_revision_id")),
		TargetRevisionID: strings.TrimSpace(query.Get("target_revision_id")),
		Category:         domain.DiffCategory(strings.TrimSpace(query.Get("category"))),
	}
	if request.BaseRevisionID == "" {
		sendError(w, r, domain.Validation("base_revision_id query parameter is required", nil))
		return
	}
	if request.TargetRevisionID == "" {
		sendError(w, r, domain.Validation("target_revision_id query parameter is required", nil))
		return
	}
	if raw := strings.TrimSpace(query.Get("base_serial")); raw != "" {
		value, err := strconv.ParseUint(raw, 10, 32)
		if err != nil || value == 0 {
			sendError(w, r, domain.Validation("base_serial must be a positive integer", map[string]any{
				"base_serial": raw,
			}))
			return
		}
		request.BaseSerial = uint32(value)
	}
	if raw := strings.TrimSpace(query.Get("target_serial")); raw != "" {
		value, err := strconv.ParseUint(raw, 10, 32)
		if err != nil || value == 0 {
			sendError(w, r, domain.Validation("target_serial must be a positive integer", map[string]any{
				"target_serial": raw,
			}))
			return
		}
		request.TargetSerial = uint32(value)
	}
	result, err := s.diffs.Preview(r.Context(), request)
	if err != nil {
		sendError(w, r, err)
		return
	}
	sendData(w, r, http.StatusOK, result)
}
