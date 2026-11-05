// Package modelcatalog reads provider model directories without inference or configuration writes.
package modelcatalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Model preserves the upstream id verbatim; display names and vendor aliases are not routing ids.
type Model struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name,omitempty"`
	DisplayName            string   `json:"display_name,omitempty"`
	ContextWindow          int      `json:"context_window,omitempty"`
	ContextLength          int      `json:"context_length,omitempty"`
	MaxTokens              int      `json:"max_tokens,omitempty"`
	MaxOutputTokens        int      `json:"max_output_tokens,omitempty"`
	ReasoningEfforts       []string `json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty"`
}

type Catalog struct {
	FetchedAt time.Time
	Models    []Model
	Notes     []string
	Auth      string
}

// Reasoning is advertised or explicitly configured metadata, never an inference result.
// Unknown is distinct from unsupported. A disabled generated proxy route alone is
// not evidence that the upstream cannot reason.
type Reasoning struct {
	State   string   `json:"state"`
	Source  string   `json:"source"`
	Efforts []string `json:"efforts,omitempty"`
	Default string   `json:"default,omitempty"`
}

func (m Model) Reasoning() Reasoning {
	if len(m.ReasoningEfforts) == 0 {
		return Reasoning{State: "unknown", Source: "unknown"}
	}
	return Reasoning{State: "known", Source: "api", Efforts: append([]string(nil), m.ReasoningEfforts...), Default: m.DefaultReasoningEffort}
}

// ValidateReasoning checks shape, not a particular client's fixed vocabulary.
// A gateway may add values which an individual client must subsequently filter.
func ValidateReasoning(m Model) error {
	seen := map[string]bool{}
	for _, e := range m.ReasoningEfforts {
		if e == "" || strings.TrimSpace(e) != e || strings.ContainsAny(e, "\r\n\x00") {
			return fmt.Errorf("model %q has invalid reasoning metadata", m.ID)
		}
		if seen[e] {
			return fmt.Errorf("model %q repeats reasoning effort %q", m.ID, e)
		}
		seen[e] = true
	}
	if e := m.DefaultReasoningEffort; e != "" {
		if strings.TrimSpace(e) != e || strings.ContainsAny(e, "\r\n\x00") {
			return fmt.Errorf("model %q has invalid default reasoning metadata", m.ID)
		}
		if len(seen) > 0 && !seen[e] {
			return fmt.Errorf("model %q default reasoning effort is not in reasoning_efforts", m.ID)
		}
	}
	return nil
}

// Parse accepts the OpenAI-style data array. It does not identify products from ids.
func Parse(body []byte) (Catalog, error) {
	var payload struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Data == nil {
		return Catalog{}, errors.New("expected a JSON object with a data model array")
	}
	cat := Catalog{Models: make([]Model, 0, len(payload.Data))}
	seen := map[string]bool{}
	skipped, unknown := 0, 0
	for _, raw := range payload.Data {
		var m Model
		if err := json.Unmarshal(raw, &m); err != nil {
			return Catalog{}, errors.New("a model entry has invalid id or reasoning metadata types")
		}
		if strings.TrimSpace(m.ID) == "" || seen[m.ID] {
			skipped++
			continue
		}
		if strings.TrimSpace(m.ID) != m.ID || strings.ContainsAny(m.ID, "\r\n\x00") {
			return Catalog{}, errors.New("a model id has surrounding whitespace or control characters")
		}
		if err := ValidateReasoning(m); err != nil {
			return Catalog{}, err
		}
		seen[m.ID] = true
		if len(m.ReasoningEfforts) == 0 || m.DefaultReasoningEffort == "" {
			unknown++
		}
		cat.Models = append(cat.Models, m)
	}
	if len(cat.Models) == 0 {
		return Catalog{}, errors.New("the endpoint returned no model ids; this catalog is incomplete")
	}
	if skipped > 0 {
		cat.Notes = append(cat.Notes, fmt.Sprintf("skipped %d duplicate or empty ids.", skipped))
	}
	if unknown > 0 {
		cat.Notes = append(cat.Notes, fmt.Sprintf("%d models omit complete reasoning metadata; generated routes use reasoning disabled as a local policy, not a capability claim.", unknown))
	}
	return cat, nil
}
