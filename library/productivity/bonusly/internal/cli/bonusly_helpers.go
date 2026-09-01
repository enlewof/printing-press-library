package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mvanhorn/printing-press-library/library/productivity/bonusly/internal/client"
	"github.com/spf13/cobra"
)

// parseArrayString parses a string that could be a JSON array of strings or a comma-separated list.
func parseArrayString(s string) []string {
	if s == "" {
		return nil
	}
	var res []string
	if err := json.Unmarshal([]byte(s), &res); err == nil {
		return res
	}
	parts := strings.Split(s, ",")
	var cleaned []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			cleaned = append(cleaned, p)
		}
	}
	return cleaned
}

// deptMember represents a member resolved from live API.
type deptMember struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// deptMemberResult represents a member for audit/values output.
type deptMemberResult struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Given    int64  `json:"given"`
	Received int64  `json:"received"`
}

// fetchDeptMembers fetches user IDs + emails for a department from live API.
func fetchDeptMembers(ctx context.Context, c *client.Client, flags *rootFlags, deptCanonical string, cmd *cobra.Command) ([]deptMember, error) {
	// Base confirmed at /users/departments (was wrong at bare /departments); this
	// exact sub-path segment for a specific department's members remains
	// unconfirmed, but /users/ is now the evidence-backed prefix, not a guess.
	path := replacePathParam("/users/departments/{department}/users", "department", deptCanonical)
	respRaw, _, err := resolveReadWithStrategyAndResponsePath(ctx, c, flags, "live", "departments", false, path, nil, nil, "", cmd.ErrOrStderr())
	if err != nil {
		return nil, classifyAPIError(err, flags)
	}

	var members []deptMember
	if err := json.Unmarshal(respRaw, &members); err != nil {
		return nil, err
	}
	return members, nil
}

// mentionCandidate is a compact match returned by resolveMentionCandidate's
// live lookup, used only to build a helpful "did you mean" suggestion list.
type mentionCandidate struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Username    string `json:"username"`
}

// mentionLookupResult is resolveMentionCandidate's outcome. Skipped is set
// when the live lookup itself never reached the network -- either because
// the enclosing command is running under the global --dry-run flag (the
// shared HTTP client intercepts EVERY request, GET included, under
// --dry-run and returns a synthetic {"dry_run": true} body with no real
// round trip -- see client.Client.dryRun) or because the API call failed
// for an unrelated reason (network/auth/rate-limit). Skipped is
// deliberately not the same as "invalid" -- callers must treat it as
// unverified, not rejected.
type mentionLookupResult struct {
	Matched     bool
	Suggestions []mentionCandidate
	Skipped     bool
	SkipReason  string
}

// resolveMentionCandidate live-checks whether a bare mention token (the text
// between "@" and the next whitespace in a Bonusly reason string, with the
// "@" already stripped by the caller) resolves to a real user. Bonusly's
// mention grammar has no other terminator than whitespace, so a two-word
// display name typed as a mention (e.g. "@Jane Doe") is
// syntactically indistinguishable from a valid single-token mention
// (e.g. "@jane.doe") -- only a live lookup against the real user directory
// can tell them apart without false-positiving on legitimate single-token
// mentions. Uses the same GET /users/autocomplete endpoint users_search.go
// fixed (PR #1899, F1) rather than a fragile capitalization heuristic.
//
// Under normal (non-dry-run) operation this always reaches the live API:
// --dry-run is the only thing that intercepts it (see mentionLookupResult
// doc above) -- there is no local-store fallback for this check, by
// design, since a stale local mirror could wrongly confirm a mention that
// no longer resolves.
//
// Returns Matched=true when any candidate's username, full email, or the
// email's local part (the part before "@") equals token case-insensitively.
func resolveMentionCandidate(ctx context.Context, c *client.Client, flags *rootFlags, token string, hintWriter io.Writer) (mentionLookupResult, error) {
	params := map[string]string{"search": token, "limit": "5"}
	data, prov, err := resolveReadWithStrategyAndResponsePath(ctx, c, flags, "live", "users", false, "/users/autocomplete", params, nil, "", hintWriter)
	if err != nil {
		return mentionLookupResult{Skipped: true, SkipReason: err.Error()}, nil
	}
	if prov.Source == "dry-run" {
		return mentionLookupResult{Skipped: true, SkipReason: "running under --dry-run"}, nil
	}
	// collectionItemsForOutput does not unwrap this envelope shape for this
	// path (it's tuned for cursor-paginated list envelopes, not Bonusly's
	// flat {"success":true,"result":[...]}); org_top.go hit the same thing
	// and hand-extracts "result" directly -- matching that proven pattern
	// here instead of assuming collectionItemsForOutput handles it.
	var envelope struct {
		Result []mentionCandidate `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return mentionLookupResult{Skipped: true, SkipReason: fmt.Sprintf("parsing autocomplete results: %v", err)}, nil
	}
	results := envelope.Result
	lowerToken := strings.ToLower(token)
	for _, cand := range results {
		localPart, _, _ := strings.Cut(cand.Email, "@")
		if strings.ToLower(cand.Username) == lowerToken ||
			strings.ToLower(cand.Email) == lowerToken ||
			strings.ToLower(localPart) == lowerToken {
			return mentionLookupResult{Matched: true}, nil
		}
	}
	if len(results) > 3 {
		results = results[:3]
	}
	return mentionLookupResult{Matched: false, Suggestions: results}, nil
}

// checkMissingMirrorGuard returns true and prints missing mirror message if db doesn't exist.
func checkMissingMirrorGuard(cmd *cobra.Command, flags *rootFlags) (bool, string, error) {
	dbPath := defaultDBPath("bonusly-pp-cli")
	if _, statErr := os.Stat(dbPath); os.IsNotExist(statErr) {
		fmt.Fprintf(cmd.ErrOrStderr(), "no local mirror at %s\nrun: bonusly-pp-cli sync --resources <resource> --db %s\n", dbPath, dbPath)
		return true, "", nil
	}
	return false, dbPath, nil
}
