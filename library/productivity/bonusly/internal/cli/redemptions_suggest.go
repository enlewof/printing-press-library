// Copyright 2026 Allen Lew and contributors. Licensed under Apache-2.0. See LICENSE.
// Novel command scaffold. Implement the RunE body before shipping.
// generate --force preserves implemented bodies; untouched TODO scaffolds may refresh.
// pp:data-source auto

package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/mvanhorn/printing-press-library/library/productivity/bonusly/internal/client"
	"github.com/mvanhorn/printing-press-library/library/productivity/bonusly/internal/store"
	"github.com/spf13/cobra"
)

// redemptionHistoryRow is one raw row read from the local "redemptions"
// mirror table (populated by `sync --resources redemptions`).
type redemptionHistoryRow struct {
	RewardName string
	State      string
	CreatedAt  string
}

// redemptionSuggestion is one ranked entry in `redemptions suggest` output:
// a reward name pulled from the user's own local redemption history,
// ranked by how often and how recently they redeemed it.
//
// Bonusly exposes no live rewards-catalog endpoint with a price (see
// README.md "Known Gaps" #1 -- the awards/incentives catalog was live-probed
// across ~20 path variants and found unreachable, and the Redemption type
// itself has no cost field), so this cannot filter by what the user can
// literally afford. It resurfaces what they've actually redeemed before as
// a memory jog, shown next to their current balance.
type redemptionSuggestion struct {
	RewardName     string `json:"reward_name"`
	TimesRedeemed  int    `json:"times_redeemed"`
	LastRedeemedAt string `json:"last_redeemed_at"`
	LastState      string `json:"last_state"`
}

// rankRedemptionSuggestions aggregates raw local redemption rows by reward
// name and ranks them most-frequently-redeemed first, breaking ties by most
// recent redemption. Pure function (no I/O) so the ranking logic is
// independently unit-testable without a live client or local database.
func rankRedemptionSuggestions(rows []redemptionHistoryRow) []redemptionSuggestion {
	byName := map[string]*redemptionSuggestion{}
	var order []string
	for _, r := range rows {
		name := r.RewardName
		if name == "" {
			name = "(unnamed reward)"
		}
		s, ok := byName[name]
		if !ok {
			s = &redemptionSuggestion{RewardName: name}
			byName[name] = s
			order = append(order, name)
		}
		s.TimesRedeemed++
		if r.CreatedAt >= s.LastRedeemedAt {
			s.LastRedeemedAt = r.CreatedAt
			s.LastState = r.State
		}
	}

	out := make([]redemptionSuggestion, 0, len(order))
	for _, name := range order {
		out = append(out, *byName[name])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TimesRedeemed != out[j].TimesRedeemed {
			return out[i].TimesRedeemed > out[j].TimesRedeemed
		}
		return out[i].LastRedeemedAt > out[j].LastRedeemedAt
	})
	return out
}

// noAffordabilityNote is surfaced on every non-empty response so agent
// callers never mistake "ranked by frequency" for "ranked by what you can
// afford" -- see the redemptionSuggestion doc comment for why no price data
// is available.
const noAffordabilityNote = "Bonusly exposes no live rewards-catalog endpoint with prices, so this can't tell you what you can literally afford -- these are your own past redemptions, ranked by how often and how recently you redeemed them, shown next to your current balance as a memory jog."

const noHistoryNote = "no local redemption history yet; run: bonusly-pp-cli sync --resources redemptions"

func newNovelRedemptionsSuggestCmd(flags *rootFlags) *cobra.Command {
	var flagLimit int

	cmd := &cobra.Command{
		Use:   "suggest",
		Short: "Suggest rewards to redeem again from your own history, next to your current point balance.",
		Long: `Suggest rewards to redeem again from your own history, next to your current point balance.

Bonusly's API exposes no live rewards-catalog endpoint with prices (see README.md "Known Gaps" -- the awards/incentives catalog was live-probed across ~20 path variants and found unreachable), so this cannot tell you what you can literally afford. Instead it shows your current redeemable balance next to the reward names you've redeemed before, ranked by how often and how recently, as a memory jog for your next redemption.`,
		Example:     "  bonusly-pp-cli redemptions suggest --agent",
		Annotations: map[string]string{"mcp:read-only": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if dryRunOK(flags) {
				fmt.Fprintln(cmd.OutOrStdout(), "would suggest redemptions from local history alongside your current point balance")
				return nil
			}

			// check missing mirror -- before any client/network call, same
			// ordering as recognition_gap.go. This command's output is
			// object-shaped (not a bare array like redemptions forecast /
			// recognition search-mine), so the missing-mirror short-circuit
			// prints "{}" for --json/--agent, matching recognition_gap.go's
			// convention for object-shaped novel commands.
			isMissing, dbPath, err := checkMissingMirrorGuard(cmd, flags)
			if err != nil {
				return err
			}
			if isMissing {
				if flags.asJSON || flags.agent {
					fmt.Fprintln(cmd.OutOrStdout(), "{}")
				}
				return nil
			}

			c, err := flags.newClient()
			if err != nil {
				return err
			}

			earningBalance, givingBalance, err := fetchBonuslyPointBalances(cmd, c, flags)
			if err != nil {
				return classifyAPIError(err, flags)
			}

			db, err := store.OpenWithContext(cmd.Context(), dbPath)
			if err != nil {
				return err
			}
			defer db.Close()

			rows, err := db.DB().QueryContext(cmd.Context(), `
				SELECT reward_name, state, created_at
				FROM redemptions
				ORDER BY created_at ASC`)
			if err != nil {
				return err
			}
			defer rows.Close()

			var history []redemptionHistoryRow
			for rows.Next() {
				var name, state, createdAt sql.NullString
				if err := rows.Scan(&name, &state, &createdAt); err != nil {
					return err
				}
				history = append(history, redemptionHistoryRow{
					RewardName: name.String,
					State:      state.String,
					CreatedAt:  createdAt.String,
				})
			}
			if err := rows.Err(); err != nil {
				return err
			}

			suggestions := rankRedemptionSuggestions(history)
			if flagLimit > 0 && len(suggestions) > flagLimit {
				suggestions = suggestions[:flagLimit]
			}

			note := noAffordabilityNote
			if len(history) == 0 {
				note = noHistoryNote
			}

			if flags.asJSON || flags.agent {
				res := map[string]any{
					"earning_balance": earningBalance,
					"giving_balance":  givingBalance,
					"suggestions":     suggestions,
					"note":            note,
				}
				return printJSONFiltered(cmd.OutOrStdout(), res, flags)
			}

			tw := newTabWriter(cmd.OutOrStdout())
			earning := "unknown"
			if earningBalance != nil {
				earning = fmt.Sprintf("%d", *earningBalance)
			}
			fmt.Fprintf(tw, "CURRENT REDEEMABLE BALANCE\t%s\n", earning)
			fmt.Fprintln(tw)
			if len(suggestions) == 0 {
				fmt.Fprintf(tw, "NOTE\t%s\n", note)
			} else {
				fmt.Fprintf(tw, "REWARD\tTIMES REDEEMED\tLAST REDEEMED\tLAST STATE\n")
				for _, s := range suggestions {
					fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", s.RewardName, s.TimesRedeemed, s.LastRedeemedAt, s.LastState)
				}
			}
			_ = tw.Flush()
			if len(suggestions) > 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "\n%s\n", note)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&flagLimit, "limit", 5, "Max number of suggestions to show (0 = no limit)")

	return cmd
}

// fetchBonuslyPointBalances makes the same live call promoted_balance.go's
// `balance` command makes (GET /users/me, hand-patched from the
// spec-derived, 404ing /users/points_balance -- see
// .printing-press-patches/bonusly-endpoint-fixes.json) and extracts the two
// point-balance fields confirmed live for non-admin accounts (README.md
// "Known Gaps" #2): earning_balance (redeemable) and giving_balance.
// Returns nil pointers, not zero values, when a field is absent from the
// response so callers can render "unknown" instead of a misleading 0.
func fetchBonuslyPointBalances(cmd *cobra.Command, c *client.Client, flags *rootFlags) (earningBalance, givingBalance *int64, err error) {
	balanceRaw, _, err := resolveReadWithStrategyAndResponsePath(cmd.Context(), c, flags, "auto", "balance", false, "/users/me", nil, nil, "", cmd.ErrOrStderr())
	if err != nil {
		return nil, nil, err
	}

	var envelope struct {
		Result struct {
			EarningBalance *int64 `json:"earning_balance"`
			GivingBalance  *int64 `json:"giving_balance"`
		} `json:"result"`
	}
	if unmarshalErr := json.Unmarshal(balanceRaw, &envelope); unmarshalErr == nil &&
		(envelope.Result.EarningBalance != nil || envelope.Result.GivingBalance != nil) {
		return envelope.Result.EarningBalance, envelope.Result.GivingBalance, nil
	}

	// Defensive fallback: balance's response shape has drifted from the
	// spec's assumptions before (pp:hand-edit bonusly-endpoint-fix in
	// promoted_balance.go). Tolerate an unwrapped body on the same bytes
	// rather than failing outright if a cache layer or future API change
	// ever returns the object without the {"result": ...} envelope.
	var bare struct {
		EarningBalance *int64 `json:"earning_balance"`
		GivingBalance  *int64 `json:"giving_balance"`
	}
	if unmarshalErr := json.Unmarshal(balanceRaw, &bare); unmarshalErr == nil {
		return bare.EarningBalance, bare.GivingBalance, nil
	}
	return nil, nil, nil
}
