package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mvanhorn/printing-press-library/library/food-and-dining/fooda/internal/client"
	"github.com/mvanhorn/printing-press-library/library/food-and-dining/fooda/internal/store"
)

type seedOrder struct {
	uuid     string
	vendor   string
	items    string
	delivery string
	unix     int64
}

// orderProps builds one order in the shape the live orders page returns.
func orderProps(o seedOrder, id int) map[string]any {
	return map[string]any{"presenter": []any{map[string]any{"order": map[string]any{
		"id":                        id,
		"event_type":                "delivery",
		"item_names":                []string{o.items},
		"order_fulfilled_time_unix": o.unix,
		"vendor_names":              []string{o.vendor},
		"status":                    "checkout_complete",
		"delivery":                  o.delivery,
		"request_id":                o.uuid,
		"payment_cents":             0,
	}}}}
}

// seedOrders writes orders into the synced store under the current FOODA_HOME.
func seedOrders(t *testing.T, orders ...seedOrder) {
	t.Helper()
	db, err := store.OpenWithContext(context.Background(), defaultDBPath("fooda-pp-cli"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	for i, o := range orders {
		raw, err := json.Marshal(orderProps(o, 9000+i))
		if err != nil {
			t.Fatalf("marshal order: %v", err)
		}
		if err := db.Upsert("order", o.uuid, raw); err != nil {
			t.Fatalf("seed order %s: %v", o.uuid, err)
		}
	}
}

// ordersFor decodes orders through the production props type.
func ordersFor(t *testing.T, orders ...seedOrder) []client.PastOrdersProps {
	t.Helper()
	var out []client.PastOrdersProps
	for i, o := range orders {
		raw, err := json.Marshal(orderProps(o, 9000+i))
		if err != nil {
			t.Fatalf("marshal order: %v", err)
		}
		var p client.PastOrdersProps
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("decode order: %v", err)
		}
		out = append(out, p)
	}
	return out
}

func deliveredText(d time.Time) string {
	return fmt.Sprintf("Delivered on %d/%d/%d at 12:14 PM EST", int(d.Month()), d.Day(), d.Year())
}

func scheduledText(d time.Time) string {
	return fmt.Sprintf("Delivering on %d/%d/%d between 12:30 PM and 1:00 PM EST", int(d.Month()), d.Day(), d.Year())
}

func isoDate(d time.Time) string { return d.Format("2006-01-02") }

func runCLI(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := RootCmd()
	cmd.SetArgs(args)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

// useFakeFooda points the CLI at srv through a config file, as the other
// end-to-end tests do, and returns the --config path to pass on the command line.
func useFakeFooda(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("FOODA_HOME", home)
	cfgDir := filepath.Join(home, "config")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(cfgDir, "fooda_config.json")
	cfgJSON, _ := json.Marshal(map[string]any{"base_url": srv.URL})
	if err := os.WriteFile(cfgPath, cfgJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

func TestServedHistoryExcludesScheduledOrders(t *testing.T) {
	t.Setenv("FOODA_HOME", t.TempDir())

	now := time.Now()
	past := now.AddDate(0, 0, -2)
	future := now.AddDate(0, 0, 1)
	seedOrders(t,
		seedOrder{uuid: "uuid-delivered", vendor: "Example Noodle Bar", items: "Example Noodles", delivery: deliveredText(past), unix: past.Unix()},
		seedOrder{uuid: "uuid-scheduled", vendor: "Example Grill", items: "Example Burger", delivery: scheduledText(future), unix: future.Unix()},
		seedOrder{uuid: "uuid-legacy", vendor: "Example Cafe", items: "Example Soup", delivery: "Delivered last week", unix: past.Unix()},
	)

	stdout, stderr, err := runCLI(t, "served-history", "--data-source", "local", "--since", "30d", "--json")
	if err != nil {
		t.Fatalf("served-history failed: %v, stderr: %s", err, stderr)
	}

	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("served-history output is not JSON: %v\n%s", err, stdout)
	}
	vendors := map[string]bool{}
	for _, r := range rows {
		vendors[fmt.Sprint(r["vendor"])] = true
	}
	if !vendors["Example Noodle Bar"] {
		t.Errorf("delivered order missing from served-history: %s", stdout)
	}
	if !vendors["Example Cafe"] {
		t.Errorf("order with unrecognized delivery text must stay in served-history: %s", stdout)
	}
	if vendors["Example Grill"] {
		t.Errorf("scheduled (not yet delivered) order must not be listed as served: %s", stdout)
	}
}

func TestClassifyDelivery(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		wantState string
		wantDate  string
	}{
		{"delivered", "Delivered on 3/4/2026 at 12:14 PM EST", "delivered", "2026-03-04"},
		{"two-digit month and day", "Delivered on 12/31/2025 at 11:59 AM PST", "delivered", "2025-12-31"},
		{"scheduled", "Delivering on 3/5/2026 between 12:30 PM and 1:00 PM EST", "scheduled", "2026-03-05"},
		{"case and padding tolerant", "  delivered ON 3/4/2026 at 12:14 PM EST", "delivered", "2026-03-04"},
		{"legacy free text", "Delivered today", "unknown", ""},
		{"empty", "", "unknown", ""},
		{"impossible date", "Delivered on 2/30/2026 at 12:00 PM EST", "unknown", ""},
		{"month out of range", "Delivering on 13/1/2026 between 12:30 PM and 1:00 PM EST", "unknown", ""},
		{"unrelated text", "Picked up on 3/4/2026", "unknown", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, date := classifyDelivery(tt.text)
			if state != tt.wantState || date != tt.wantDate {
				t.Errorf("classifyDelivery(%q) = (%q, %q), want (%q, %q)", tt.text, state, date, tt.wantState, tt.wantDate)
			}
		})
	}
}

func TestResolveDeliveryDate(t *testing.T) {
	now := time.Date(2026, 3, 4, 15, 0, 0, 0, time.Local)
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"today", "2026-03-04", false},
		{"", "2026-03-04", false},
		{"  Today ", "2026-03-04", false},
		{"2026-03-05", "2026-03-05", false},
		{"2026-02-30", "", true},
		{"tomorrow", "", true},
		{"3/4/2026", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := resolveDeliveryDate(tt.in, now)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveDeliveryDate(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("resolveDeliveryDate(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDeliveryRowsForDate(t *testing.T) {
	day := func(d int) int64 { return time.Date(2026, 3, d, 12, 0, 0, 0, time.Local).Unix() }
	orders := ordersFor(t,
		seedOrder{uuid: "u-delivered", vendor: "Example Noodle Bar", items: "Noodles", delivery: "Delivered on 3/4/2026 at 12:14 PM EST", unix: day(4)},
		seedOrder{uuid: "u-scheduled", vendor: "Example Grill", items: "Burger", delivery: "Delivering on 3/5/2026 between 12:30 PM and 1:00 PM EST", unix: day(5)},
		seedOrder{uuid: "u-older", vendor: "Example Cafe", items: "Soup", delivery: "Delivered on 3/3/2026 at 12:10 PM EST", unix: day(3)},
		seedOrder{uuid: "u-unknown", vendor: "Example Deli", items: "Sandwich", delivery: "Delivered today", unix: day(4)},
	)

	t.Run("delivered and unknown-text orders on the date, in input order", func(t *testing.T) {
		rows := deliveryRowsForDate(orders, "2026-03-04")
		if len(rows) != 2 {
			t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
		}
		if rows[0].UUID != "u-delivered" || rows[0].State != "delivered" || rows[0].Date != "2026-03-04" {
			t.Errorf("row 0 = %+v", rows[0])
		}
		if rows[1].UUID != "u-unknown" || rows[1].State != "unknown" || rows[1].Delivery != "Delivered today" {
			t.Errorf("row 1 must keep raw text and fall back to the fulfilled-time date: %+v", rows[1])
		}
	})

	t.Run("scheduled order", func(t *testing.T) {
		rows := deliveryRowsForDate(orders, "2026-03-05")
		if len(rows) != 1 || rows[0].State != "scheduled" || rows[0].Vendor != "Example Grill" || rows[0].Items != "Burger" {
			t.Errorf("rows = %+v", rows)
		}
	})

	t.Run("no orders yields an empty, non-nil slice", func(t *testing.T) {
		rows := deliveryRowsForDate(orders, "2026-03-09")
		if rows == nil || len(rows) != 0 {
			t.Errorf("rows = %#v, want empty non-nil slice", rows)
		}
	})
}

func TestDeliveryStatusFromSyncedStore(t *testing.T) {
	t.Setenv("FOODA_HOME", t.TempDir())

	now := time.Now()
	tomorrow := now.AddDate(0, 0, 1)
	older := now.AddDate(0, 0, -3)
	seedOrders(t,
		seedOrder{uuid: "u-today", vendor: "Example Noodle Bar", items: "Example Noodles", delivery: deliveredText(now), unix: now.Unix()},
		seedOrder{uuid: "u-tomorrow", vendor: "Example Grill", items: "Example Burger", delivery: scheduledText(tomorrow), unix: tomorrow.Unix()},
		seedOrder{uuid: "u-older", vendor: "Example Cafe", items: "Example Soup", delivery: deliveredText(older), unix: older.Unix()},
	)

	decode := func(t *testing.T, stdout string) []deliveryRow {
		t.Helper()
		var rows []deliveryRow
		if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
			t.Fatalf("output is not a JSON array: %v\n%s", err, stdout)
		}
		return rows
	}

	t.Run("defaults to today", func(t *testing.T) {
		stdout, stderr, err := runCLI(t, "delivery-status", "--data-source", "local", "--json")
		if err != nil {
			t.Fatalf("delivery-status failed: %v, stderr: %s", err, stderr)
		}
		rows := decode(t, stdout)
		if len(rows) != 1 || rows[0].State != "delivered" || rows[0].Vendor != "Example Noodle Bar" || rows[0].Date != isoDate(now) {
			t.Errorf("rows = %+v", rows)
		}
	})

	t.Run("explicit date finds a scheduled order", func(t *testing.T) {
		stdout, stderr, err := runCLI(t, "delivery-status", "--data-source", "local", "--date", isoDate(tomorrow), "--json")
		if err != nil {
			t.Fatalf("delivery-status failed: %v, stderr: %s", err, stderr)
		}
		rows := decode(t, stdout)
		if len(rows) != 1 || rows[0].State != "scheduled" || rows[0].Vendor != "Example Grill" {
			t.Errorf("rows = %+v", rows)
		}
	})

	t.Run("no order is an empty array and a clean exit", func(t *testing.T) {
		stdout, stderr, err := runCLI(t, "delivery-status", "--data-source", "local", "--date", "2000-01-01", "--json")
		if err != nil {
			t.Fatalf("a date with no order must not be an error: %v, stderr: %s", err, stderr)
		}
		if strings.TrimSpace(stdout) != "[]" {
			t.Errorf("stdout = %q, want []", stdout)
		}
	})

	t.Run("human output", func(t *testing.T) {
		stdout, _, err := runCLI(t, "delivery-status", "--data-source", "local")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"delivered", "Example Noodle Bar", "Example Noodles"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("human output missing %q:\n%s", want, stdout)
			}
		}
		stdout, _, err = runCLI(t, "delivery-status", "--data-source", "local", "--date", "2000-01-01")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stdout, "No order found for 2000-01-01") {
			t.Errorf("empty-day message missing:\n%s", stdout)
		}
	})

	t.Run("rejects a bad --date as a usage error", func(t *testing.T) {
		_, _, err := runCLI(t, "delivery-status", "--data-source", "local", "--date", "next tuesday")
		if err == nil {
			t.Fatal("expected an error for an unparseable --date")
		}
		if ExitCode(err) != 2 || !strings.Contains(err.Error(), "--date") {
			t.Errorf("want usage error (exit 2) naming --date, got exit %d: %v", ExitCode(err), err)
		}
	})
}

func TestDeliveryStatusLive(t *testing.T) {
	now := time.Now()
	props, err := json.Marshal(orderProps(seedOrder{
		uuid: "u-live", vendor: "Example Noodle Bar", items: "Example Noodles",
		delivery: deliveredText(now), unix: now.Unix(),
	}, 9100))
	if err != nil {
		t.Fatal(err)
	}
	page := fmt.Sprintf(`<html><body><div id="order" props="%s"></div></body></html>`, html.EscapeString(string(props)))

	var gotPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		if r.URL.Path == "/settings/orders" {
			_, _ = w.Write([]byte(page))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	cfgPath := useFakeFooda(t, srv)

	stdout, stderr, err := runCLI(t, "delivery-status", "--config", cfgPath, "--json")
	if err != nil {
		t.Fatalf("delivery-status (live) failed: %v, stderr: %s", err, stderr)
	}
	var rows []deliveryRow
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("output is not a JSON array: %v\n%s", err, stdout)
	}
	if len(rows) != 1 || rows[0].State != "delivered" || rows[0].UUID != "u-live" {
		t.Errorf("rows = %+v", rows)
	}
	if len(gotPaths) != 1 || gotPaths[0] != "/settings/orders" {
		t.Errorf("live path should read only /settings/orders, requested %v", gotPaths)
	}
}

func TestDeliveryStatusDryRunSendsNothing(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
	}))
	defer srv.Close()
	cfgPath := useFakeFooda(t, srv)

	if _, stderr, err := runCLI(t, "delivery-status", "--config", cfgPath, "--dry-run"); err != nil {
		t.Fatalf("dry-run failed: %v, stderr: %s", err, stderr)
	}
	if requests != 0 {
		t.Errorf("dry-run sent %d requests, want 0", requests)
	}
}

func TestWeekAheadNotesDroppedEvents(t *testing.T) {
	const hint = "delivery-status"

	serve := func(t *testing.T, eventDate time.Time) string {
		t.Helper()
		event := fmt.Sprintf(`{"id":"1","startTime":"%sT12:00:00Z","endTime":"%sT13:00:00Z","status":"ACTIVE","__typename":"PopupEventPublic","restaurants":[{"id":"r1","name":"Example Kitchen"}]}`,
			isoDate(eventDate), isoDate(eventDate))
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/my":
				_, _ = w.Write([]byte(`<html><head><meta name="api-client-token" content="dummy-client"><meta name="api-session-token" content="dummy-session"></head></html>`))
			case "/graphql":
				_, _ = w.Write([]byte(`{"data":{"searchPublicEvents":{"events":{"nodes":[` + event + `]}}}}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(srv.Close)
		return useFakeFooda(t, srv)
	}

	run := func(t *testing.T, cfgPath string, extra ...string) (string, string) {
		t.Helper()
		resetMemoizedAccountAndBuilding()
		args := append([]string{"week-ahead", "--config", cfgPath, "--account", "a", "--building", "b"}, extra...)
		stdout, stderr, err := runCLI(t, args...)
		if err != nil {
			t.Fatalf("week-ahead failed: %v, stderr: %s", err, stderr)
		}
		return stdout, stderr
	}

	t.Run("hint on stderr when today has no event", func(t *testing.T) {
		cfg := serve(t, time.Now().AddDate(0, 0, 1))
		stdout, stderr := run(t, cfg)
		if !strings.Contains(stdout, "Example Kitchen") {
			t.Fatalf("expected tomorrow's event in stdout:\n%s", stdout)
		}
		if !strings.Contains(stderr, hint) {
			t.Errorf("stderr should point at delivery-status when today is absent:\n%s", stderr)
		}
		if strings.Contains(stdout, hint) {
			t.Errorf("hint must not pollute stdout:\n%s", stdout)
		}
	})

	t.Run("no hint when today has an event", func(t *testing.T) {
		cfg := serve(t, time.Now())
		_, stderr := run(t, cfg)
		if strings.Contains(stderr, hint) {
			t.Errorf("unexpected hint when today has an event:\n%s", stderr)
		}
	})

	t.Run("no hint in machine-readable mode", func(t *testing.T) {
		cfg := serve(t, time.Now().AddDate(0, 0, 1))
		_, stderr := run(t, cfg, "--json")
		if strings.Contains(stderr, hint) {
			t.Errorf("--json must stay free of stderr chatter:\n%s", stderr)
		}
	})

	t.Run("help explains the drop-off", func(t *testing.T) {
		stdout, _, err := runCLI(t, "week-ahead", "--help")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stdout, hint) || !strings.Contains(stdout, "delivered") {
			t.Errorf("week-ahead --help should explain that delivered events drop off and name delivery-status:\n%s", stdout)
		}
	})
}

func TestWhichFindsDeliveryStatus(t *testing.T) {
	for _, q := range []string{"was my order delivered", "has my food been delivered", "check if today's order has been delivered", "delivery status"} {
		t.Run(q, func(t *testing.T) {
			matches := rankWhich(whichIndex, q, 3)
			if len(matches) == 0 || matches[0].Entry.Command != "delivery-status" {
				t.Errorf("rankWhich(%q) top = %+v, want delivery-status first", q, matches)
			}
		})
	}
}
