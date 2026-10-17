package cli

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// PATCH(amend-2026-10-17: add test suite for CDP discovery and safety)

func TestParseDevToolsActivePort(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantURL string
		wantOk  bool
	}{
		{
			name:    "valid",
			content: "9222\n/devtools/browser/abc",
			wantURL: "ws://127.0.0.1:9222/devtools/browser/abc",
			wantOk:  true,
		},
		{
			name:    "CRLF",
			content: "9222\r\n/devtools/browser/abc\r\n",
			wantURL: "ws://127.0.0.1:9222/devtools/browser/abc",
			wantOk:  true,
		},
		{
			name:    "non-numeric port",
			content: "abc\n/devtools/browser/abc",
			wantURL: "",
			wantOk:  false,
		},
		{
			name:    "port 0",
			content: "0\n/devtools/browser/abc",
			wantURL: "",
			wantOk:  false,
		},
		{
			name:    "port 70000",
			content: "70000\n/devtools/browser/abc",
			wantURL: "",
			wantOk:  false,
		},
		{
			name:    "missing path",
			content: "9222",
			wantURL: "",
			wantOk:  false,
		},
		{
			name:    "missing path newline",
			content: "9222\n",
			wantURL: "",
			wantOk:  false,
		},
		{
			name:    "path without prefix",
			content: "9222\n/browser/abc",
			wantURL: "",
			wantOk:  false,
		},
		{
			name:    "path containing ..",
			content: "9222\n/devtools/browser/../abc",
			wantURL: "",
			wantOk:  false,
		},
		{
			name:    "whitespace in path",
			content: "9222\n/devtools/browser/abc def",
			wantURL: "",
			wantOk:  false,
		},
		{
			name:    "empty",
			content: "",
			wantURL: "",
			wantOk:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURL, gotOk := parseDevToolsActivePort(tt.content)
			if gotOk != tt.wantOk {
				t.Fatalf("got ok = %v, want %v", gotOk, tt.wantOk)
			}
			if gotOk && gotURL != tt.wantURL {
				t.Fatalf("got URL = %q, want %q", gotURL, tt.wantURL)
			}
		})
	}
}

func TestDevToolsActivePortTargets(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("CONCUR_CHROME_USER_DATA_DIR", tempDir)

	t.Run("absent", func(t *testing.T) {
		targets := devToolsActivePortTargets()
		if len(targets) != 0 {
			t.Fatalf("expected 0 targets, got %v", targets)
		}
	})

	t.Run("present and valid", func(t *testing.T) {
		content := "9222\n/devtools/browser/valid-id\n"
		err := os.WriteFile(filepath.Join(tempDir, "DevToolsActivePort"), []byte(content), 0644)
		if err != nil {
			t.Fatal(err)
		}
		targets := devToolsActivePortTargets()
		if len(targets) != 1 || targets[0] != "ws://127.0.0.1:9222/devtools/browser/valid-id" {
			t.Fatalf("unexpected targets: %v", targets)
		}
	})

	t.Run("profile subdirectory (chrome://inspect mode)", func(t *testing.T) {
		if err := os.Remove(filepath.Join(tempDir, "DevToolsActivePort")); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		profileDir := filepath.Join(tempDir, "Default")
		if err := os.MkdirAll(profileDir, 0o755); err != nil {
			t.Fatal(err)
		}
		content := "9222\n/devtools/browser/profile-id\n"
		if err := os.WriteFile(filepath.Join(profileDir, "DevToolsActivePort"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		targets := devToolsActivePortTargets()
		if len(targets) != 1 || targets[0] != "ws://127.0.0.1:9222/devtools/browser/profile-id" {
			t.Fatalf("unexpected targets: %v", targets)
		}
		_ = os.RemoveAll(profileDir)
	})

	t.Run("malformed", func(t *testing.T) {
		content := "abc\n/devtools/browser/valid-id\n"
		err := os.WriteFile(filepath.Join(tempDir, "DevToolsActivePort"), []byte(content), 0644)
		if err != nil {
			t.Fatal(err)
		}
		targets := devToolsActivePortTargets()
		if len(targets) != 0 {
			t.Fatalf("expected 0 targets, got %v", targets)
		}
	})
}

func TestDescribeCDPTarget(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   string
	}{
		{
			name:   "plain port",
			target: "9222",
			want:   "port 9222",
		},
		{
			name:   "ws URL with host, port, path, ID",
			target: "ws://127.0.0.1:9222/devtools/browser/abcdef-1234",
			want:   "port 9222",
		},
		{
			name:   "ws URL IPv6",
			target: "ws://[::1]:9333/devtools/browser/xyz",
			want:   "port 9333",
		},
		{
			name:   "invalid ws URL / anything else",
			target: "invalid-value",
			want:   "the dedicated Concur browser",
		},
		{
			name:   "empty",
			target: "",
			want:   "the dedicated Concur browser",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := describeCDPTarget(tt.target)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			// Verify that the ID never appears in the output
			if strings.Contains(got, "abcdef-1234") || strings.Contains(got, "xyz") {
				t.Fatalf("target description leaked session ID: %q", got)
			}
		})
	}
}

func TestDetectDedicatedConcurBrowser(t *testing.T) {
	// Save and temporarily clear package-level candidates to isolate test from real local ports
	origCandidates := concurCDPPortCandidates
	concurCDPPortCandidates = nil
	t.Cleanup(func() {
		concurCDPPortCandidates = origCandidates
	})

	// Create a temp directory for mock agent-browser
	mockBinDir := t.TempDir()
	mockPath := filepath.Join(mockBinDir, "agent-browser")

	recordFile := filepath.Join(mockBinDir, "args.log")

	// Mock script: appends command line args to recordFile and returns successful JSON for "get url"
	mockScript := fmt.Sprintf(`#!/bin/bash
echo "$@" >> "%s"
if [[ "$*" == *"get url"* ]]; then
	echo '{"success":true,"data":{"url":"https://us2.concursolutions.com/home"}}'
fi
`, recordFile)

	err := os.WriteFile(mockPath, []byte(mockScript), 0755)
	if err != nil {
		t.Fatal(err)
	}

	// Put mock bin on PATH
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", mockBinDir+string(filepath.ListSeparator)+origPath)

	// Set up throwaway listener
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	addr := listener.Addr().String()
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}

	tempUserDir := t.TempDir()
	t.Setenv("CONCUR_CHROME_USER_DATA_DIR", tempUserDir)

	// Write DevToolsActivePort pointing to our listener's port
	dtContent := fmt.Sprintf("%s\n/devtools/browser/mock-session-id-123\n", portStr)
	err = os.WriteFile(filepath.Join(tempUserDir, "DevToolsActivePort"), []byte(dtContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("detection success with open port", func(t *testing.T) {
		// Clear args log
		_ = os.Remove(recordFile)

		gotCDP := detectDedicatedConcurBrowser()
		wantCDP := fmt.Sprintf("ws://127.0.0.1:%s/devtools/browser/mock-session-id-123", portStr)
		if gotCDP != wantCDP {
			t.Fatalf("got CDP %q, want %q", gotCDP, wantCDP)
		}

		// Verify that the mock agent-browser was called with correct cdp argument
		argsData, err := os.ReadFile(recordFile)
		if err != nil {
			t.Fatalf("failed to read recorded args: %v", err)
		}
		argsStr := string(argsData)
		expectedArg := fmt.Sprintf("--cdp ws://127.0.0.1:%s/devtools/browser/mock-session-id-123", portStr)
		if !strings.Contains(argsStr, expectedArg) {
			t.Fatalf("expected args to contain %q, got: %q", expectedArg, argsStr)
		}
	})

	t.Run("detection skipped with closed port", func(t *testing.T) {
		// Close the listener so the port is closed
		listener.Close()

		// Clear args log
		_ = os.Remove(recordFile)

		gotCDP := detectDedicatedConcurBrowser()
		if gotCDP != "" {
			t.Fatalf("expected detection to skip and return empty, got %q", gotCDP)
		}

		// Verify that mock agent-browser was NEVER called (since port is closed and skipped before executing exec)
		if _, err := os.Stat(recordFile); err == nil {
			argsData, _ := os.ReadFile(recordFile)
			t.Fatalf("expected agent-browser to not be spawned, but it recorded args: %s", string(argsData))
		}
	})
}
