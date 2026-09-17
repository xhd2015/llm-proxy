package openai

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	grokapi "github.com/xhd2015/dot-pkgs/go-pkgs/shell/grok/api"
)

func TestGrokPortNumber(t *testing.T) {
	n, err := grokPortNumber("")
	if err != nil || n != 0 {
		t.Fatalf("empty: n=%d err=%v", n, err)
	}
	n, err = grokPortNumber("8893")
	if err != nil || n != 8893 {
		t.Fatalf("8893: n=%d err=%v", n, err)
	}
	if _, err := grokPortNumber("nope"); err == nil {
		t.Fatal("want invalid port error")
	}
}

func TestLoadGrokCacheAndCodexConfig(t *testing.T) {
	dir := t.TempDir()
	cache := []byte(`{"models":{"grok-4.6":{"info":{"id":"grok-4.6","name":"Grok 4.6","supported_in_api":true,"reasoning_efforts":[{"value":"high","default":true}]}}}}`)
	if err := os.WriteFile(filepath.Join(dir, "models_cache.json"), cache, 0o644); err != nil {
		t.Fatal(err)
	}
	got := grokapi.CodexConfig("http://localhost:9000/v1", loadGrokCache(dir))
	for _, want := range []string{
		`model = "grok-4.6"`,
		`base_url = "http://localhost:9000/v1"`,
		`wire_api = "responses"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
}
