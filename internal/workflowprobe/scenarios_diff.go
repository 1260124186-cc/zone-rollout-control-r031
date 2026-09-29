package workflowprobe

import (
	"fmt"
	"net/http"
	"net/url"

	"solo-0014-zone-rollout-control/internal/domain"
)

func runRevisionDiff() error {
	harness, err := newHarness()
	if err != nil {
		return err
	}
	defer harness.close()

	var plan domain.RolloutPlan
	if err := harness.call(http.MethodPut, "/v1/plans", domain.PlanCreateInput{
		Zone:                "diff.example",
		MinimumSuccessRatio: 1,
		MaximumLatencyMS:    80,
		ExpectedNodes:       1,
	}, &plan); err != nil {
		return err
	}

	var first domain.ZoneRevision
	if err := harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/revisions", domain.RevisionCreateInput{
		Serial:    5001,
		Authority: "ns1.diff.example",
		Entries: []domain.ZoneEntry{
			{Owner: "a.diff.example", Type: domain.EntryA, Value: "192.0.2.1"},
			{Owner: "a.diff.example", Type: domain.EntryAAAA, Value: "2001:db8::1"},
			{Owner: "b.diff.example", Type: domain.EntryCNAME, Value: "target.diff.example"},
			{Owner: "same.diff.example", Type: domain.EntryA, Value: "192.0.2.50"},
		},
	}, &first); err != nil {
		return err
	}

	var second domain.ZoneRevision
	if err := harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/revisions", domain.RevisionCreateInput{
		Serial:    5002,
		Authority: "ns2.diff.example",
		Entries: []domain.ZoneEntry{
			{Owner: "a.diff.example", Type: domain.EntryA, Value: "192.0.2.2"},
			{Owner: "b.diff.example", Type: domain.EntryCNAME, Value: "target.diff.example"},
			{Owner: "new.diff.example", Type: domain.EntryCNAME, Value: "new-target.diff.example"},
			{Owner: "same.diff.example", Type: domain.EntryA, Value: "192.0.2.50"},
		},
	}, &second); err != nil {
		return err
	}

	var firstDiff domain.RevisionDiff
	if err := getRevisionDiff(harness, plan.ID, first.ID, second.ID, nil, &firstDiff); err != nil {
		return err
	}
	if err := expectRevisionDiffShape(firstDiff, plan.ID, first.ID, second.ID); err != nil {
		return err
	}
	if err := expectFullDiffSummary(firstDiff.Summary); err != nil {
		return err
	}
	if len(firstDiff.Changes) != 3 {
		return ensure(false, "full revision diff must contain three categorized changes")
	}
	if firstDiff.Changes[0].Category != domain.DiffAdded || firstDiff.Changes[0].Owner != "new.diff.example" {
		return ensure(false, "added changes are not first in fixed order")
	}
	if firstDiff.Changes[1].Category != domain.DiffRemoved || firstDiff.Changes[1].Owner != "a.diff.example" || firstDiff.Changes[1].Type != domain.EntryAAAA {
		return ensure(false, "removed AAAA change is not second in fixed order")
	}
	if firstDiff.Changes[2].Category != domain.DiffValueChanged ||
		len(firstDiff.Changes[2].Before) != 1 || firstDiff.Changes[2].Before[0].Value != "192.0.2.1" ||
		len(firstDiff.Changes[2].After) != 1 || firstDiff.Changes[2].After[0].Value != "192.0.2.2" {
		return ensure(false, "value change does not preserve complete before and after entries")
	}
	if firstDiff.From.ContentFingerprint == firstDiff.To.ContentFingerprint {
		return ensure(false, "revision sides need distinct content fingerprints")
	}
	if firstDiff.Fingerprint == "" || firstDiff.FingerprintAlgorithm != "sha256" {
		return ensure(false, "revision diff identity fingerprint is incomplete")
	}

	var replay domain.RevisionDiff
	if err := getRevisionDiff(harness, plan.ID, first.ID, second.ID, nil, &replay); err != nil {
		return err
	}
	if replay.Fingerprint != firstDiff.Fingerprint {
		return ensure(false, "identical revision diff request is not reproducible")
	}

	var bySerial domain.RevisionDiff
	if err := getRevisionDiffBySerial(harness, plan.ID, first.Serial, second.Serial, &bySerial); err != nil {
		return err
	}
	if bySerial.Fingerprint != firstDiff.Fingerprint {
		return ensure(false, "revision serial references must identify the same comparison")
	}

	var addedOnly domain.RevisionDiff
	if err := getRevisionDiff(harness, plan.ID, first.ID, second.ID, []string{"added"}, &addedOnly); err != nil {
		return err
	}
	if len(addedOnly.Changes) != 1 || addedOnly.Changes[0].Category != domain.DiffAdded {
		return ensure(false, "added category filter did not return exactly one change")
	}
	if addedOnly.Fingerprint != firstDiff.Fingerprint || addedOnly.Summary.TotalChanges != 3 {
		return ensure(false, "category filter must preserve full summary and identity")
	}

	var changedOnly domain.RevisionDiff
	if err := getRevisionDiff(harness, plan.ID, first.ID, second.ID, []string{"changed"}, &changedOnly); err != nil {
		return err
	}
	if len(changedOnly.Changes) != 1 || changedOnly.Changes[0].Category != domain.DiffValueChanged {
		return ensure(false, "changed category alias did not select value changes")
	}

	var reverse domain.RevisionDiff
	if err := getRevisionDiff(harness, plan.ID, second.ID, first.ID, nil, &reverse); err != nil {
		return err
	}
	if reverse.Fingerprint == firstDiff.Fingerprint ||
		reverse.Summary.Added != 1 || reverse.Summary.Removed != 1 || reverse.Summary.ValueChanged != 1 {
		return ensure(false, "reverse comparison does not have its own stable identity")
	}

	var otherPlan domain.RolloutPlan
	if err := harness.call(http.MethodPut, "/v1/plans", domain.PlanCreateInput{
		Zone:                "other.example",
		MinimumSuccessRatio: 1,
		MaximumLatencyMS:    80,
		ExpectedNodes:       1,
	}, &otherPlan); err != nil {
		return err
	}
	var otherRevision domain.ZoneRevision
	if err := harness.call(http.MethodPatch, "/v1/plans/"+otherPlan.ID+"/revisions", domain.RevisionCreateInput{
		Serial:    6001,
		Authority: "ns1.other.example",
		Entries: []domain.ZoneEntry{
			{Owner: "other.example", Type: domain.EntryA, Value: "198.51.100.1"},
		},
	}, &otherRevision); err != nil {
		return err
	}
	if err := getRevisionDiff(harness, plan.ID, first.ID, otherRevision.ID, nil, nil); err != nil {
		if expectFailure(err, http.StatusNotFound, string(domain.CodeNotFound)) != nil {
			return err
		}
	} else {
		return ensure(false, "cross-plan revision reference must fail")
	}
	if err := getRevisionDiff(harness, plan.ID, first.ID, "rev_missing", nil, nil); err != nil {
		if expectFailure(err, http.StatusNotFound, string(domain.CodeNotFound)) != nil {
			return err
		}
	} else {
		return ensure(false, "missing revision reference must fail")
	}
	if err := getRevisionDiff(harness, plan.ID, first.ID, second.ID, []string{"unknown"}, nil); err != nil {
		if expectFailure(err, http.StatusBadRequest, string(domain.CodeValidationFailed)) != nil {
			return err
		}
	} else {
		return ensure(false, "unknown diff category must fail")
	}

	var later domain.ZoneRevision
	if err := harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/revisions", domain.RevisionCreateInput{
		Serial:    5003,
		Authority: "ns2.diff.example",
		Entries: append(second.Entries, domain.ZoneEntry{
			Owner: "future.diff.example",
			Type:  domain.EntryA,
			Value: "192.0.2.99",
		}),
	}, &later); err != nil {
		return err
	}
	var afterLaterRevision domain.RevisionDiff
	if err := getRevisionDiff(harness, plan.ID, first.ID, second.ID, nil, &afterLaterRevision); err != nil {
		return err
	}
	if afterLaterRevision.Fingerprint != firstDiff.Fingerprint {
		return ensure(false, "later revisions must not rewrite an existing immutable comparison")
	}
	_ = later
	return nil
}

func getRevisionDiff(
	harness *harness,
	planID string,
	fromID string,
	toID string,
	categories []string,
	target any,
) error {
	return getRevisionDiffQuery(harness, planID, url.Values{
		"from_revision_id": {fromID},
		"to_revision_id":   {toID},
	}, categories, target)
}

func getRevisionDiffBySerial(
	harness *harness,
	planID string,
	fromSerial uint32,
	toSerial uint32,
	target any,
) error {
	query := url.Values{}
	query.Set("from_serial", fmt.Sprintf("%d", fromSerial))
	query.Set("to_serial", fmt.Sprintf("%d", toSerial))
	return getRevisionDiffQuery(harness, planID, query, nil, target)
}

func getRevisionDiffQuery(
	harness *harness,
	planID string,
	query url.Values,
	categories []string,
	target any,
) error {
	for _, category := range categories {
		query.Add("category", category)
	}
	path := fmt.Sprintf("/v1/plans/%s/revision-diff?%s", url.PathEscape(planID), query.Encode())
	return harness.call(http.MethodGet, path, nil, target)
}

func expectRevisionDiffShape(diff domain.RevisionDiff, planID, fromID, toID string) error {
	if diff.PlanID != planID || diff.From.ID != fromID || diff.To.ID != toID {
		return ensure(false, "revision diff did not retain requested identity")
	}
	if diff.From.PlanID != planID || diff.To.PlanID != planID {
		return ensure(false, "revision diff sides do not identify the plan")
	}
	if diff.From.Serial != 5001 || diff.To.Serial != 5002 {
		return ensure(false, "revision diff side serials are incorrect")
	}
	return nil
}

func expectFullDiffSummary(summary domain.RevisionDiffSummary) error {
	if summary.FromEntryCount != 4 || summary.ToEntryCount != 4 {
		return ensure(false, "revision diff entry counts are incorrect")
	}
	if summary.Added != 1 || summary.Removed != 1 || summary.ValueChanged != 1 || summary.Unchanged != 2 {
		return ensure(false, "revision diff category summary is incorrect")
	}
	if summary.EntriesAdded != 2 || summary.EntriesRemoved != 2 || summary.TotalChanges != 3 {
		return ensure(false, "revision diff value summary is incorrect")
	}
	return nil
}
