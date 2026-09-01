---
date: 2026-09-01
target_cli: bonusly-pp-cli
amend_run_id: amend-2026-09-01T1718
scope_tier: all (bugs + direct-input architecture change)
findings_count: 7
mode: both
---

# Amend plan: bonusly-pp-cli

## Provenance note

Dogfood findings (F1, F4-F6) are sourced from this agent's first-hand
participation in the session, not a parsed Claude-Code-format transcript.
The runtime for this session is OpenCode, which stores session state in a
local SQLite DB (`~/.local/share/opencode/opencode.db` +
`storage/session_diff/*.json`), not the `~/.claude/projects/<slug>/*.jsonl`
format this skill's transcript-parsing procedure expects. There was no
candidate file to mis-select between, so the file-confirmation checkpoint
was replaced with a direct statement of the substitution to the user.

All root causes below were independently verified against Bonusly's live,
official API reference at docs.bonus.ly (fetched during this run — the spec
in this repo pre-dates that reference and marks the broken paths as
`# inferred`) and against the running CLI (live reproduction during the
dogfood session that motivated this amend). Names and email addresses in the
evidence below are placeholders substituted after the fact — see this
run's PII scrub report for the original session's real (now-redacted)
values.

## Findings and target files

### F1 — `users search` hits a nonexistent endpoint (bug)

- **Evidence**: `bonusly-pp-cli users search --query "Jordan"` (a known-valid
  name on the authenticated account) returns `404 {"success":false,"message":
  "User not found"}`. Reproduced against multiple inputs including the
  authenticated user's own name — not an existence signal, the route itself
  is wrong.
- **Root cause**: `internal/cli/users_search.go` calls `GET /users/search
  ?query=`. This path does not exist in Bonusly's API. The real endpoint is
  `GET /users/autocomplete` with a **required** `search` param (not
  `query`), confirmed at https://docs.bonus.ly/reference/autocomplete_users
  — `operationId: autocomplete_users`, `path: /v1/users/autocomplete`,
  params: `search` (required), `limit` (default 20), `include_non_receivable`
  (bool, default false). Response shape is a compact array (`id,
  display_name, username, email, user_mode, can_receive, full_pic_url,
  profile_pic_url`) — NOT the full UserDecorator shape `users get` returns.
- **Target file**: `internal/cli/users_search.go`
- **Fix**: change `path` to `/users/autocomplete`; rename the `--query` flag's
  wire param from `query` to `search` (keep the flag name `--query` for
  backward CLI compatibility, or rename to `--search` with `--query` as a
  hidden alias — implementer's call, but the wire param sent to the API MUST
  be `search`); add `--include-non-receivable` bool flag wired to
  `include_non_receivable`; keep `--limit`. Drop `--department`/`--location`
  client-side filters if the real endpoint has no such params (it doesn't) —
  either remove them or keep as client-side post-filters on the returned
  compact array, clearly documented as client-side.
- **Test scenario**: `users search --query "Jane"` (or `--search`) against
  the live API returns a non-empty array containing at least one user whose
  `display_name` contains "Jane", using a real token.

### F2 — `users get` hits a nonexistent endpoint (bug)

- **Evidence**: `bonusly-pp-cli users get jordan@example.com` (the
  authenticated user's own, definitely-valid email) returns `404
  {"success":false,"message":"User not found"}`. Reproduced against a second
  known-valid email too.
- **Root cause**: `internal/cli/users_get.go` calls `GET /users/show
  ?identifier=`. This path/param does not exist. The real endpoint is `GET
  /users/{id}` — the identifier is a **path** segment, and it is a Bonusly
  internal BSON ObjectId, NOT an email or display name, confirmed at
  https://docs.bonus.ly/reference/get_user — `operationId: get_user`, `path:
  /v1/users/{id}`, single required path param `id` ("BSON ObjectId of the
  user"). 404 response example is literally `{"success":false,"message":
  "User not found"}` — matches what we saw for every input, because
  `/users/show` never matches any real user id.
- **Target file**: `internal/cli/users_get.go`
- **Fix**: this command's `--help` and spec.yaml promise resolution "by id,
  email, or display name" — that promise can still be kept, but only by
  chaining through the now-fixed autocomplete endpoint:
  1. If the input looks like a bare Bonusly ObjectId (24 lowercase
     hex chars — same shape as the `id` fields already seen in this
     session's `users me` output, e.g. `000000000000000000000001`), call
     `GET /users/{id}` directly.
  2. Otherwise (email or display name), call `GET /users/autocomplete
     ?search=<input>&limit=5` first. If exactly one result comes back,
     follow up with `GET /users/{id}` using that result's `id` and return
     the full record. If zero results, surface a clear "no user matched
     <input>" error (not a raw 404 passthrough). If more than one result,
     return the candidate list with a clear "ambiguous — multiple matches"
     message so the caller can pick, rather than silently guessing.
- **Test scenario**: `users get jordan@example.com` (email path) and
  `users get 000000000000000000000001` (bare-id path, using the id printed
  by `users me` in this same test run) both return the full user record.

### F3 — `org top --search` is a silent no-op; `--cursor` pagination is dead (bug)

- **Evidence**: `org top --search "Jane"`, `org top --search "Doe"`,
  and `org top` with no search flag at all returned byte-identical 100-row
  result sets. `--cursor` was never populated in any response (no
  `next_cursor`/`cursor` field anywhere in the envelope), so pagination
  beyond the first page is unreachable today regardless of how many
  top-level users exist.
- **Root cause**: `internal/cli/org_top.go` already carries a prior hand-patch
  (`pp:hand-edit bonusly-endpoint-fix`) redirecting the base call from the
  nonexistent `/users/top_level` to `GET /users?top_level=true`, which is
  correct and should be KEPT. But it then forwards `--search` as a raw
  `search=` query param on that same `/users` call. Per
  https://docs.bonus.ly/reference/list_users, `GET /v1/users`'s real,
  documented parameters are `limit` (max 100), `skip`, `email` (substring
  match), `sort`, `include_archived`, `show_financial_data`, `user_mode` —
  there is no `search`/free-text param on this endpoint at all, so the
  server silently ignores it (Rails strong-params drops unrecognized keys
  rather than erroring). Separately, this endpoint paginates via
  `limit`/`skip`, not a cursor — `--cursor` was never going to do anything
  either.
- **Target file**: `internal/cli/org_top.go`
- **Fix**:
  1. Replace `--cursor` with real `--limit`/`--skip` flags wired to the
     real params (default `limit=100` to match current behavior).
  2. For `--search`: since the API has no server-side free-text filter on
     this endpoint, do the filtering client-side after fetching — keep the
     `top_level=true` fetch as-is, then (when `--search` is non-empty) filter
     the decoded result array in Go for a case-insensitive substring match
     against `display_name`, `email`, `username`, `first_name`, `last_name`
     before printing/returning. Add a one-line comment explaining why (no
     server-side search param exists on this endpoint) so a future
     regenerate doesn't "fix" it back to a passthrough param.
- **Test scenario**: `org top --search "rivera"` returns only the matching
  top-level user, not the full 100-row set; `org top --limit 5` returns
  exactly 5 rows.

### F4 — `give`/`recognition create` silently accept invalid mentions (bug)

- **Evidence**: `give --to "Jane Doe" --amount 25 --hashtag
  empowerothers --message "..."` — `--dry-run` happily echoed back
  `+25 @Jane Doe ...` as if valid. The real (non-dry-run) call
  failed with `400 {"success":false,"message":"Reason is incomplete. You
  need a recipient."}`. Per
  https://docs.bonus.ly/reference/create_bonus, the `reason` field's mention
  syntax is `@alice @bob` — space-free tokens (username or email), matching
  what actually worked once we had the right identifier
  (`@jane.doe@example.com` succeeded).
- **Target files**: `internal/cli/give.go`, `internal/cli/recognition_create.go`
- **Fix**: validate each `--to` value (and each `@mention` inside
  `recognition create --reason` where feasible) client-side before building
  the request: reject any token containing whitespace with a clear, actionable
  error (e.g. "recipient \"Jane Doe\" contains a space — use an
  email or username instead, e.g. jane.doe@example.com or
  jane.doe"). Apply the same validation inside `--dry-run` so the
  preview catches this before a real spend attempt, not after. Do not attempt
  automatic name-to-email resolution here (that's F1/F2's job if the caller
  wants it) — the API's own error, once triggered by a real malformed mention,
  is not obviously actionable; the fix is to make the failure obvious and
  immediate instead of it being a fluke that dry-run doesn't catch.
- **Test scenario**: `give --to "Jane Doe" --amount 15 --hashtag teamwork
  --message x --dry-run` now fails fast with the new client-side validation
  error instead of printing a false-positive success preview.

### F5 — `give_amounts` reads like a hard constraint but isn't (bug/doc)

- **Evidence**: this session gave 25 points successfully even though the
  authenticated user's own `give_amounts` field was `[7, 15, 22, 30, 37]` —
  25 is not in that list. The OpenAPI schema for `give_amounts` on the User
  object is just `{"type": "array", "items": {"type": "number"}}` with no
  enum/validation tie to the `reason` amount parser on `POST /bonuses` — it's
  UI-suggestion data, not a server-enforced allow-list.
- **Target files**: wherever `give_amounts`/`suggested_give_amounts` are
  surfaced in `--help` text or docs (check `internal/cli/give.go`,
  `internal/cli/users_me.go`/`users_get.go` doc comments, README, SKILL.md)
- **Fix**: any help text or documentation that implies these are the only
  valid `--amount` values must be corrected to say they are UI suggestions;
  the API accepts any numeric amount. If `give.go` ever validates `--amount`
  against a fetched `give_amounts` list client-side (check before editing),
  remove that client-side rejection — do not block an amount the server would
  accept.
- **Test scenario**: `give --to <resolvable-recipient> --amount 25 ...`
  (an amount outside the example `give_amounts` list) succeeds without a
  client-side rejection.

### F6 — browser-cookie auth passes reads, 403s on every write (bug)

- **Evidence**: `auth login --chrome` succeeded and subsequent read commands
  (`users me`, `company`, `org top`) worked. `give`/`recognition create`
  (POST /bonuses) failed twice, both times immediately after a fresh
  `auth login --chrome` re-import, with `403 {"success":false,"message":
  "CSRF token is missing or invalid"}`. Switching to `auth set-token
  <PAT>` fixed the write path immediately (same command, same payload,
  only the credential type changed) — next failure was a 400 business-logic
  error (bad mention), not a 403, proving the credential itself was the
  blocker.
- **Root cause**: see F7 below — this is the symptom, F7 is the structural
  fix.

### F7 — remove browser-cookie auth; PAT is the only real auth mechanism (direct-input ask, architecture)

- **User's ask (verbatim)**: "since api is public, amend the cli to avoid
  using the browser"
- **Independent confirmation**: https://help.bonus.ly/en/articles/15264520-bonusly-api-access-for-admins
  — "Bonusly has replaced the legacy API key system with Personal Access
  Tokens (PATs). PATs are the single way to authenticate non-agentic
  integrations going forward... direct calls to the Bonusly REST API all
  run on the same token type." The OpenAPI `securitySchemes` for every
  endpoint in the real reference are exactly two: `ApiKeyAuth` (`access_token`
  query param, read-only tokens only) and `BearerAuth` (`Authorization:
  Bearer <token>` header, required for writes). There is no session-cookie
  security scheme anywhere in Bonusly's documented API. The CLI's own
  manifest already declares `"auth_type": "bearer_token"` — the shipped
  code just never enforced that.
- **Target files**:
  - `internal/cli/auth.go` — `newAuthLoginCmd` and its Chrome-profile-discovery
    / pycookiecheat-shelling / cookies-file-import helper functions
    (everything from the `chromeChannelStable` const block through
    `parseCookieHeaderCookies`, roughly lines 216-1015 in the pre-amend file)
  - `internal/config/config.go` — `CookieVal` field, `CookieCredential()`,
    `SaveCookie()`, and every `// pp:hand-edit bonusly-cookiejar` marked line
  - `internal/cliutil/credentials.go` — the `CookieVal` field on the
    persisted-credentials struct (marked `// pp:hand-edit bonusly-cookiejar`)
  - `internal/client/bonusly_cookiejar.go` — delete entirely (its only job is
    seeding a jar from `CookieVal`)
  - `internal/client/client.go` — remove the `seedCookieJar()` call site and
    its `bonusly-cookiejar` hand-edit comment; **do not** remove
    `newHTTPClient`'s generic `http.CookieJar` parameter/usage itself if it's
    also used for ordinary Set-Cookie handling during normal request flow —
    check before deleting, only remove the custom pre-seeding wiring
  - `newAuthStatusCmd` (in auth.go) — simplify `authed := header != "" ||
    cfg.CookieCredential() != ""` back to `authed := header != ""`
- **Fix**:
  1. `newAuthLoginCmd` becomes a short, non-interactive redirect: printing
     that Bonusly's API is PAT-only, with the exact steps from the official
     doc (mint at Settings → Services for a personal token, or Company →
     Integrations → API & Tokens for an admin/company token), and the
     `auth set-token <token>` command to save it. Keep the `login` subcommand
     name (so existing muscle-memory/scripts get a helpful redirect instead
     of "unknown command"), but it must not shell out to Chrome, sqlite3, or
     pycookiecheat under any flag combination.
  2. Delete the now-fully-dead Chrome/cookie helper functions rather than
     leaving them unreachable — this is a real removal, not a shutoff switch.
  3. Remove `CookieVal` and its accessors from config + credentials structs;
     update the TOML schema/tests accordingly (check
     `internal/config/config_test.go` and any credentials round-trip tests
     for now-invalid references).
  4. Update `SKILL.md` and `README.md`'s "Auth Setup" section to describe
     ONLY the PAT flow (mirroring the real onboarding steps from
     help.bonus.ly), removing every "Chrome"/"browser session" reference in
     prose.
  5. `doctor` command: check its auth-status reporting block for any
     cookie-specific messaging tied to what's being removed (it referenced
     `auth_source`/`credentials_location`, not `CookieVal` directly per the
     earlier grep — verify no dangling reference after the config changes).
- **Test scenario**: `go build ./...` succeeds with `CookieVal` and its
  call sites fully gone (not just unreachable); `auth login --chrome`
  prints the PAT redirect message and exits without touching Chrome/sqlite3;
  `auth status` still correctly reports "not authenticated" vs "credentials
  present" using only the bearer-token/header path.

## Risks and dependencies

- F1 and F2 are independent fixes but both depend on the same real
  `/users/autocomplete` endpoint being wired correctly — implement F1 first,
  then F2 can reuse the same request-building path internally rather than
  duplicating it.
- F6 and F7 are the same root cause at two different layers (symptom vs.
  structural fix) — implement together; do not fix F6 by patching the CSRF
  symptom while leaving the browser-cookie path in place, that would
  contradict F7.
- F3's client-side substring filter must run on the decoded array *after*
  the existing `top_level=true` fetch, not replace that fetch — do not
  regress the already-working (prior-hand-patched) base listing behavior.
- All fixes touch only this printed CLI's copy under the managed clone —
  no changes to the Printing Press generator/templates. If any fix reveals
  a pattern that would recur across every printed CLI (e.g. a generic
  "guessed inferred path" problem), that is a `/printing-press-retro`
  candidate, not something to fix here.

## Validation plan

1. `go build ./...` and `go vet ./...` inside the CLI module.
2. `<PRINTING_PRESS_BIN> publish validate --dir <CLI_DIR> --json` — manifest,
   phase5, govulncheck (scoped), go vet, go build, `--help`, `--version`.
3. Live smoke test against the real Bonusly API (token already configured in
   this environment) for each finding's test scenario above.
4. One `.printing-press-patches/<id>.json` per finding (or grouped by
   shared root cause per the dependency notes above), each with accurate
   `files`, `call_sites`/`markers` where a hand-edit comment anchors the
   customization, and `findings_addressed`.

## Outcome

All 7 findings were fixed in this run. See
`.printing-press-patches/bonusly-fix-user-lookup-endpoints.json` (F1, F2),
`.printing-press-patches/bonusly-org-top-pagination-and-search.json` (F3),
`.printing-press-patches/bonusly-mentions-whitespace-validation.json` (F4,
F5 — F5 required no code change, only confirming no client-side amount
rejection existed), and `.printing-press-patches/bonusly-pat-only-auth.json`
(F6, F7 — this patch also retracts and deletes the prior
`bonusly-cookie-session-auth.json` patch, whose customization it fully
replaces).
