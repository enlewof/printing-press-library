// Copyright 2026 Allen Lew and contributors. Licensed under Apache-2.0. See LICENSE.

package types

import (
	"encoding/json"
	"testing"
)

// TestExpense_UnmarshalJSON_LegacyFlatShape covers the P1 PR-review finding
// "Stored Expenses No Longer Decode": writeThroughCache persists raw API
// bytes verbatim into local SQLite, so any row cached before the F2 struct
// change (flat expenseTypeCode/transactionAmount/transactionCurrencyCode/
// vendorDescription/hasException fields) must still decode without error,
// not crash scan-duplicates on an old cache.
func TestExpense_UnmarshalJSON_LegacyFlatShape(t *testing.T) {
	legacy := []byte(`{
		"expenseId": "exp-legacy-1",
		"expenseTypeCode": "Mobile/Cellular Phone",
		"transactionDate": "2025-06-01",
		"transactionAmount": 65.5,
		"transactionCurrencyCode": "USD",
		"vendorDescription": "on-call cell phone",
		"businessPurpose": "",
		"hasException": true
	}`)

	var e Expense
	if err := json.Unmarshal(legacy, &e); err != nil {
		t.Fatalf("legacy flat-shaped row failed to decode: %v", err)
	}

	if e.ExpenseId != "exp-legacy-1" {
		t.Errorf("ExpenseId = %q, want exp-legacy-1", e.ExpenseId)
	}
	if e.ExpenseType.Name != "Mobile/Cellular Phone" {
		t.Errorf("ExpenseType.Name = %q, want Mobile/Cellular Phone (mapped from legacy expenseTypeCode)", e.ExpenseType.Name)
	}
	if e.TransactionAmount.Value != 65.5 {
		t.Errorf("TransactionAmount.Value = %v, want 65.5 (mapped from legacy bare number)", e.TransactionAmount.Value)
	}
	if e.Vendor.Description != "on-call cell phone" {
		t.Errorf("Vendor.Description = %q, want %q (mapped from legacy vendorDescription)", e.Vendor.Description, "on-call cell phone")
	}
	if !e.HasExceptions {
		t.Error("HasExceptions should be true, mapped from legacy singular hasException")
	}
	if e.HasBlockingExceptions {
		t.Error("HasBlockingExceptions should be false -- unknowable from the single legacy flag, must not be guessed true")
	}
}

// TestExpense_UnmarshalJSON_CurrentNestedShape is a regression guard: the
// live API's actual current shape (confirmed live 2026-09-05) must keep
// decoding correctly now that legacy-shape tolerance has been added
// alongside it.
func TestExpense_UnmarshalJSON_CurrentNestedShape(t *testing.T) {
	current := []byte(`{
		"expenseId": "exp-current-1",
		"expenseType": {"id": "CELPH", "name": "Mobile/Cellular Phone", "code": "OTHER", "isDeleted": false, "listItemId": null},
		"transactionDate": "2026-08-31",
		"transactionAmount": {"value": 50, "currencyCode": "USD"},
		"vendor": {"id": null, "name": null, "description": "T-Mobile"},
		"paymentType": {"id": "CASH", "name": "Cash", "code": "CASH"},
		"businessPurpose": "on-call cell phone",
		"hasExceptions": false,
		"hasBlockingExceptions": false
	}`)

	var e Expense
	if err := json.Unmarshal(current, &e); err != nil {
		t.Fatalf("current nested-shaped row failed to decode: %v", err)
	}

	if e.ExpenseType.Name != "Mobile/Cellular Phone" || e.ExpenseType.Id != "CELPH" {
		t.Errorf("ExpenseType = %+v, want Name=Mobile/Cellular Phone Id=CELPH", e.ExpenseType)
	}
	if e.TransactionAmount.Value != 50 || e.TransactionAmount.CurrencyCode != "USD" {
		t.Errorf("TransactionAmount = %+v, want Value=50 CurrencyCode=USD", e.TransactionAmount)
	}
	if e.Vendor.Description != "T-Mobile" {
		t.Errorf("Vendor.Description = %q, want T-Mobile", e.Vendor.Description)
	}
	if e.PaymentType.Id != "CASH" {
		t.Errorf("PaymentType.Id = %q, want CASH", e.PaymentType.Id)
	}
}

// TestMoney_UnmarshalJSON covers both shapes directly, independent of the
// parent Expense struct.
func TestMoney_UnmarshalJSON(t *testing.T) {
	var m Money
	if err := json.Unmarshal([]byte(`{"value": 12.5, "currencyCode": "USD"}`), &m); err != nil {
		t.Fatalf("object shape: %v", err)
	}
	if m.Value != 12.5 || m.CurrencyCode != "USD" {
		t.Errorf("object shape: got %+v", m)
	}

	var m2 Money
	if err := json.Unmarshal([]byte(`12.5`), &m2); err != nil {
		t.Fatalf("bare-number shape: %v", err)
	}
	if m2.Value != 12.5 || m2.CurrencyCode != "" {
		t.Errorf("bare-number shape: got %+v, want Value=12.5 CurrencyCode=\"\"", m2)
	}

	var m3 Money
	if err := json.Unmarshal([]byte(`"not a number or object"`), &m3); err == nil {
		t.Error("expected an error for an unparseable shape, got nil")
	}
}
