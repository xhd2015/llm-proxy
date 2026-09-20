package openai

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDSHModelsConfigFlagPositions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	for _, args := range [][]string{
		{"--config", path, "dsh-models"},
		{"dsh-models", "--config", path},
		{"dsh-models", "--config=" + path},
	} {
		err := Handle(args)
		if err == nil || !strings.Contains(err.Error(), "load --config:") || !strings.Contains(err.Error(), "missing.json") {
			t.Fatalf("Handle(%q) = %v; expected config loading", args, err)
		}
	}
}

func TestDSHModelsRequiresConfig(t *testing.T) {
	if err := Handle([]string{"dsh-models"}); err == nil || err.Error() != "dsh-models requires --config FILE" {
		t.Fatalf("unexpected error: %v", err)
	}
}
