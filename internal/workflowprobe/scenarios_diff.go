package workflowprobe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"solo-0014-zone-rollout-control/internal/domain"
)

func runRevisionDiffPreview() error {
	harness, err := newHarness()
	if err != nil {
		return err
	}
	defer harness.close()

	var plan domain.RolloutPlan
	if err := harness.call(http.MethodPut, "/v1/plans", domain.PlanCreateInput{
		Zone:                "edge.example",
		MinimumSuccessRatio: 1,
		MaximumLatencyMS:    80,
		ExpectedNodes:       2,
	}, &plan); err != nil {
		return err
	}

	var revision1 domain.ZoneRevision
	if err := harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/revisions", domain.RevisionCreateInput{
		Serial:    100,
		Authority: "ns1.edge.example",
		Entries: []domain.ZoneEntry{
			{Owner: "edge.example", Type: domain.EntryA, Value: "192.0.2.1"},
			{Owner: "www.edge.example", Type: domain.EntryCNAME, Value: "edge.example"},
			{Owner: "api.edge.example", Type: domain.EntryA, Value: "192.0.2.5"},
		},
	}, &revision1); err != nil {
		return err
	}
	var revision2 domain.ZoneRevision
	if err := harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/revisions", domain.RevisionCreateInput{
		Serial:    200,
		Authority: "ns2.edge.example",
		Entries: []domain.ZoneEntry{
			{Owner: "edge.example", Type: domain.EntryA, Value: "192.0.2.2"},
			{Owner: "www.edge.example", Type: domain.EntryCNAME, Value: "edge.example"},
			{Owner: "cdn.edge.example", Type: domain.EntryCNAME, Value: "cdn-origin.example"},
		},
	}, &revision2); err != nil {
		return err
	}

	diffPath := "/v1/plans/" + plan.ID + "/revisions/diff" +
		"?base_revision_id=" + revision1.ID +
		"&target_revision_id=" + revision2.ID

	var before domain.RolloutPlan
	if err := harness.call(http.MethodGet, "/v1/plans/"+plan.ID, nil, &before); err != nil {
		return err
	}

	var diff domain.RevisionDiff
	if err := harness.call(http.MethodGet, diffPath, nil, &diff); err != nil {
		return err
	}

	var after domain.RolloutPlan
	if err := harness.call(http.MethodGet, "/v1/plans/"+plan.ID, nil, &after); err != nil {
		return err
	}
	if before.Version != after.Version {
		return fmt.Errorf("diff preview mutated plan version: %d -> %d", before.Version, after.Version)
	}

	if diff.PlanID != plan.ID || diff.Direction != "100->200" {
		return ensure(false, "diff identity fields are wrong")
	}
	if diff.Base.RevisionID != revision1.ID || diff.Base.Serial != 100 ||
		diff.Target.RevisionID != revision2.ID || diff.Target.Serial != 200 {
		return ensure(false, "diff revision references are wrong")
	}
	if diff.Base.Authority != "ns1.edge.example" || diff.Target.Authority != "ns2.edge.example" {
		return ensure(false, "diff authority references are wrong")
	}
	if diff.Summary.Added != 1 || diff.Summary.Removed != 1 || diff.Summary.Changed != 1 ||
		diff.Summary.Unchanged != 1 || diff.Summary.TotalCompared != 4 || !diff.Summary.AuthorityChanged {
		return ensure(false, fmt.Sprintf("diff summary is wrong: %+v", diff.Summary))
	}
	wantOrder := []struct {
		owner    string
		category domain.DiffCategory
		old      string
		new      string
	}{
		{"api.edge.example", domain.DiffRemoved, "192.0.2.5", ""},
		{"cdn.edge.example", domain.DiffAdded, "", "cdn-origin.example"},
		{"edge.example", domain.DiffChanged, "192.0.2.1", "192.0.2.2"},
	}
	if len(diff.Entries) != len(wantOrder) {
		return ensure(false, fmt.Sprintf("expected %d entries, got %d", len(wantOrder), len(diff.Entries)))
	}
	for index, want := range wantOrder {
		got := diff.Entries[index]
		if got.Owner != want.owner || got.Category != want.category ||
			got.OldValue != want.old || got.NewValue != want.new {
			return ensure(false, fmt.Sprintf("entry %d is %+v, want %+v", index, got, want))
		}
	}
	if diff.FingerprintAlgo != domain.FingerprintSHA256 ||
		len(diff.Fingerprint) != len("sha256:")+64 || diff.Filtered {
		return ensure(false, "diff fingerprint or filter flags are wrong")
	}

	// The same request must reproduce exactly, including its fingerprint.
	var repeated domain.RevisionDiff
	if err := harness.call(http.MethodGet, diffPath, nil, &repeated); err != nil {
		return err
	}
	if repeated.Fingerprint != diff.Fingerprint {
		return ensure(false, "repeated preview produced a different fingerprint")
	}
	if err := compareJSON(repeated, diff); err != nil {
		return fmt.Errorf("repeated preview is not stable: %w", err)
	}

	// Category filters narrow entries but never change summary or fingerprint.
	for _, category := range []domain.DiffCategory{domain.DiffAdded, domain.DiffRemoved, domain.DiffChanged} {
		var filtered domain.RevisionDiff
		if err := harness.call(http.MethodGet, diffPath+"&category="+string(category), nil, &filtered); err != nil {
			return err
		}
		if !filtered.Filtered || filtered.FilterCategory != category {
			return ensure(false, "filtered preview did not report its filter")
		}
		if len(filtered.Entries) != 1 || filtered.Entries[0].Category != category {
			return ensure(false, "category filter returned the wrong entries")
		}
		if filtered.Fingerprint != diff.Fingerprint || filtered.Summary != diff.Summary {
			return ensure(false, "filtering altered fingerprint or summary")
		}
	}
	if err := harness.call(http.MethodGet, diffPath+"&category=bogus", nil, nil); err != nil {
		if err := expectFailure(err, http.StatusBadRequest, string(domain.CodeValidationFailed)); err != nil {
			return err
		}
	} else {
		return ensure(false, "bogus category was accepted")
	}

	// Reverse direction mirrors add/remove and produces its own identity.
	var reverse domain.RevisionDiff
	reversePath := "/v1/plans/" + plan.ID + "/revisions/diff" +
		"?base_revision_id=" + revision2.ID +
		"&target_revision_id=" + revision1.ID
	if err := harness.call(http.MethodGet, reversePath, nil, &reverse); err != nil {
		return err
	}
	if reverse.Direction != "200->100" || reverse.Fingerprint == diff.Fingerprint {
		return ensure(false, "reverse preview has a wrong identity")
	}
	if reverse.Summary.Added != 1 || reverse.Summary.Removed != 1 || reverse.Summary.Changed != 1 {
		return ensure(false, "reverse preview has wrong categories")
	}

	// A later revision cannot retroactively change an already generated preview.
	var revision3 domain.ZoneRevision
	if err := harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/revisions", domain.RevisionCreateInput{
		Serial:    300,
		Authority: "ns2.edge.example",
		Entries: []domain.ZoneEntry{
			{Owner: "edge.example", Type: domain.EntryA, Value: "192.0.2.2"},
			{Owner: "new.edge.example", Type: domain.EntryAAAA, Value: "2001:db8::1"},
		},
	}, &revision3); err != nil {
		return err
	}
	var afterNewRevision domain.RevisionDiff
	if err := harness.call(http.MethodGet, diffPath, nil, &afterNewRevision); err != nil {
		return err
	}
	if afterNewRevision.Fingerprint != diff.Fingerprint {
		return ensure(false, "later revision rewrote an existing preview fingerprint")
	}

	// Stale serial references must fail explicitly.
	if err := harness.call(http.MethodGet, diffPath+"&base_serial=999", nil, nil); err != nil {
		if err := expectFailure(err, http.StatusConflict, string(domain.CodeStaleReference)); err != nil {
			return err
		}
	} else {
		return ensure(false, "stale base serial was accepted")
	}

	// Cross-plan references must fail explicitly.
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
		Serial:    100,
		Authority: "ns1.other.example",
		Entries: []domain.ZoneEntry{
			{Owner: "other.example", Type: domain.EntryA, Value: "198.51.100.7"},
		},
	}, &otherRevision); err != nil {
		return err
	}
	crossPath := "/v1/plans/" + plan.ID + "/revisions/diff" +
		"?base_revision_id=" + revision1.ID +
		"&target_revision_id=" + otherRevision.ID
	if err := harness.call(http.MethodGet, crossPath, nil, nil); err != nil {
		if err := expectFailure(err, http.StatusConflict, string(domain.CodeCrossPlanReference)); err != nil {
			return err
		}
	} else {
		return ensure(false, "cross-plan reference was accepted")
	}

	// Missing entities and parameters fail with stable errors.
	missingRevisionPath := "/v1/plans/" + plan.ID + "/revisions/diff" +
		"?base_revision_id=rev_missing&target_revision_id=" + revision2.ID
	if err := harness.call(http.MethodGet, missingRevisionPath, nil, nil); err != nil {
		if err := expectFailure(err, http.StatusNotFound, string(domain.CodeNotFound)); err != nil {
			return err
		}
	} else {
		return ensure(false, "missing revision was accepted")
	}
	missingPlanPath := "/v1/plans/pln_missing/revisions/diff" +
		"?base_revision_id=" + revision1.ID + "&target_revision_id=" + revision2.ID
	if err := harness.call(http.MethodGet, missingPlanPath, nil, nil); err != nil {
		if err := expectFailure(err, http.StatusNotFound, string(domain.CodeNotFound)); err != nil {
			return err
		}
	} else {
		return ensure(false, "missing plan was accepted")
	}
	if err := harness.call(
		http.MethodGet,
		"/v1/plans/"+plan.ID+"/revisions/diff?base_revision_id="+revision1.ID,
		nil, nil,
	); err != nil {
		if err := expectFailure(err, http.StatusBadRequest, string(domain.CodeValidationFailed)); err != nil {
			return err
		}
	} else {
		return ensure(false, "missing target_revision_id was accepted")
	}
	return nil
}

func compareJSON(left any, right any) error {
	leftPayload, err := json.Marshal(left)
	if err != nil {
		return err
	}
	rightPayload, err := json.Marshal(right)
	if err != nil {
		return err
	}
	if !bytes.Equal(leftPayload, rightPayload) {
		return fmt.Errorf("payloads differ:\n%s\n%s", leftPayload, rightPayload)
	}
	return nil
}
