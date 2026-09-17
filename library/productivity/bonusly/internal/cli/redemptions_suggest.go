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
	"strings"

	"github.com/mvanhorn/printing-press-library/library/productivity/bonusly/internal/client"
	"github.com/mvanhorn/printing-press-library/library/productivity/bonusly/internal/cliutil"
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

// nonSuccessfulRedemptionStates is a conservative denylist of state values
// that unambiguously mean a redemption did not complete. Bonusly's real
// state enum is unconfirmed anywhere in this CLI's spec/docs (bonusly-spec.yaml
// discloses every field as inferred-or-unconfirmed; the Redemption type
// carries no documented enum), so this intentionally excludes only
// clear-cut negative-outcome words rather than guessing a full allowlist --
// an unrecognized or empty state is treated as evidence of a real
// redemption rather than silently dropped.
var nonSuccessfulRedemptionStates = []string{
	"pending", "denied", "rejected", "failed", "cancelled", "canceled", "expired",
}

// looksLikeCompletedRedemption reports whether state does not match any
// entry in nonSuccessfulRedemptionStates. Used to keep a handful of pending
// or failed claim attempts from outranking rewards the user has actually
// received (see PR review discussion: a reward retried several times while
// pending would otherwise inflate times_redeemed above rewards that
// completed on the first try).
func looksLikeCompletedRedemption(state string) bool {
	lower := strings.ToLower(strings.TrimSpace(state))
	for _, bad := range nonSuccessfulRedemptionStates {
		if strings.Contains(lower, bad) {
			return false
		}
	}
	return true
}

// rankRedemptionSuggestions aggregates raw local redemption rows by reward
// name and ranks them most-frequently-redeemed first, breaking ties by most
// recent redemption. Rows whose state clearly indicates the redemption
// never completed (see looksLikeCompletedRedemption) are excluded entirely
// -- a reward with only pending/denied/failed attempts has no evidence of
// a successful past redemption, so it should not be suggested "again".
// Pure function (no I/O) so the ranking logic is independently unit-testable
// without a live client or local database.
func rankRedemptionSuggestions(rows []redemptionHistoryRow) []redemptionSuggestion {
	byName := map[string]*redemptionSuggestion{}
	var order []string
	for _, r := range rows {
		if !looksLikeCompletedRedemption(r.State) {
			continue
		}
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
			if flagLimit < 0 {
				_ = cmd.Usage()
				return usageErr(fmt.Errorf("--limit must be zero or greater (0 means no limit); got %d", flagLimit))
			}
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

			// Balance-fetch failure is deliberately non-fatal: this
			// command's core value (ranked local history) does not depend
			// on the live balance call succeeding, and --data-source local
			// has no working local mirror for the "balance" resourceType
			// today (it is not in knownSyncResourceNames()'s syncable list,
			// so resolveLocal's fallback always 404s -- the same gap
			// promoted_balance.go's `balance` command inherits from its own
			// identical strategy="auto" call). Surfacing balanceUnavailable
			// as an explicit, distinct reason -- rather than hard-failing
			// the whole command or silently rendering "unknown" with no
			// explanation -- lets a caller tell "balance couldn't be
			// resolved" apart from "you have zero points".
			earningBalance, givingBalance, balanceUnavailable := fetchBonuslyPointBalances(cmd, c, flags)

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

			var note string
			switch {
			case len(history) == 0:
				note = noHistoryNote
			case len(suggestions) == 0:
				note = "no completed redemptions found in your local history (entries present are pending/denied/failed/expired) -- see: bonusly-pp-cli redemptions list-mine"
			default:
				note = noAffordabilityNote
			}

			if flags.asJSON || flags.agent {
				res := map[string]any{
					"earning_balance":     earningBalance,
					"giving_balance":      givingBalance,
					"balance_unavailable": balanceUnavailable,
					"suggestions":         suggestions,
					"note":                note,
				}
				return printJSONFiltered(cmd.OutOrStdout(), res, flags)
			}

			tw := newTabWriter(cmd.OutOrStdout())
			earning := "unknown"
			if earningBalance != nil {
				earning = fmt.Sprintf("%d", *earningBalance)
			}
			fmt.Fprintf(tw, "CURRENT REDEEMABLE BALANCE\t%s\n", earning)
			if balanceUnavailable != "" {
				fmt.Fprintf(tw, "BALANCE NOTE\t%s\n", cliutil.ScrubTerminal(balanceUnavailable))
			}
			fmt.Fprintln(tw)
			if len(suggestions) == 0 {
				fmt.Fprintf(tw, "NOTE\t%s\n", note)
			} else {
				fmt.Fprintf(tw, "REWARD\tTIMES REDEEMED\tLAST REDEEMED\tLAST STATE\n")
				for _, s := range suggestions {
					// Reward name and state are remote-controlled strings
					// (Bonusly-side reward catalog / redemption state);
					// scrub before writing to a terminal so an org-controlled
					// value containing tabs/newlines/ANSI-OSC bytes cannot
					// inject table rows or trigger escape-sequence behavior.
					fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", cliutil.ScrubTerminal(s.RewardName), s.TimesRedeemed, s.LastRedeemedAt, cliutil.ScrubTerminal(s.LastState))
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
//
// Deliberately never returns a hard error: this command's core value (your
// ranked local redemption history) does not depend on the balance call
// succeeding, and --data-source local has no reachable local mirror for the
// "balance" resourceType today ("balance" is absent from
// knownSyncResourceNames()'s syncable list, so resolveLocal's ID-based
// lookup can never be populated by `sync` -- the same gap
// promoted_balance.go's `balance` command inherits from its own identical
// strategy="auto" call; not introduced here). Any failure -- network,
// local-resolution dead-end, or an unexpected response shape -- is reported
// through the returned reason string instead of aborting the whole command,
// and the two are kept distinguishable: a genuinely absent field (fields
// parsed, both nil) returns reason "" the same as success, while a call or
// decode failure always carries a non-empty, specific reason so callers
// never render "unknown" without an explanation.
func fetchBonuslyPointBalances(cmd *cobra.Command, c *client.Client, flags *rootFlags) (earningBalance, givingBalance *int64, unavailableReason string) {
	balanceRaw, _, err := resolveReadWithStrategyAndResponsePath(cmd.Context(), c, flags, "auto", "balance", false, "/users/me", nil, nil, "", cmd.ErrOrStderr())
	if err != nil {
		return nil, nil, fmt.Sprintf("could not fetch your current balance: %v", err)
	}

	var envelope struct {
		Result struct {
			EarningBalance *int64 `json:"earning_balance"`
			GivingBalance  *int64 `json:"giving_balance"`
		} `json:"result"`
	}
	if unmarshalErr := json.Unmarshal(balanceRaw, &envelope); unmarshalErr == nil &&
		(envelope.Result.EarningBalance != nil || envelope.Result.GivingBalance != nil) {
		return envelope.Result.EarningBalance, envelope.Result.GivingBalance, ""
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
	if unmarshalErr := json.Unmarshal(balanceRaw, &bare); unmarshalErr == nil &&
		(bare.EarningBalance != nil || bare.GivingBalance != nil) {
		return bare.EarningBalance, bare.GivingBalance, ""
	}
	return nil, nil, "balance response did not contain earning_balance or giving_balance in the shape this CLI recognizes"
}
