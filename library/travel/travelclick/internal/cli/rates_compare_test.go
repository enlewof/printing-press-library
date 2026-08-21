// Copyright 2026 Allen Lew and contributors. Licensed under Apache-2.0. See LICENSE.
// cli-printing-press: novel-scaffold-test

package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mvanhorn/printing-press-library/library/travel/travelclick/internal/types"
)

func TestNovelRatesCompareHelpWires(t *testing.T) {
	cmd := RootCmd()
	cmd.SetArgs([]string{"rates", "compare", "--help"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("rates compare --help error = %v (novel command not wired correctly?)", err)
	}
	help := out.String()
	for _, want := range []string{"Usage:", "compare"} {
		if !strings.Contains(help, want) {
			t.Fatalf("rates compare --help missing %q in output:\n%s", want, help)
		}
	}
}

func TestComputeLowestHotelRate(t *testing.T) {
	stays := []types.AvailRoomStay{
		{
			RoomTypes: []types.RoomType{
				{
					RoomTypeName: "Deluxe King",
					AverageRates: []types.RatePlanRate{
						{
							RatePlanCode: "BAR",
							Rate:         200.0,
						},
						{
							RatePlanCode: "MEM",
							Rate:         180.0,
						},
					},
					NightlyRates: []types.NightlyRate{
						{
							RatePlanCode:                "BAR",
							AmtTotal:                    200.0,
							TotalServiceChargeExclusive: 20.0,
						},
						{
							RatePlanCode:                "MEM",
							AmtTotal:                    180.0,
							TotalServiceChargeExclusive: 15.0,
						},
					},
				},
			},
		},
	}

	best, hasRate := computeLowestHotelRate(stays, "102306", "made-nyc", 2)
	if !hasRate {
		t.Fatalf("expected to find a rate")
	}

	// 180 + 15 = 195.0 total cost is cheaper than 200 + 20 = 220.0
	if best.LowestTotal != 195.0 {
		t.Errorf("expected lowest total to be 195.0, got %f", best.LowestTotal)
	}
	if best.RoomTypeName != "Deluxe King" {
		t.Errorf("expected room type Deluxe King, got %s", best.RoomTypeName)
	}
	if best.RatePlanCode != "MEM" {
		t.Errorf("expected rate plan MEM, got %s", best.RatePlanCode)
	}
}

func TestComputeLowestHotelRateFallback(t *testing.T) {
	// Test fallback when NightlyRates are missing
	stays := []types.AvailRoomStay{
		{
			RoomTypes: []types.RoomType{
				{
					RoomTypeName: "Suite",
					AverageRates: []types.RatePlanRate{
						{
							RatePlanCode: "BAR",
							Rate:         300.0,
						},
					},
				},
			},
		},
	}

	best, hasRate := computeLowestHotelRate(stays, "102306", "made-nyc", 3)
	if !hasRate {
		t.Fatalf("expected to find a rate")
	}

	// 300.0 * 3 nights = 900.0
	if best.LowestTotal != 900.0 {
		t.Errorf("expected lowest total to be 900.0, got %f", best.LowestTotal)
	}
	if best.RatePlanCode != "BAR" {
		t.Errorf("expected rate plan BAR, got %s", best.RatePlanCode)
	}
}
