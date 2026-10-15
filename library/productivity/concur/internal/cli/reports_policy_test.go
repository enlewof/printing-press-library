package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestNewestReportIDs(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		n    int
		want []string
	}{
		{
			name: "newest reportDate first, wrapped in a content envelope",
			raw:  `{"content":[{"reportId":"A","reportDate":"2026-01-01"},{"reportId":"C","reportDate":"2026-09-28"},{"reportId":"B","reportDate":"2026-05-05"}]}`,
			n:    2,
			want: []string{"C", "B"},
		},
		{
			name: "bare array works too",
			raw:  `[{"reportId":"A","reportDate":"2026-01-01"},{"reportId":"B","reportDate":"2026-02-02"}]`,
			n:    5,
			want: []string{"B", "A"},
		},
		{
			name: "same-day reports tie-break deterministically",
			raw:  `[{"reportId":"A","reportDate":"2026-10-15"},{"reportId":"B","reportDate":"2026-10-15"}]`,
			n:    2,
			want: []string{"B", "A"},
		},
		{
			name: "objects without a reportId are ignored",
			raw:  `{"links":[{"rel":"next"}],"content":[{"name":"x"}]}`,
			n:    3,
			want: []string{},
		},
		{
			name: "garbage yields nothing",
			raw:  `<html>not json</html>`,
			n:    3,
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newestReportIDs([]byte(tt.raw), tt.n)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("newestReportIDs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPolicyFromReportHeader(t *testing.T) {
	id, name := policyFromReportHeader([]byte(`{"reportId":"R","policyId":"P1","policy":"Example Policy (EX-1)"}`))
	if id != "P1" || name != "Example Policy (EX-1)" {
		t.Errorf("got (%q, %q)", id, name)
	}
	if id, _ := policyFromReportHeader([]byte(`{"reportId":"R"}`)); id != "" {
		t.Errorf("header without a policy should yield no id, got %q", id)
	}
	if id, _ := policyFromReportHeader([]byte(`not json`)); id != "" {
		t.Errorf("unparsable header should yield no id, got %q", id)
	}
}

// A caller-supplied policyId must never be silently replaced by an inferred
// one; the helper has to bail out before it makes any request.
func TestRetryReportCreate_NeverOverridesExplicitPolicy(t *testing.T) {
	origErr := errors.New("policyId is required")
	data, status, err := retryReportCreateWithInferredPolicy(&cobra.Command{}, nil, "", nil,
		map[string]any{"name": "x", "policyId": "MINE"}, "user", "TRAVELER", origErr)
	if data != nil || status != 0 || !errors.Is(err, origErr) {
		t.Errorf("got (%v, %d, %v), want (nil, 0, origErr)", data, status, err)
	}
}

func TestReportsCreate_InfersPolicyOverHTTP(t *testing.T) {
	// The browser fallback must not be touched: any agent-browser call
	// leaves a marker the test then fails on.
	tmpDir := t.TempDir()
	marker := filepath.Join(tmpDir, "browser_invoked")
	mock := "#!/bin/bash\ntouch \"$MOCK_BROWSER_MARKER\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "agent-browser"), []byte(mock), 0o755); err != nil {
		t.Fatalf("writing mock agent-browser: %v", err)
	}
	t.Setenv("PATH", tmpDir+string(filepath.ListSeparator)+os.Getenv("PATH"))
	t.Setenv("MOCK_BROWSER_MARKER", marker)

	var posts []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reports"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			posts = append(posts, body)
			if _, has := body["policyId"]; !has {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"validationErrors":[{"source":"policyId","message":"policyId is required","id":"field.required"}]}`))
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"uri":"https://example.invalid/expensereports/v4/users/u/context/TRAVELER/reports/NEWREPORT"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/reports"):
			_, _ = w.Write([]byte(`{"content":[{"reportId":"OLDER","reportDate":"2026-01-01"},{"reportId":"NEWEST","reportDate":"2026-09-28"}]}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/reports/NEWEST"):
			_, _ = w.Write([]byte(`{"reportId":"NEWEST","policyId":"POLICY-EX","policy":"Example Policy (EX-1)"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	t.Setenv("CONCUR_BASE_URL", server.URL)
	t.Setenv("PRINTING_PRESS_VERIFY", "1")
	t.Setenv("PRINTING_PRESS_VERIFY_LIVE_HTTP", "1")

	cmd := RootCmd()
	cmd.SetArgs([]string{"reports", "create", "--user-id", "test-user-id", "--name", "Example Trip", "--yes", "--json"})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, errOut.String())
	}

	if len(posts) != 2 {
		t.Fatalf("expected 2 POSTs (rejected, then retried with a policy), got %d: %v", len(posts), posts)
	}
	if _, has := posts[0]["policyId"]; has {
		t.Errorf("first POST should not carry a policyId: %v", posts[0])
	}
	if posts[1]["policyId"] != "POLICY-EX" || posts[1]["name"] != "Example Trip" {
		t.Errorf("retry POST should carry the inferred policy and the original name: %v", posts[1])
	}
	if !strings.Contains(errOut.String(), "Example Policy (EX-1)") {
		t.Errorf("stderr should name the policy that was used, got: %q", errOut.String())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("browser fallback was invoked, but the HTTP retry should have made it unnecessary")
	}

	var envelope map[string]any
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	if ok, _ := envelope["success"].(bool); !ok {
		t.Errorf("expected success envelope, got %v", envelope)
	}
}
