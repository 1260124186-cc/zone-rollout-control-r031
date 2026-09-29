package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"solo-0014-zone-rollout-control/internal/domain"
)

func (s *Server) compareRevisions(w http.ResponseWriter, r *http.Request) {
	planID := r.PathValue("id")
	query := r.URL.Query()
	fromID, fromSerial, err := revisionSelector(query, "from")
	if err != nil {
		sendError(w, r, err)
		return
	}
	toID, toSerial, err := revisionSelector(query, "to")
	if err != nil {
		sendError(w, r, err)
		return
	}
	categories, err := parseDiffCategories(diffQueryValues(query, "category", "categories"))
	if err != nil {
		sendError(w, r, err)
		return
	}
	result, err := s.revisionDiffs.Compare(
		r.Context(),
		planID,
		fromID,
		toID,
		fromSerial,
		toSerial,
		categories,
	)
	if err != nil {
		sendError(w, r, err)
		return
	}
	sendData(w, r, http.StatusOK, result)
}

func revisionSelector(query url.Values, side string) (string, uint32, error) {
	id := firstQueryValue(query, side+"_revision_id", side+"_revision", side+"_id")
	serialValue := firstQueryValue(query, side+"_serial", side+"_revision_serial")
	if id == "" && serialValue == "" {
		return "", 0, domain.Validation("revision diff query is invalid", map[string]any{
			side + "_revision_id": "is required",
		})
	}
	if id != "" {
		return id, 0, nil
	}
	serial, err := strconv.ParseUint(serialValue, 10, 32)
	if err != nil || serial == 0 {
		return "", 0, domain.Validation("revision serial is invalid", map[string]any{
			side + "_serial": serialValue,
		})
	}
	return "", uint32(serial), nil
}

func firstQueryValue(query url.Values, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(query.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

func diffQueryValues(query url.Values, keys ...string) []string {
	values := make([]string, 0)
	for _, key := range keys {
		values = append(values, query[key]...)
	}
	return values
}

func parseDiffCategories(rawValues []string) ([]domain.DiffCategory, error) {
	requested := make([]domain.DiffCategory, 0)
	seen := make(map[domain.DiffCategory]struct{})
	for _, rawValue := range rawValues {
		for _, token := range strings.Split(rawValue, ",") {
			value := strings.TrimSpace(token)
			if value == "" {
				continue
			}
			category, ok := domain.NormalizeDiffCategory(value)
			if !ok {
				return nil, domain.Validation("diff category is not supported", map[string]any{
					"category": value,
					"supported": []string{
						string(domain.DiffAdded),
						string(domain.DiffRemoved),
						string(domain.DiffValueChanged),
					},
				})
			}
			if _, exists := seen[category]; !exists {
				seen[category] = struct{}{}
				requested = append(requested, category)
			}
		}
	}
	categories := make([]domain.DiffCategory, 0, len(requested))
	for _, category := range []domain.DiffCategory{
		domain.DiffAdded,
		domain.DiffRemoved,
		domain.DiffValueChanged,
	} {
		if _, exists := seen[category]; exists {
			categories = append(categories, category)
		}
	}
	return categories, nil
}
