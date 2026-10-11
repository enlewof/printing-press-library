package cli

import (
	"net/url"
	"strings"
	"testing"
)

func TestRedactedFormPreviewMasksCSRFToken(t *testing.T) {
	v := url.Values{}
	v.Set("authenticity_token", "secret-token-value")
	v.Set("quantity", "1")
	got := redactedFormPreview(v)
	if strings.Contains(got, "secret-token-value") {
		t.Fatalf("token leaked in preview: %s", got)
	}
	if !strings.Contains(got, "quantity=1") {
		t.Fatalf("expected non-secret fields preserved: %s", got)
	}
}
