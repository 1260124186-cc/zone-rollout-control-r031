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
	DiffAdded   DiffCategory = "added"
	DiffRemoved DiffCategory = "removed"
	DiffChanged DiffCategory = "changed"
)

const FingerprintSHA256 = "sha256"

type EntryDiff struct {
	Category DiffCategory `json:"category"`
	Owner    string       `json:"owner"`
	Type     EntryType    `json:"type"`
	OldValue string       `json:"old_value,omitempty"`
	NewValue string       `json:"new_value,omitempty"`
}

type RevisionRef struct {
	RevisionID string `json:"revision_id"`
	Serial     uint32 `json:"serial"`
	Authority  string `json:"authority"`
	EntryCount int    `json:"entry_count"`
}

type RevisionDiffSummary struct {
	Added            int  `json:"added"`
	Removed          int  `json:"removed"`
	Changed          int  `json:"changed"`
	Unchanged        int  `json:"unchanged"`
	TotalCompared    int  `json:"total_compared"`
	AuthorityChanged bool `json:"authority_changed"`
}

type RevisionDiff struct {
	PlanID          string              `json:"plan_id"`
	Direction       string              `json:"direction"`
	Base            RevisionRef         `json:"base"`
	Target          RevisionRef         `json:"target"`
	Summary         RevisionDiffSummary `json:"summary"`
	Entries         []EntryDiff         `json:"entries"`
	FilterCategory  DiffCategory        `json:"filter_category,omitempty"`
	Filtered        bool                `json:"filtered"`
	Fingerprint     string              `json:"fingerprint"`
	FingerprintAlgo string              `json:"fingerprint_algorithm"`
}

type entryKey struct {
	owner string
	typ   EntryType
}

type fingerprintRevision struct {
	RevisionID string `json:"revision_id"`
	Serial     uint32 `json:"serial"`
	Authority  string `json:"authority"`
}

type fingerprintEntry struct {
	Category string `json:"category"`
	Owner    string `json:"owner"`
	Type     string `json:"type"`
	OldValue string `json:"old_value"`
	NewValue string `json:"new_value"`
}

// BuildRevisionDiff computes a deterministic, read-only comparison of two
// immutable zone revisions. The fingerprint always covers the complete change
// set, so an optional category filter narrows the reported entries without
// changing the preview identity.
func BuildRevisionDiff(base ZoneRevision, target ZoneRevision, filter DiffCategory) (RevisionDiff, error) {
	if filter != "" && filter != DiffAdded && filter != DiffRemoved && filter != DiffChanged {
		return RevisionDiff{}, Validation("diff category is not supported", map[string]any{
			"category": string(filter),
		})
	}
	changes, unchanged := diffEntries(base.Entries, target.Entries)
	summary := RevisionDiffSummary{
		Added:            countCategory(changes, DiffAdded),
		Removed:          countCategory(changes, DiffRemoved),
		Changed:          countCategory(changes, DiffChanged),
		Unchanged:        unchanged,
		AuthorityChanged: base.Authority != target.Authority,
	}
	summary.TotalCompared = summary.Added + summary.Removed + summary.Changed + summary.Unchanged

	diff := RevisionDiff{
		PlanID:          base.PlanID,
		Direction:       fmt.Sprintf("%d->%d", base.Serial, target.Serial),
		Base:            revisionRef(base),
		Target:          revisionRef(target),
		Summary:         summary,
		Entries:         make([]EntryDiff, 0, len(changes)),
		Fingerprint:     revisionDiffFingerprint(base.PlanID, base, target, changes),
		FingerprintAlgo: FingerprintSHA256,
	}
	for _, change := range changes {
		if filter == "" || change.Category == filter {
			diff.Entries = append(diff.Entries, change)
		}
	}
	if filter != "" {
		diff.Filtered = true
		diff.FilterCategory = filter
	}
	return diff, nil
}

func revisionRef(revision ZoneRevision) RevisionRef {
	return RevisionRef{
		RevisionID: revision.ID,
		Serial:     revision.Serial,
		Authority:  revision.Authority,
		EntryCount: revision.EntryCount,
	}
}

func countCategory(changes []EntryDiff, category DiffCategory) int {
	count := 0
	for _, change := range changes {
		if change.Category == category {
			count++
		}
	}
	return count
}

// diffEntries performs a deterministic multiset comparison per (owner, type)
// key: shared values are unchanged, surplus values are aligned into changed
// pairs first, and any remaining values are reported as pure adds or removes.
func diffEntries(baseEntries []ZoneEntry, targetEntries []ZoneEntry) ([]EntryDiff, int) {
	baseByKey := make(map[entryKey][]string)
	targetByKey := make(map[entryKey][]string)
	keySet := make(map[entryKey]struct{})
	for _, entry := range baseEntries {
		key := entryKey{owner: entry.Owner, typ: entry.Type}
		baseByKey[key] = append(baseByKey[key], entry.Value)
		keySet[key] = struct{}{}
	}
	for _, entry := range targetEntries {
		key := entryKey{owner: entry.Owner, typ: entry.Type}
		targetByKey[key] = append(targetByKey[key], entry.Value)
		keySet[key] = struct{}{}
	}
	keys := make([]entryKey, 0, len(keySet))
	for key := range keySet {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].owner != keys[j].owner {
			return keys[i].owner < keys[j].owner
		}
		return keys[i].typ < keys[j].typ
	})

	changes := make([]EntryDiff, 0)
	unchanged := 0
	for _, key := range keys {
		baseValues := append([]string(nil), baseByKey[key]...)
		targetValues := append([]string(nil), targetByKey[key]...)
		sort.Strings(baseValues)
		sort.Strings(targetValues)

		removedValues := make([]string, 0)
		addedValues := make([]string, 0)
		i, j := 0, 0
		for i < len(baseValues) && j < len(targetValues) {
			switch {
			case baseValues[i] == targetValues[j]:
				unchanged++
				i++
				j++
			case baseValues[i] < targetValues[j]:
				removedValues = append(removedValues, baseValues[i])
				i++
			default:
				addedValues = append(addedValues, targetValues[j])
				j++
			}
		}
		removedValues = append(removedValues, baseValues[i:]...)
		addedValues = append(addedValues, targetValues[j:]...)

		paired := len(removedValues)
		if len(addedValues) < paired {
			paired = len(addedValues)
		}
		for index := 0; index < paired; index++ {
			changes = append(changes, EntryDiff{
				Category: DiffChanged,
				Owner:    key.owner,
				Type:     key.typ,
				OldValue: removedValues[index],
				NewValue: addedValues[index],
			})
		}
		for _, value := range removedValues[paired:] {
			changes = append(changes, EntryDiff{
				Category: DiffRemoved,
				Owner:    key.owner,
				Type:     key.typ,
				OldValue: value,
			})
		}
		for _, value := range addedValues[paired:] {
			changes = append(changes, EntryDiff{
				Category: DiffAdded,
				Owner:    key.owner,
				Type:     key.typ,
				NewValue: value,
			})
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Owner != changes[j].Owner {
			return changes[i].Owner < changes[j].Owner
		}
		if changes[i].Type != changes[j].Type {
			return changes[i].Type < changes[j].Type
		}
		if changes[i].Category != changes[j].Category {
			return changes[i].Category < changes[j].Category
		}
		if changes[i].OldValue != changes[j].OldValue {
			return changes[i].OldValue < changes[j].OldValue
		}
		return changes[i].NewValue < changes[j].NewValue
	})
	return changes, unchanged
}

func revisionDiffFingerprint(
	planID string,
	base ZoneRevision,
	target ZoneRevision,
	changes []EntryDiff,
) string {
	entries := make([]fingerprintEntry, 0, len(changes))
	for _, change := range changes {
		entries = append(entries, fingerprintEntry{
			Category: string(change.Category),
			Owner:    change.Owner,
			Type:     string(change.Type),
			OldValue: change.OldValue,
			NewValue: change.NewValue,
		})
	}
	payload := struct {
		PlanID  string              `json:"plan_id"`
		Base    fingerprintRevision `json:"base"`
		Target  fingerprintRevision `json:"target"`
		Entries []fingerprintEntry  `json:"entries"`
	}{
		PlanID: planID,
		Base: fingerprintRevision{
			RevisionID: base.ID,
			Serial:     base.Serial,
			Authority:  base.Authority,
		},
		Target: fingerprintRevision{
			RevisionID: target.ID,
			Serial:     target.Serial,
			Authority:  target.Authority,
		},
		Entries: entries,
	}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return FingerprintSHA256 + ":" + hex.EncodeToString(sum[:])
}
