package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mvanhorn/printing-press-library/library/food-and-dining/fooda/internal/store"
)

// saveSyncTime records when orders were last synced, as `sync` does.
func saveSyncTime(t *testing.T, at time.Time, count int) {
	t.Helper()
	db, err := store.OpenWithContext(context.Background(), defaultDBPath("fooda-pp-cli"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	if err := db.SaveSyncStateAt("order", "complete", count, at); err != nil {
		t.Fatalf("save sync state: %v", err)
	}
}

// unreachableFooda points the CLI at a server that has already shut down, so
// every request fails with "connection refused". It sets FOODA_HOME.
func unreachableFooda(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	cfg := useFakeFooda(t, srv)
	srv.Close()
	return cfg
}

type agentRows struct {
	Meta struct {
		Source string `json:"source"`
	} `json:"meta"`
	Results []deliveryRow `json:"results"`
}

func TestDeliveryStatusEmptySyncedStoreIsAnEmptyResult(t *testing.T) {
	t.Setenv("FOODA_HOME", t.TempDir())
	saveSyncTime(t, time.Now(), 0)

	stdout, stderr, err := runCLI(t, "delivery-status", "--data-source", "local", "--json")
	if err != nil {
		t.Fatalf("a synced history with zero orders is a valid result, got error: %v (stderr: %s)", err, stderr)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Errorf("stdout = %q, want []", stdout)
	}

	stdout, _, err = runCLI(t, "delivery-status", "--data-source", "local", "--date", "2000-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "No order found for 2000-01-01") {
		t.Errorf("empty-day message missing:\n%s", stdout)
	}
}

func TestDeliveryStatusMissingStoreIsNotFound(t *testing.T) {
	t.Setenv("FOODA_HOME", t.TempDir())

	_, _, err := runCLI(t, "delivery-status", "--data-source", "local")
	if err == nil {
		t.Fatal("expected an error when no store exists")
	}
	if ExitCode(err) != 3 || !strings.Contains(err.Error(), "sync") {
		t.Errorf("want not-found (exit 3) pointing at sync, got exit %d: %v", ExitCode(err), err)
	}
}

func TestDeliveryStatusFallsBackToSyncedOrdersWhenOffline(t *testing.T) {
	cfg := unreachableFooda(t)
	now := time.Now()
	syncedAt := now.Add(-2 * time.Hour).Truncate(time.Minute)
	seedOrders(t, seedOrder{uuid: "u-offline", vendor: "Example Noodle Bar", items: "Example Noodles", delivery: deliveredText(now), unix: now.Unix()})
	saveSyncTime(t, syncedAt, 1)

	t.Run("auto reads the synced store and says so", func(t *testing.T) {
		stdout, stderr, err := runCLI(t, "delivery-status", "--config", cfg, "--json")
		if err != nil {
			t.Fatalf("offline with synced orders should fall back, got: %v (stderr: %s)", err, stderr)
		}
		var rows []deliveryRow
		if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
			t.Fatalf("output is not a JSON array: %v\n%s", err, stdout)
		}
		if len(rows) != 1 || rows[0].UUID != "u-offline" || rows[0].State != "delivered" {
			t.Errorf("rows = %+v", rows)
		}
		if !strings.Contains(stderr, "falling back to local database") {
			t.Errorf("stderr should announce the fallback:\n%s", stderr)
		}
		want := syncedAt.Local().Format("2006-01-02 15:04")
		if !strings.Contains(stderr, want) || !strings.Contains(stderr, "out of date") {
			t.Errorf("stderr should give the sync time %q and warn the state may be stale:\n%s", want, stderr)
		}
	})

	t.Run("agent envelope marks the result local", func(t *testing.T) {
		stdout, _, err := runCLI(t, "delivery-status", "--config", cfg, "--agent", "--select", "date,state")
		if err != nil {
			t.Fatal(err)
		}
		var env agentRows
		if err := json.Unmarshal([]byte(stdout), &env); err != nil {
			t.Fatalf("not an agent envelope: %v\n%s", err, stdout)
		}
		if env.Meta.Source != "local" || len(env.Results) != 1 {
			t.Errorf("meta.source = %q with %d results, want local with 1", env.Meta.Source, len(env.Results))
		}
	})

	t.Run("--data-source live never falls back", func(t *testing.T) {
		stdout, stderr, err := runCLI(t, "delivery-status", "--config", cfg, "--data-source", "live", "--json")
		if err == nil {
			t.Fatalf("live-only must surface the network error, got output: %s", stdout)
		}
		if strings.Contains(stderr, "falling back") {
			t.Errorf("live-only must not announce a fallback:\n%s", stderr)
		}
	})
}

func TestDeliveryStatusOfflineWithoutASyncedStoreExplainsBoth(t *testing.T) {
	cfg := unreachableFooda(t)

	_, _, err := runCLI(t, "delivery-status", "--config", cfg)
	if err == nil {
		t.Fatal("expected an error with no network and no synced store")
	}
	if !strings.Contains(err.Error(), "connection refused") || !strings.Contains(err.Error(), "no local database fallback available") {
		t.Errorf("error should carry the live cause and the missing fallback, got: %v", err)
	}
}

func TestDeliveryStatusDoesNotFallBackOnNonNetworkErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	cfg := useFakeFooda(t, srv)
	now := time.Now()
	seedOrders(t, seedOrder{uuid: "u-stale", vendor: "Example Noodle Bar", items: "Example Noodles", delivery: deliveredText(now), unix: now.Unix()})

	stdout, stderr, err := runCLI(t, "delivery-status", "--config", cfg, "--json")
	if err == nil {
		t.Fatalf("an HTTP error is not a connectivity problem and must not be papered over with old data, got: %s", stdout)
	}
	if strings.Contains(stderr, "falling back") {
		t.Errorf("unexpected fallback on an HTTP error:\n%s", stderr)
	}
}

func TestDeliveryStatusLiveAgentEnvelopeIsLive(t *testing.T) {
	now := time.Now()
	props, err := json.Marshal(orderProps(seedOrder{uuid: "u-live", vendor: "Example Noodle Bar", items: "Example Noodles", delivery: deliveredText(now), unix: now.Unix()}, 9200))
	if err != nil {
		t.Fatal(err)
	}
	page := fmt.Sprintf(`<html><body><div id="order" props="%s"></div></body></html>`, html.EscapeString(string(props)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/settings/orders" {
			_, _ = w.Write([]byte(page))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	cfg := useFakeFooda(t, srv)

	stdout, stderr, err := runCLI(t, "delivery-status", "--config", cfg, "--agent", "--select", "date,state")
	if err != nil {
		t.Fatalf("live agent run failed: %v (stderr: %s)", err, stderr)
	}
	var env agentRows
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("not an agent envelope: %v\n%s", err, stdout)
	}
	if env.Meta.Source != "live" || len(env.Results) != 1 {
		t.Errorf("meta.source = %q with %d results, want live with 1", env.Meta.Source, len(env.Results))
	}
	if strings.Contains(stderr, "falling back") {
		t.Errorf("a successful live read must not mention a fallback:\n%s", stderr)
	}
}
