package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// codexNativeBinEnv overrides the codex binary used to capture the native
// model catalog (unusual installs; tests construct loaders directly instead).
const codexNativeBinEnv = "LLM_PROXY_CODEX_BIN"

// nativeCatalog is the model catalog shipped by the installed Codex CLI, as
// rendered by `codex debug models`. Entries are kept as decoded JSON so the
// merge never has to know Codex's entry schema.
type nativeCatalog struct {
	models  []any
	version string // e.g. "codex-cli 0.155.1"; "" when it could not be read
	err     error  // non-nil when the native catalog is unavailable
}

// nativeCatalogLoader captures the native catalog once per instance: the
// *-models commands create one per invocation (always fresh), while the web
// editor keeps one per server process so preview requests reuse the snapshot.
type nativeCatalogLoader struct {
	once    sync.Once
	bin     string
	catalog nativeCatalog
}

func newNativeCatalogLoader() *nativeCatalogLoader {
	return &nativeCatalogLoader{bin: codexNativeBin()}
}

func codexNativeBin() string {
	if bin := strings.TrimSpace(os.Getenv(codexNativeBinEnv)); bin != "" {
		return bin
	}
	return "codex"
}

func (l *nativeCatalogLoader) get() nativeCatalog {
	l.once.Do(func() { l.catalog = loadNativeCatalog(l.bin) })
	return l.catalog
}

// loadNativeCatalog runs `codex debug models --bundled` and decodes the
// models array of the catalog bundled with the codex binary. The plain
// `codex debug models` refresh is deliberately NOT used: once the user
// enables model_catalog_json, it renders the effective catalog — the proxy's
// own installed catalog — and the capture would self-reference.
func loadNativeCatalog(bin string) nativeCatalog {
	version := nativeCodexVersion(bin)
	out, err := exec.Command(bin, "debug", "models", "--bundled").Output()
	if err != nil {
		return nativeCatalog{err: fmt.Errorf("could not read the bundled catalog from %s: %v", bin, err)}
	}
	decoder := json.NewDecoder(bytes.NewReader(out))
	decoder.UseNumber()
	var doc struct {
		Models []any `json:"models"`
	}
	if err := decoder.Decode(&doc); err != nil {
		return nativeCatalog{err: fmt.Errorf("could not parse the bundled catalog from %s: %v", bin, err)}
	}
	if doc.Models == nil {
		return nativeCatalog{err: fmt.Errorf("the bundled catalog from %s has no models array", bin)}
	}
	return nativeCatalog{models: doc.Models, version: version}
}

func nativeCodexVersion(bin string) string {
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// shortBaseInstructions is the minimal agent prompt for proxy models when no
// bundled prompt can be derived. Codex requires base_instructions on every
// catalog entry, so something must always fill the slot.
const shortBaseInstructions = "You are Codex, a coding agent."

// neutralBaseInstructions replaces the GPT-identity header of a bundled
// prompt: the upstream model is not GPT, but the workspace mission sentence
// from codex's own prompt is kept.
const neutralBaseInstructions = "You are Codex, a coding agent. You and the user share one workspace, and your job is to collaborate with them until their goal is genuinely handled."

// deriveBaseInstructions builds the agent prompt for proxy models from the
// bundled catalog capture: it picks the shortest bundled prompt (the leanest
// and least model-specific one) and replaces its identity header — everything
// before the first section header, which is where the GPT identity lives —
// with a neutral one. The body (editing constraints, autonomy, skills,
// communication rules) is model-agnostic and reused as-is. It returns ""
// when the capture holds no usable prompt, or when the body cannot be kept
// model-neutral.
func deriveBaseInstructions(nativeModels []any) string {
	shortest := ""
	for _, entry := range nativeModels {
		object, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		prompt, _ := object["base_instructions"].(string)
		if prompt == "" {
			continue
		}
		if shortest == "" || len(prompt) < len(shortest) {
			shortest = prompt
		}
	}
	if shortest == "" {
		return ""
	}
	headerStart := strings.Index(shortest, "\n# ")
	if headerStart < 0 {
		// The prompt is a bare identity paragraph with no reusable body.
		return neutralBaseInstructions
	}
	body := shortest[headerStart+1:]
	if strings.Contains(body, "GPT") {
		// The body must stay model-neutral to be reusable for any upstream.
		return ""
	}
	return neutralBaseInstructions + "\n\n" + body
}
