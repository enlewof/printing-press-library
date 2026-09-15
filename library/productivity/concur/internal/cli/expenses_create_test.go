// Copyright 2026 Allen Lew and contributors. Licensed under Apache-2.0. See LICENSE.

package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// TestExpensesCreate_FlagBodyUsesLiveConfirmedShape covers F1: the flag-driven
// body builder used to send expenseTypeCode/transactionCurrencyCode/
// paymentTypeId/vendorDescription, all rejected live with HTTP 400
// "Unrecognized field". The live API's NewReportExpense model instead wants
// expenseType/paymentType/vendor as objects ({"code":...}, {"id":...},
// {"name":...}) and transactionAmount/transactionDate as flat values
// (unchanged). This asserts the corrected shape is actually sent on the wire.
func TestExpensesCreate_FlagBodyUsesLiveConfirmedShape(t *testing.T) {
	var capturedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading request body: %v", err)
		}
		if err := json.Unmarshal(raw, &capturedBody); err != nil {
			t.Fatalf("unmarshaling request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"expenseId": "exp-1"}`))
	}))
	defer server.Close()

	t.Setenv("CONCUR_BASE_URL", server.URL)
	t.Setenv("PRINTING_PRESS_VERIFY", "1")
	t.Setenv("PRINTING_PRESS_VERIFY_LIVE_HTTP", "1")

	cmd := RootCmd()
	cmd.SetArgs([]string{
		"expenses", "create",
		"--user-id", "test-user-id",
		"--report-id", "test-report-id",
		"--type", "CELPH",
		"--date", "2026-09-15",
		"--amount", "50",
		"--payment-type", "CASH",
		"--vendor", "on-call cell phone",
		"--json",
	})

	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(os.Stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedBody == nil {
		t.Fatal("server never received a request body")
	}

	if _, present := capturedBody["expenseTypeCode"]; present {
		t.Errorf("expenseTypeCode must not be sent (rejected live as unrecognized), got body: %+v", capturedBody)
	}
	if _, present := capturedBody["transactionCurrencyCode"]; present {
		t.Errorf("transactionCurrencyCode must not be sent (not a valid top-level property live), got body: %+v", capturedBody)
	}
	if _, present := capturedBody["paymentTypeId"]; present {
		t.Errorf("paymentTypeId must not be sent (rejected live as unrecognized), got body: %+v", capturedBody)
	}
	if _, present := capturedBody["vendorDescription"]; present {
		t.Errorf("vendorDescription must not be sent (rejected live as unrecognized), got body: %+v", capturedBody)
	}

	expenseType, ok := capturedBody["expenseType"].(map[string]any)
	if !ok {
		t.Fatalf("expected expenseType to be an object, got: %+v", capturedBody["expenseType"])
	}
	if expenseType["code"] != "CELPH" {
		t.Errorf("expected expenseType.code=CELPH, got %+v", expenseType)
	}

	paymentType, ok := capturedBody["paymentType"].(map[string]any)
	if !ok {
		t.Fatalf("expected paymentType to be an object, got: %+v", capturedBody["paymentType"])
	}
	if paymentType["id"] != "CASH" {
		t.Errorf("expected paymentType.id=CASH, got %+v", paymentType)
	}

	vendor, ok := capturedBody["vendor"].(map[string]any)
	if !ok {
		t.Fatalf("expected vendor to be an object, got: %+v", capturedBody["vendor"])
	}
	if vendor["name"] != "on-call cell phone" {
		t.Errorf("expected vendor.name=%q, got %+v", "on-call cell phone", vendor)
	}

	if capturedBody["transactionAmount"] != 50.0 {
		t.Errorf("expected transactionAmount=50 (flat number, unchanged), got %+v", capturedBody["transactionAmount"])
	}
	if capturedBody["transactionDate"] != "2026-09-15" {
		t.Errorf("expected transactionDate=2026-09-15 (flat string, unchanged), got %+v", capturedBody["transactionDate"])
	}
}

// TestExpensesCreate_NonUSDCurrencyWarns covers F1's currency gap: no working
// currency-override field was found live, so rather than silently sending a
// confirmed-wrong key (transactionCurrencyCode) or silently dropping the
// user's flag, a non-USD --currency must produce an explicit stderr warning
// and the body must carry no currency-related key at all.
func TestExpensesCreate_NonUSDCurrencyWarns(t *testing.T) {
	var capturedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading request body: %v", err)
		}
		if err := json.Unmarshal(raw, &capturedBody); err != nil {
			t.Fatalf("unmarshaling request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"expenseId": "exp-1"}`))
	}))
	defer server.Close()

	t.Setenv("CONCUR_BASE_URL", server.URL)
	t.Setenv("PRINTING_PRESS_VERIFY", "1")
	t.Setenv("PRINTING_PRESS_VERIFY_LIVE_HTTP", "1")

	cmd := RootCmd()
	cmd.SetArgs([]string{
		"expenses", "create",
		"--user-id", "test-user-id",
		"--report-id", "test-report-id",
		"--type", "CELPH",
		"--date", "2026-09-15",
		"--amount", "50",
		"--currency", "GBP",
		"--payment-type", "CASH",
		"--vendor", "on-call cell phone",
		"--json",
	})

	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !bytes.Contains(errOut.Bytes(), []byte("no working currency-override field")) {
		t.Errorf("expected a stderr warning about the currency gap, got stderr: %s", errOut.String())
	}

	for _, key := range []string{"transactionCurrencyCode", "currency", "currencyCode"} {
		if _, present := capturedBody[key]; present {
			t.Errorf("expected no currency-related key in the body when no working field is known, found %q in: %+v", key, capturedBody)
		}
	}
}
