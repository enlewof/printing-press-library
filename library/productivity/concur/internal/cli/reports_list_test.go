// Copyright 2026 Allen Lew and contributors. Licensed under Apache-2.0. See LICENSE.

package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// TestReportsList_SpringHALPagination covers F4:
// asserts reports list can paginate over a Spring HAL style envelope
// with content and links properties across multiple pages.
func TestReportsList_SpringHALPagination(t *testing.T) {
	apiCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls++
		page := r.URL.Query().Get("page")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		if page == "" || page == "1" {
			_, _ = w.Write([]byte(`{
				"links": [
					{
						"rel": "next",
						"href": "https://www-us2.api.concursolutions.com/expensereports/v4/users/test-user/context/TRAVELER/reports?page=2&size=100"
					}
				],
				"content": [
					{
						"reportId": "report-1",
						"name": "Report 1",
						"businessPurpose": "Client site visit",
						"approvalStatus": "Approved",
						"total": 120.00,
						"currencyCode": "USD"
					}
				]
			}`))
			return
		}

		if page == "2" {
			_, _ = w.Write([]byte(`{
				"links": [],
				"content": [
					{
						"reportId": "report-2",
						"name": "Report 2",
						"businessPurpose": "Team building",
						"approvalStatus": "Submitted",
						"total": 350.00,
						"currencyCode": "USD"
					}
				]
			}`))
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	t.Setenv("CONCUR_BASE_URL", server.URL)
	t.Setenv("PRINTING_PRESS_VERIFY", "1")
	t.Setenv("PRINTING_PRESS_VERIFY_LIVE_HTTP", "1")

	cmd := RootCmd()
	cmd.SetArgs([]string{
		"reports", "list",
		"--user-id", "test-user-id",
		"--all",
		"--json",
	})

	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(os.Stderr)

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if apiCalls != 2 {
		t.Errorf("expected 2 API calls (page 1 and page 2), got %d", apiCalls)
	}

	var envelope map[string]any
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatalf("failed to unmarshal output JSON: %v", err)
	}

	results, ok := envelope["results"].([]any)
	if !ok {
		t.Fatalf("expected results field in envelope, got %+v", envelope)
	}

	if len(results) != 2 {
		t.Errorf("expected 2 aggregated reports, got %d", len(results))
	}

	// Verify report names to ensure correct aggregation
	r1, ok1 := results[0].(map[string]any)
	r2, ok2 := results[1].(map[string]any)
	if !ok1 || !ok2 {
		t.Fatalf("unexpected result shape: %+v", results)
	}

	if r1["name"].(string) != "Report 1" || r2["name"].(string) != "Report 2" {
		t.Errorf("expected Report 1 and Report 2, got %q and %q", r1["name"], r2["name"])
	}
}
