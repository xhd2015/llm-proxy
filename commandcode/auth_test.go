package commandcode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAuth(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}
	return dir
}

func TestReadAuthValid(t *testing.T) {
	home := writeAuth(t, `{"apiKey":"user_abc","userId":"u1","userName":"tester"}`)
	auth, err := ReadAuth(home)
	if err != nil {
		t.Fatalf("ReadAuth: %v", err)
	}
	if auth.APIKey != "user_abc" || auth.UserID != "u1" || auth.UserName != "tester" {
		t.Errorf("auth = %+v", auth)
	}
}

func TestReadAuthErrors(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantSub string
	}{
		{"empty apiKey", `{"userId":"u1"}`, "apiKey"},
		{"malformed json", `{not json`, "parse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReadAuth(writeAuth(t, tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("err = %v, want substring %q", err, tt.wantSub)
			}
		})
	}
}

func TestReadAuthMissingFile(t *testing.T) {
	_, err := ReadAuth(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Errorf("err = %v, want a credentials error", err)
	}
}

func TestReadAuthRereadsFile(t *testing.T) {
	home := writeAuth(t, `{"apiKey":"first"}`)
	if _, err := ReadAuth(home); err != nil {
		t.Fatalf("first read: %v", err)
	}
	// A rotated key must be picked up without restarting the proxy.
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"apiKey":"second"}`), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	auth, err := ReadAuth(home)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if auth.APIKey != "second" {
		t.Errorf("apiKey = %q, want second", auth.APIKey)
	}
}

func TestAuthPath(t *testing.T) {
	got, err := AuthPath("/tmp/cc-home")
	if err != nil {
		t.Fatalf("AuthPath: %v", err)
	}
	if got != "/tmp/cc-home/auth.json" {
		t.Errorf("AuthPath = %q", got)
	}
}
