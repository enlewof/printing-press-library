// Copyright 2026 Allen Lew and contributors. Licensed under Apache-2.0. See LICENSE.

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mvanhorn/printing-press-library/library/productivity/concur/internal/client"
)

// TestIsExpensesCreate404DefectError covers the trigger condition for the
// browser fallback: it must fire ONLY on the exact live-confirmed
// signature (HTTP 404 + "No static resource" + a path containing
// "/expenses"), never on an unrelated 404 or a different status code.
func TestIsExpensesCreate404DefectError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "exact live signature matches",
			err: &client.APIError{
				StatusCode: 404,
				Body:       `{"errorMessage":"No static resource /expensereports/v4/users/u1/context/TRAVELER/reports/r1/expenses."}`,
			},
			want: true,
		},
		{
			name: "matches on the PATCH (update) path too -- confirmed live the defect is not create-only",
			err: &client.APIError{
				StatusCode: 404,
				Body:       `{"errorMessage":"No static resource /expensereports/v4/users/u1/context/TRAVELER/reports/r1/expenses/exp1."}`,
			},
			want: true,
		},
		{
			name: "unrelated 404 (bad report id) does not match",
			err: &client.APIError{
				StatusCode: 404,
				Body:       `{"errorMessage":"report not found"}`,
			},
			want: false,
		},
		{
			name: "No static resource on an unrelated path does not match",
			err: &client.APIError{
				StatusCode: 404,
				Body:       `{"errorMessage":"No static resource /some/other/path."}`,
			},
			want: false,
		},
		{
			name: "400 with the same body text does not match (wrong status code)",
			err: &client.APIError{
				StatusCode: 400,
				Body:       `{"errorMessage":"No static resource /expenses."}`,
			},
			want: false,
		},
		{
			name: "nil error does not match",
			err:  nil,
			want: false,
		},
		{
			name: "non-APIError does not match",
			err:  errors.New("boom"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isExpensesCreate404DefectError(tt.err); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestExtractExpenseFallbackParams covers reading the fallback's needed
// fields from an already-constructed body map, for BOTH input paths this
// command supports: the flag-driven object shape this file builds, and an
// arbitrary --stdin caller's JSON (which the fallback must also work for
// now that the !stdinBody exclusion has been removed -- a --stdin caller's
// flag variables are always empty, so extraction has to read the body
// itself, not the flags).
func TestExtractExpenseFallbackParams(t *testing.T) {
	t.Run("flag-driven object shape", func(t *testing.T) {
		body := map[string]any{
			"expenseType":       map[string]any{"code": "01000"},
			"paymentType":       map[string]any{"id": "CASH"},
			"transactionDate":   "2026-09-15",
			"transactionAmount": 50.0,
			"vendor":            map[string]any{"name": "F45 Training"},
			"businessPurpose":   "gym",
		}
		typeCode, paymentType, txDate, amt, vendor, purpose := extractExpenseFallbackParams(body)
		if typeCode != "01000" || paymentType != "CASH" || txDate != "2026-09-15" || amt != 50.0 || vendor != "F45 Training" || purpose != "gym" {
			t.Errorf("got (%q, %q, %q, %v, %q, %q)", typeCode, paymentType, txDate, amt, vendor, purpose)
		}
	})

	t.Run("stdin caller using description instead of name for vendor", func(t *testing.T) {
		body := map[string]any{
			"expenseType":       map[string]any{"code": "CELPH"},
			"paymentType":       map[string]any{"id": "CASH"},
			"transactionDate":   "2026-09-15",
			"transactionAmount": 50.0,
			"vendor":            map[string]any{"description": "on-call cell phone"},
		}
		_, _, _, _, vendor, _ := extractExpenseFallbackParams(body)
		if vendor != "on-call cell phone" {
			t.Errorf("expected vendor extracted from the description sub-field, got %q", vendor)
		}
	})

	t.Run("non-map body returns zero values instead of panicking", func(t *testing.T) {
		typeCode, paymentType, txDate, amt, vendor, purpose := extractExpenseFallbackParams("not a map")
		if typeCode != "" || paymentType != "" || txDate != "" || amt != 0 || vendor != "" || purpose != "" {
			t.Errorf("expected all zero values for a non-map body, got (%q, %q, %q, %v, %q, %q)", typeCode, paymentType, txDate, amt, vendor, purpose)
		}
	})
}

// TestDiffNewExpense covers the before/after expense-list diff used to
// identify what the browser fallback's Save Expense click created, since
// the browser itself never surfaces a usable expense ID.
func TestDiffNewExpense(t *testing.T) {
	before := []json.RawMessage{
		json.RawMessage(`{"expenseId":"exp-1"}`),
	}
	beforeIDs := expenseIDSet(before)

	t.Run("exactly one new expense is identified", func(t *testing.T) {
		after := []json.RawMessage{
			json.RawMessage(`{"expenseId":"exp-1"}`),
			json.RawMessage(`{"expenseId":"exp-2","transactionAmount":50}`),
		}
		got, err := diffNewExpense(before, after, beforeIDs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var parsed struct {
			ExpenseID string `json:"expenseId"`
		}
		if err := json.Unmarshal(got, &parsed); err != nil {
			t.Fatalf("unexpected unmarshal error: %v", err)
		}
		if parsed.ExpenseID != "exp-2" {
			t.Errorf("got expense %q, want exp-2", parsed.ExpenseID)
		}
	})

	t.Run("zero new expenses errors instead of guessing", func(t *testing.T) {
		after := []json.RawMessage{
			json.RawMessage(`{"expenseId":"exp-1"}`),
		}
		if _, err := diffNewExpense(before, after, beforeIDs); err == nil {
			t.Error("expected an error when no new expense appeared, got nil")
		}
	})

	t.Run("multiple new expenses errors instead of guessing which one", func(t *testing.T) {
		after := []json.RawMessage{
			json.RawMessage(`{"expenseId":"exp-1"}`),
			json.RawMessage(`{"expenseId":"exp-2"}`),
			json.RawMessage(`{"expenseId":"exp-3"}`),
		}
		if _, err := diffNewExpense(before, after, beforeIDs); err == nil {
			t.Error("expected an error when multiple new expenses appeared, got nil")
		}
	})
}

// TestExpensesCreatePartialSuccessError mirrors
// TestReportsCreatePartialSuccessError's coverage for the analogous
// duplicate-risk guard on the expenses-create browser fallback.
func TestExpensesCreatePartialSuccessError(t *testing.T) {
	cause := errors.New("boom")
	e := &expensesCreatePartialSuccessError{reportId: "RPT123", cause: cause}

	if !strings.Contains(e.Error(), "RPT123") {
		t.Errorf("expected error message to include the report ID, got: %s", e.Error())
	}
	if !strings.Contains(strings.ToLower(e.Error()), "duplicate") {
		t.Errorf("expected error message to warn about duplicate creation, got: %s", e.Error())
	}
	if !errors.Is(e, cause) {
		t.Error("expected Unwrap() to expose the underlying cause via errors.Is")
	}
}

// TestExpensesCreate_BrowserFallback exercises the full command path with a
// mocked agent-browser binary (a bash script on $PATH, matching the pattern
// established in reports_create_fallback_test.go) and a mocked Concur API
// server, so no real browser or live tenant is touched.
func TestExpensesCreate_BrowserFallback(t *testing.T) {
	tmpDir := t.TempDir()
	mockBinPath := filepath.Join(tmpDir, "agent-browser")
	stateFile := filepath.Join(tmpDir, "mock_state")

	mockScript := `#!/bin/bash
arg1="$1"
arg2="$2"

if [ "$arg1" = "--cdp" ]; then
	echo '{"success": false}'
	exit 0
fi

if [ "$arg1" = "get" ] && [ "$arg2" = "url" ]; then
	echo "https://us2.concursolutions.com/nui/expense/reports/mock-report/expenses/new?expenseTypeId=CELPH"
	exit 0
fi

if [ "$arg1" = "snapshot" ]; then
	echo '{"success":true,"data":{"origin":"https://us2.concursolutions.com","refs":{"e1":{"name":"Amount","role":"textbox"},"e2":{"name":"Vendor Description","role":"textbox"},"e3":{"name":"Save Expense","role":"button"}}}}'
	exit 0
fi

if [ "$arg1" = "click" ] && [ "$arg2" = "@e3" ]; then
	touch "$MOCK_STATE_FILE"
	exit 0
fi

exit 0
`
	if err := os.WriteFile(mockBinPath, []byte(mockScript), 0o755); err != nil {
		t.Fatalf("writing mock binary: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", tmpDir+string(filepath.ListSeparator)+oldPath)
	t.Setenv("MOCK_STATE_FILE", stateFile)

	t.Run("404Defect_TriggersFallback", func(t *testing.T) {
		_ = os.Remove(stateFile)

		apiCalls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiCalls++
			w.Header().Set("Content-Type", "application/json")

			switch {
			case r.Method == "POST" && strings.Contains(r.URL.Path, "/expenses"):
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errorMessage":"No static resource /expensereports/v4/users/test-user-id/context/TRAVELER/reports/mock-report/expenses."}`))
			case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/expenses"):
				if _, err := os.Stat(stateFile); err == nil {
					// After the mocked Save Expense click: one new expense.
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`[{"expenseId":"exp-new-1","transactionAmount":50}]`))
				} else {
					// Before the browser fallback: empty report.
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`[]`))
				}
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()

		t.Setenv("CONCUR_BASE_URL", server.URL)
		t.Setenv("CONCUR_UI_BASE_URL", "https://us2.concursolutions.com")
		t.Setenv("PRINTING_PRESS_VERIFY", "1")
		t.Setenv("PRINTING_PRESS_VERIFY_LIVE_HTTP", "1")

		cmd := RootCmd()
		cmd.SetArgs([]string{
			"expenses", "create",
			"--user-id", "test-user-id",
			"--report-id", "mock-report",
			"--type", "CELPH",
			"--date", "2026-09-15",
			"--amount", "50",
			"--payment-type", "CASH",
			"--vendor", "on-call cell phone",
			"--json",
		})

		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)

		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if _, err := os.Stat(stateFile); os.IsNotExist(err) {
			t.Error("expected browser fallback to be driven (Save Expense clicked), but state file does not exist")
		}

		var envelope map[string]any
		if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
			t.Fatalf("failed to unmarshal output JSON: %v", err)
		}
		if success, ok := envelope["success"].(bool); !ok || !success {
			t.Errorf("expected envelope success: true, got %+v", envelope)
		}
		// --json (not --agent) puts the payload directly under "data", not
		// nested under "results".
		data, ok := envelope["data"].(map[string]any)
		if !ok {
			t.Fatalf("expected data field in envelope, got %+v", envelope)
		}
		if data["expenseId"] != "exp-new-1" {
			t.Errorf("expected the diffed new expense in the response, got %+v", data)
		}
	})

	t.Run("NonMatching404_DoesNotTriggerFallback", func(t *testing.T) {
		_ = os.Remove(stateFile)

		apiCalls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiCalls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errorMessage":"report not found"}`))
		}))
		defer server.Close()

		t.Setenv("CONCUR_BASE_URL", server.URL)
		t.Setenv("CONCUR_UI_BASE_URL", "https://us2.concursolutions.com")
		t.Setenv("PRINTING_PRESS_VERIFY", "1")
		t.Setenv("PRINTING_PRESS_VERIFY_LIVE_HTTP", "1")

		cmd := RootCmd()
		cmd.SetArgs([]string{
			"expenses", "create",
			"--user-id", "test-user-id",
			"--report-id", "mock-report",
			"--type", "CELPH",
			"--date", "2026-09-15",
			"--amount", "50",
			"--json",
		})

		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)

		if err := cmd.Execute(); err == nil {
			t.Fatal("expected an error, got nil")
		}

		if apiCalls != 1 {
			t.Errorf("expected exactly 1 API call (no fallback GET calls), got %d", apiCalls)
		}
		if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
			t.Error("expected browser fallback NOT to be driven, but state file exists")
		}
	})

	// StdinBody_ALSOTriggersFallback covers the fix removing this fallback's
	// former !stdinBody exclusion: a --stdin caller's flag variables are
	// always empty, so the OLD code could never drive the fallback for
	// that input path at all (the confirmed-live 404 defect would just
	// surface as a plain error, no matter which input path hit it).
	// Redirects the real os.Stdin via a pipe since expenses_create.go
	// reads directly from os.Stdin, not through cobra's InOrStdin().
	t.Run("StdinBody_ALSOTriggersFallback", func(t *testing.T) {
		_ = os.Remove(stateFile)

		apiCalls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiCalls++
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.Method == "POST" && strings.Contains(r.URL.Path, "/expenses"):
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errorMessage":"No static resource /expensereports/v4/users/test-user-id/context/TRAVELER/reports/mock-report/expenses."}`))
			case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/expenses"):
				if _, err := os.Stat(stateFile); err == nil {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`[{"expenseId":"exp-new-stdin-1","transactionAmount":50}]`))
				} else {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`[]`))
				}
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()

		t.Setenv("CONCUR_BASE_URL", server.URL)
		t.Setenv("CONCUR_UI_BASE_URL", "https://us2.concursolutions.com")
		t.Setenv("PRINTING_PRESS_VERIFY", "1")
		t.Setenv("PRINTING_PRESS_VERIFY_LIVE_HTTP", "1")

		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("creating pipe: %v", err)
		}
		origStdin := os.Stdin
		os.Stdin = r
		t.Cleanup(func() { os.Stdin = origStdin })

		stdinBody := `{"expenseType":{"code":"01000"},"transactionAmount":50,"transactionDate":"2026-09-15","paymentType":{"id":"CASH"},"vendor":{"name":"F45 Training"},"businessPurpose":"gym"}`
		go func() {
			_, _ = w.Write([]byte(stdinBody))
			_ = w.Close()
		}()

		cmd := RootCmd()
		cmd.SetArgs([]string{
			"expenses", "create",
			"--user-id", "test-user-id",
			"--report-id", "mock-report",
			"--stdin",
			"--json",
		})

		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)

		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if _, err := os.Stat(stateFile); os.IsNotExist(err) {
			t.Error("expected browser fallback to be driven for a --stdin body too, but state file does not exist")
		}

		var envelope map[string]any
		if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
			t.Fatalf("failed to unmarshal output JSON: %v", err)
		}
		data, ok := envelope["data"].(map[string]any)
		if !ok || data["expenseId"] != "exp-new-stdin-1" {
			t.Errorf("expected the diffed new expense in the response for the --stdin path, got %+v", envelope)
		}
	})

	t.Run("SuccessfulHTTP_NeverTriggersFallback", func(t *testing.T) {
		_ = os.Remove(stateFile)

		apiCalls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiCalls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"expenseId":"exp-direct-1"}`))
		}))
		defer server.Close()

		t.Setenv("CONCUR_BASE_URL", server.URL)
		t.Setenv("CONCUR_UI_BASE_URL", "https://us2.concursolutions.com")
		t.Setenv("PRINTING_PRESS_VERIFY", "1")
		t.Setenv("PRINTING_PRESS_VERIFY_LIVE_HTTP", "1")

		cmd := RootCmd()
		cmd.SetArgs([]string{
			"expenses", "create",
			"--user-id", "test-user-id",
			"--report-id", "mock-report",
			"--type", "CELPH",
			"--date", "2026-09-15",
			"--amount", "50",
			"--json",
		})

		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)

		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if apiCalls != 1 {
			t.Errorf("expected exactly 1 API call, got %d", apiCalls)
		}
		if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
			t.Error("expected browser fallback NOT to be driven, but state file exists")
		}
	})
}
