// Copyright 2026 Allen Lew and contributors. Licensed under Apache-2.0. See LICENSE.

package cli

import (
	"errors"
	"strings"
	"testing"
)

// TestClassifyAPIError_ExpensesCreate404Signature covers F2: a fully-valid
// expenses-create request body was confirmed live to return this exact
// Spring "no static resource" 404 shape instead of 201 once every field
// passes client-side validation. The generic "run the list command" hint
// is actively misleading for a blocked POST, so this signature must get
// the more specific, accurate hint instead.
func TestClassifyAPIError_ExpensesCreate404Signature(t *testing.T) {
	err := errors.New(`POST /expensereports/v4/users/u1/context/TRAVELER/reports/r1/expenses returned HTTP 404: {"errorMessage":"No static resource /expensereports/v4/users/u1/context/TRAVELER/reports/r1/expenses."}`)

	got := classifyAPIError(err, &rootFlags{})
	if got == nil {
		t.Fatal("expected a non-nil classified error")
	}

	msg := got.Error()
	if !strings.Contains(msg, "Concur-backend defect") {
		t.Errorf("expected the specific 404 hint, got: %s", msg)
	}
	if strings.Contains(msg, "Run the 'list' command") {
		t.Errorf("expected the specific hint to replace the generic one, but generic text is still present: %s", msg)
	}

	var typed *cliError
	if !errors.As(got, &typed) {
		t.Fatalf("expected a *cliError, got %T", got)
	}
	if typed.code != 3 {
		t.Errorf("expected exit code 3 (not found), got %d", typed.code)
	}
}

// TestClassifyAPIError_Generic404Unaffected is a regression guard: an
// unrelated 404 (e.g. a bad report ID on a GET) must still get the
// original generic hint, not the expenses-create-specific one.
func TestClassifyAPIError_Generic404Unaffected(t *testing.T) {
	err := errors.New(`GET /expensereports/v4/users/u1/context/TRAVELER/reports/does-not-exist returned HTTP 404: {"errorMessage":"report not found"}`)

	got := classifyAPIError(err, &rootFlags{})
	if got == nil {
		t.Fatal("expected a non-nil classified error")
	}

	msg := got.Error()
	if !strings.Contains(msg, "Run the 'list' command") {
		t.Errorf("expected the generic 404 hint to still apply, got: %s", msg)
	}
	if strings.Contains(msg, "Concur-backend defect") {
		t.Errorf("expensescreate-specific hint should not fire on an unrelated 404: %s", msg)
	}
}
