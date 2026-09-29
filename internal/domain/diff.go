package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

type DiffCategory string

const (
	DiffAdded        DiffCategory = "added"
	DiffRemoved      DiffCategory = "removed"
	DiffValueChanged DiffCategory = "value_changed"
)

type RevisionDiffSide struct {
	ID                 string `json:"id"`
	PlanID             string `json:"plan_id"`
	Serial             uint32 `json:"serial"`
	Authority          string `json:"authority"`
	EntryCount         int    `json:"entry_count"`
	ContentFingerprint string `json:"content_fingerprint"`
}

type RevisionDiffSummary struct {
	FromEntryCount    int  `json:"from_entry_count"`
	ToEntryCount      int  `json:"to_entry_count"`
	Added             int  `json:"added"`
	Removed           int  `json:"removed"`
	ValueChanged      int  `json:"value_changed"`
	Unchanged         int  `json:"unchanged"`
	TotalChanges      int  `json:"total_changes"`
	EntriesAdded      int  `json:"entries_added"`
	EntriesRemoved    int  `json:"entries_removed"`
	SerialChanged     bool `json:"serial_changed"`
	AuthorityChanged  bool `json:"authority_changed"`
}

type RevisionDiffItem struct {
	Category DiffCategory `json:"category"`
	Owner    string       `json:"owner"`
	Type     EntryType    `json:"type"`
	Before []ZoneEntry `json:"before"`
	After  []ZoneEntry `json:"after"`
}

type RevisionDiff struct {
	PlanID               string             `json:"plan_id"`
	From                 RevisionDiffSide   `json:"from"`
	To                   RevisionDiffSide   `json:"to"`
	Summary              RevisionDiffSummary `json:"summary"`
	Changes              []RevisionDiffItem `json:"changes"`
	Fingerprint          string             `json:"fingerprint"`
	IdentityFingerprint  string             `json:"identity_fingerprint"`
	FingerprintAlgorithm string             `json:"fingerprint_algorithm"`
	Filter               RevisionDiffFilter `json:"filter"`
}

type RevisionDiffFilter struct {
	Categories []DiffCategory `json:"categories"`
}

type revisionEntryKey struct {
	Owner string
	Type  EntryType
}

// BuildRevisionDiff compares two immutable revisions of the same plan. The
// result is deterministic: changes are grouped by normalized owner+type and
// ordered by category, owner and type. It performs no I/O and never mutates
// either revision.
func BuildRevisionDiff(planID string, from, to ZoneRevision) (RevisionDiff, error) {
	if from.PlanID != planID || to.PlanID != planID {
		return RevisionDiff{}, Validation("revisions must belong to the requested plan", map[string]any{
			"plan_id": planID,
			"from":    from.PlanID,
			"to":      to.PlanID,
		})
	}

	fromGroups := groupRevisionEntries(from.Entries)
	toGroups := groupRevisionEntries(to.Entries)
	keys := unionRevisionEntryKeys(fromGroups, toGroups)
	changes := make([]RevisionDiffItem, 0)
	summary := RevisionDiffSummary{
		FromEntryCount:   len(from.Entries),
		ToEntryCount:     len(to.Entries),
		SerialChanged:    from.Serial != to.Serial,
		AuthorityChanged: from.Authority != to.Authority,
	}

	for _, key := range keys {
		before := fromGroups[key]
		after := toGroups[key]
		switch {
		case before == nil:
			changes = append(changes, RevisionDiffItem{
				Category: DiffAdded,
				Owner:    key.Owner,
				Type:     key.Type,
				Before:   []ZoneEntry{},
				After:    append([]ZoneEntry(nil), after...),
			})
			summary.Added++
			summary.EntriesAdded += len(after)
		case after == nil:
			changes = append(changes, RevisionDiffItem{
				Category: DiffRemoved,
				Owner:    key.Owner,
				Type:     key.Type,
				Before:   append([]ZoneEntry(nil), before...),
				After:    []ZoneEntry{},
			})
			summary.Removed++
			summary.EntriesRemoved += len(before)
		case !equalZoneEntries(before, after):
			changes = append(changes, RevisionDiffItem{
				Category: DiffValueChanged,
				Owner:    key.Owner,
				Type:     key.Type,
				Before:   append([]ZoneEntry(nil), before...),
				After:    append([]ZoneEntry(nil), after...),
			})
			summary.ValueChanged++
			summary.EntriesAdded += countMissingValues(after, before)
			summary.EntriesRemoved += countMissingValues(before, after)
		default:
			summary.Unchanged++
		}
	}

	sortRevisionDiffItems(changes)
	summary.TotalChanges = summary.Added + summary.Removed + summary.ValueChanged
	diff := RevisionDiff{
		PlanID:  planID,
		From:    revisionDiffSide(from),
		To:      revisionDiffSide(to),
		Summary: summary,
		Changes: changes,
		Filter:  RevisionDiffFilter{Categories: []DiffCategory{}},
	}
	diff.Fingerprint = revisionFingerprint(diff)
	diff.IdentityFingerprint = diff.Fingerprint
	diff.FingerprintAlgorithm = "sha256"
	return diff, nil
}

// FilterRevisionDiff narrows the visible change items without altering the
// full summary or the identity fingerprint, which always describes the
// complete comparison.
func FilterRevisionDiff(diff RevisionDiff, categories []DiffCategory) RevisionDiff {
	if len(categories) == 0 {
		diff.Filter.Categories = []DiffCategory{}
		diff.Changes = append([]RevisionDiffItem(nil), diff.Changes...)
		return diff
	}
	allowed := make(map[DiffCategory]struct{}, len(categories))
	for _, category := range categories {
		allowed[category] = struct{}{}
	}
	filtered := make([]RevisionDiffItem, 0, len(diff.Changes))
	for _, change := range diff.Changes {
		if _, ok := allowed[change.Category]; ok {
			filtered = append(filtered, change)
		}
	}
	diff.Changes = filtered
	diff.Filter.Categories = append([]DiffCategory(nil), categories...)
	return diff
}

func NormalizeDiffCategory(value string) (DiffCategory, bool) {
	switch value {
	case string(DiffAdded), "add", "addition", "create", "created", "insert", "inserted":
		return DiffAdded, true
	case string(DiffRemoved), "deleted", "delete", "deletion", "remove", "drop", "dropped":
		return DiffRemoved, true
	case string(DiffValueChanged), "value-changed", "value-change", "changed", "change", "modified", "update", "updated":
		return DiffValueChanged, true
	default:
		return "", false
	}
}

func ValidateDiffCategories(categories []DiffCategory) error {
	for _, category := range categories {
		switch category {
		case DiffAdded, DiffRemoved, DiffValueChanged:
		default:
			return Validation(fmt.Sprintf("diff category %q is not supported", category), map[string]any{
				"category": category,
			})
		}
	}
	return nil
}

func revisionDiffSide(revision ZoneRevision) RevisionDiffSide {
	entries := normalizedZoneEntries(revision.Entries)
	return RevisionDiffSide{
		ID:                 revision.ID,
		PlanID:             revision.PlanID,
		Serial:             revision.Serial,
		Authority:          revision.Authority,
		EntryCount:         len(entries),
		ContentFingerprint: revisionContentFingerprint(revision, entries),
	}
}

func revisionContentFingerprint(revision ZoneRevision, entries []ZoneEntry) string {
	payload, _ := json.Marshal(struct {
		Serial    uint32      `json:"serial"`
		Authority string      `json:"authority"`
		Entries   []ZoneEntry `json:"entries"`
	}{
		Serial:    revision.Serial,
		Authority: revision.Authority,
		Entries:   entries,
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func revisionFingerprint(diff RevisionDiff) string {
	payload, _ := json.Marshal(struct {
		PlanID  string                  `json:"plan_id"`
		From    revisionFingerprintSide `json:"from"`
		To      revisionFingerprintSide `json:"to"`
		Changes []RevisionDiffItem      `json:"changes"`
	}{
		PlanID: diff.PlanID,
		From: revisionFingerprintSide{
			ID:                 diff.From.ID,
			Serial:             diff.From.Serial,
			Authority:          diff.From.Authority,
			ContentFingerprint: diff.From.ContentFingerprint,
		},
		To: revisionFingerprintSide{
			ID:                 diff.To.ID,
			Serial:             diff.To.Serial,
			Authority:          diff.To.Authority,
			ContentFingerprint: diff.To.ContentFingerprint,
		},
		Changes: diff.Changes,
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

type revisionFingerprintSide struct {
	ID                 string `json:"id"`
	Serial             uint32 `json:"serial"`
	Authority          string `json:"authority"`
	ContentFingerprint string `json:"content_fingerprint"`
}

func groupRevisionEntries(entries []ZoneEntry) map[revisionEntryKey][]ZoneEntry {
	normalized := normalizedZoneEntries(entries)
	grouped := make(map[revisionEntryKey][]ZoneEntry)
	for _, entry := range normalized {
		key := revisionEntryKey{Owner: entry.Owner, Type: entry.Type}
		grouped[key] = append(grouped[key], entry)
	}
	return grouped
}

func normalizedZoneEntries(entries []ZoneEntry) []ZoneEntry {
	normalized := append([]ZoneEntry(nil), entries...)
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].Owner != normalized[j].Owner {
			return normalized[i].Owner < normalized[j].Owner
		}
		if normalized[i].Type != normalized[j].Type {
			return normalized[i].Type < normalized[j].Type
		}
		return normalized[i].Value < normalized[j].Value
	})
	return normalized
}

func unionRevisionEntryKeys(from, to map[revisionEntryKey][]ZoneEntry) []revisionEntryKey {
	seen := make(map[revisionEntryKey]struct{}, len(from)+len(to))
	keys := make([]revisionEntryKey, 0, len(from)+len(to))
	for key := range from {
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for key := range to {
		if _, ok := seen[key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Owner != keys[j].Owner {
			return keys[i].Owner < keys[j].Owner
		}
		return keys[i].Type < keys[j].Type
	})
	return keys
}

func sortRevisionDiffItems(items []RevisionDiffItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Category != items[j].Category {
			return diffCategoryOrder(items[i].Category) < diffCategoryOrder(items[j].Category)
		}
		if items[i].Owner != items[j].Owner {
			return items[i].Owner < items[j].Owner
		}
		return items[i].Type < items[j].Type
	})
}

func diffCategoryOrder(category DiffCategory) int {
	switch category {
	case DiffAdded:
		return 0
	case DiffRemoved:
		return 1
	case DiffValueChanged:
		return 2
	default:
		return 3
	}
}

func equalZoneEntries(left, right []ZoneEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func countMissingValues(entries, reference []ZoneEntry) int {
	count := 0
	for _, entry := range entries {
		if !zoneEntryExists(entry, reference) {
			count++
		}
	}
	return count
}

func zoneEntryExists(entry ZoneEntry, entries []ZoneEntry) bool {
	for _, candidate := range entries {
		if candidate == entry {
			return true
		}
	}
	return false
}
