// Copyright 2026 Allen Lew and contributors. Licensed under Apache-2.0. See LICENSE.
// cli-printing-press: novel-scaffold-test
// Novel command scaffold tests. Keep the wiring smoke test and add behavior cases as needed.

package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestNovelRedemptionsSuggestHelpWires smoke-tests that the redemptions
// suggest command resolves at runtime and renders useful --help output.
// Catches wiring regressions (missing AddCommand, panicking RunE on --help,
// etc.) before review. Keep this smoke test when adding behavior-specific
// cases.
func TestNovelRedemptionsSuggestHelpWires(t *testing.T) {
	cmd := RootCmd()
	cmd.SetArgs([]string{"redemptions", "suggest", "--help"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("redemptions suggest --help error = %v (novel command not wired correctly?)", err)
	}
	help := out.String()
	for _, want := range []string{"Usage:", "suggest"} {
		if !strings.Contains(help, want) {
			t.Fatalf("redemptions suggest --help missing %q in output:\n%s", want, help)
		}
	}
}

// TestRankRedemptionSuggestions covers the pure aggregation/ranking logic
// with no I/O: frequency ordering, recency tie-breaking, and the
// empty-reward-name fallback label.
func TestRankRedemptionSuggestions(t *testing.T) {
	tests := []struct {
		name string
		rows []redemptionHistoryRow
		want []redemptionSuggestion
	}{
		{
			name: "empty input yields no suggestions",
			rows: nil,
			want: []redemptionSuggestion{},
		},
		{
			name: "ranks by times redeemed descending",
			rows: []redemptionHistoryRow{
				{RewardName: "Coffee Gift Card", State: "fulfilled", CreatedAt: "2026-01-01T00:00:00Z"},
				{RewardName: "Book Credit", State: "fulfilled", CreatedAt: "2026-02-01T00:00:00Z"},
				{RewardName: "Coffee Gift Card", State: "fulfilled", CreatedAt: "2026-03-01T00:00:00Z"},
				{RewardName: "Coffee Gift Card", State: "fulfilled", CreatedAt: "2026-04-01T00:00:00Z"},
			},
			want: []redemptionSuggestion{
				{RewardName: "Coffee Gift Card", TimesRedeemed: 3, LastRedeemedAt: "2026-04-01T00:00:00Z", LastState: "fulfilled"},
				{RewardName: "Book Credit", TimesRedeemed: 1, LastRedeemedAt: "2026-02-01T00:00:00Z", LastState: "fulfilled"},
			},
		},
		{
			name: "ties on times redeemed break by most recent",
			rows: []redemptionHistoryRow{
				{RewardName: "A", State: "fulfilled", CreatedAt: "2026-01-01T00:00:00Z"},
				{RewardName: "B", State: "fulfilled", CreatedAt: "2026-06-01T00:00:00Z"},
			},
			want: []redemptionSuggestion{
				{RewardName: "B", TimesRedeemed: 1, LastRedeemedAt: "2026-06-01T00:00:00Z", LastState: "fulfilled"},
				{RewardName: "A", TimesRedeemed: 1, LastRedeemedAt: "2026-01-01T00:00:00Z", LastState: "fulfilled"},
			},
		},
		{
			name: "blank reward name falls back to a labeled placeholder",
			rows: []redemptionHistoryRow{
				{RewardName: "", State: "pending", CreatedAt: "2026-05-01T00:00:00Z"},
			},
			want: []redemptionSuggestion{
				{RewardName: "(unnamed reward)", TimesRedeemed: 1, LastRedeemedAt: "2026-05-01T00:00:00Z", LastState: "pending"},
			},
		},
		{
			name: "tracks the latest state, not just the first seen",
			rows: []redemptionHistoryRow{
				{RewardName: "Gift Card", State: "pending", CreatedAt: "2026-01-01T00:00:00Z"},
				{RewardName: "Gift Card", State: "fulfilled", CreatedAt: "2026-02-01T00:00:00Z"},
			},
			want: []redemptionSuggestion{
				{RewardName: "Gift Card", TimesRedeemed: 2, LastRedeemedAt: "2026-02-01T00:00:00Z", LastState: "fulfilled"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rankRedemptionSuggestions(tt.rows)
			if len(got) != len(tt.want) {
				t.Fatalf("rankRedemptionSuggestions() returned %d entries, want %d\ngot:  %+v\nwant: %+v", len(got), len(tt.want), got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("entry %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}
