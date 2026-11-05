package modelcatalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Credential and Provider are the configuration schema shared with the config UI.
// They contain secrets: do not use them as an HTTP response or session record.
type Credential struct {
	Name       string `json:"name"`
	Selected   bool   `json:"selected"`
	Home       string `json:"home"`
	AuthFile   string `json:"authFile"`
	BaseURL    string `json:"baseUrl"`
	Token      string `json:"token"`
	TokenFile  string `json:"tokenFile"`
	DummyToken string `json:"dummyToken"`
}
type Provider struct {
	Name          string       `json:"name"`
	Kind          string       `json:"kind"`
	Subscription  bool         `json:"subscription"`
	Home          string       `json:"home"`
	AuthFile      string       `json:"authFile"`
	Version       string       `json:"version"`
	BaseURL       string       `json:"baseUrl"`
	Protocol      string       `json:"protocol"`
	ModelListMode string       `json:"modelListMode"`
	Token         string       `json:"token"`
	TokenFile     string       `json:"tokenFile"`
	DummyToken    string       `json:"dummyToken"`
	Credentials   []Credential `json:"credentials"`
}

// SelectedCredentialIndex preserves the config UI's first-selected, else first policy.
func (p Provider) SelectedCredentialIndex() int {
	if len(p.Credentials) == 0 {
		return -1
	}
	for i, c := range p.Credentials {
		if c.Selected {
			return i
		}
	}
	return 0
}
func (p *Provider) ApplySelected() {
	i := p.SelectedCredentialIndex()
	if i < 0 {
		return
	}
	c := p.Credentials[i]
	p.Home = c.Home
	p.AuthFile = c.AuthFile
	p.BaseURL = c.BaseURL
	p.Token = c.Token
	p.TokenFile = c.TokenFile
	p.DummyToken = c.DummyToken
}

// TokenReference describes an existing credential without copying its secret.
// JSON guards keep a reordered or retargeted config from silently changing identity.
type TokenReference struct {
	File        string            `json:"file,omitempty"`
	JSONPointer string            `json:"json_pointer,omitempty"`
	Guards      map[string]string `json:"guards,omitempty"`
	PublicToken string            `json:"public_token,omitempty"`
}

// Source is a backend-only snapshot. JSON and diagnostic formatting do not expose credentials.
type Source struct {
	Path      string         `json:"-"`
	Name      string         `json:"name"`
	BaseURL   string         `json:"base_url"`
	Protocol  string         `json:"protocol"`
	Reference TokenReference `json:"credential_reference"`
	token     string
	auth      string
	overrides map[string]Reasoning
}

func (s Source) String() string   { return s.Name + " (" + SafeURL(s.BaseURL) + ")" }
func (s Source) GoString() string { return s.String() }
func (s Source) Endpoint() (Endpoint, error) {
	u, err := ModelURL(s.BaseURL)
	return Endpoint{URL: u, Token: s.token, Auth: s.auth}, err
}
func (s Source) Reasoning(m Model) Reasoning {
	r := m.Reasoning()
	if r.State == "known" {
		return r
	}
	if configured, ok := s.overrides[m.ID]; ok {
		configured.Efforts = append([]string(nil), configured.Efforts...)
		return configured
	}
	return r
}
func DefaultConfigPath() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".config", "llm-proxy", "config.json"), nil
}
func expandPath(p string) (string, error) {
	p = os.ExpandEnv(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		h, e := os.UserHomeDir()
		if e != nil {
			return "", e
		}
		p = filepath.Join(h, strings.TrimPrefix(p, "~/"))
	}
	return filepath.Abs(p)
}

// ReadSource reads only the named provider and relevant explicit model declarations.
// Invalid unrelated routes do not prevent discovery; no configuration is changed.
func ReadSource(path, name string) (Source, error) {
	if path == "" {
		var err error
		path, err = DefaultConfigPath()
		if err != nil {
			return Source{}, err
		}
	}
	path, err := expandPath(path)
	if err != nil {
		return Source{}, errors.New("invalid model source config path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Source{}, fmt.Errorf("read model source config: %w", err)
	}
	var doc struct {
		Providers []Provider `json:"providers"`
		Models    []struct {
			Protocol  string `json:"protocol"`
			Provider  string `json:"provider"`
			Model     string `json:"providerModelName"`
			Excluded  bool   `json:"excluded"`
			Reasoning *struct {
				Disabled bool               `json:"disabled"`
				Default  string             `json:"defaultEffort"`
				Mapping  map[string]*string `json:"effortsMapping"`
			} `json:"reasoning"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return Source{}, errors.New("invalid model source configuration JSON")
	}
	index := -1
	for i, p := range doc.Providers {
		if p.Name == name {
			if index >= 0 {
				return Source{}, errors.New("duplicate model source provider")
			}
			index = i
		}
	}
	if index < 0 {
		return Source{}, fmt.Errorf("model source provider %q is not configured", name)
	}
	p := doc.Providers[index]
	if p.Kind != "http-proxy" {
		return Source{}, errors.New("model source is not an http-proxy provider")
	}
	if len(p.Credentials) > 0 && (p.BaseURL != "" || p.Token != "" || p.TokenFile != "" || p.DummyToken != "" || p.Home != "" || p.AuthFile != "") {
		return Source{}, errors.New("model source mixes credential entries and legacy connection fields")
	}
	prefix := "/providers/" + strconv.Itoa(index)
	guards := map[string]string{prefix + "/name": p.Name, prefix + "/kind": p.Kind}
	ci := p.SelectedCredentialIndex()
	p.ApplySelected()
	cp := prefix
	if ci >= 0 {
		cp += "/credentials/" + strconv.Itoa(ci)
		if n := p.Credentials[ci].Name; n != "" {
			guards[cp+"/name"] = n
		}
	}
	if _, err := ModelURL(p.BaseURL); err != nil {
		return Source{}, err
	}
	guards[cp+"/baseUrl"] = p.BaseURL
	n := 0
	for _, value := range []string{p.Token, p.TokenFile, p.DummyToken} {
		if value != "" {
			n++
			if strings.TrimSpace(value) != value {
				return Source{}, errors.New("model source credential has surrounding whitespace")
			}
		}
	}
	if n > 1 {
		return Source{}, errors.New("model source has conflicting authentication fields")
	}
	s := Source{Path: path, Name: name, BaseURL: p.BaseURL, Protocol: p.Protocol, Reference: TokenReference{}, overrides: map[string]Reasoning{}}
	switch {
	case p.TokenFile != "":
		f, err := expandPath(p.TokenFile)
		if err != nil {
			return Source{}, errors.New("invalid model source token file")
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			return Source{}, errors.New("cannot read model source token file")
		}
		s.token = strings.TrimSpace(string(raw))
		if s.token == "" {
			return Source{}, errors.New("model source token file is empty")
		}
		s.auth = "tokenFile"
		s.Reference.File = f
	case p.Token != "":
		s.token = p.Token
		s.auth = "token"
		s.Reference = TokenReference{File: path, JSONPointer: cp + "/token", Guards: guards}
	case p.DummyToken != "":
		s.token = p.DummyToken
		s.auth = "dummyToken"
		s.Reference.PublicToken = p.DummyToken
	default:
		s.auth = "none"
	}
	for _, m := range doc.Models {
		if m.Provider != name || m.Excluded {
			continue
		}
		if p.Protocol == "" && m.Protocol != "" {
			if s.Protocol != "" && s.Protocol != m.Protocol {
				return Source{}, errors.New("model source protocol is ambiguous; configure it explicitly")
			}
			s.Protocol = m.Protocol
		}
		if m.Protocol != "openai-responses" || m.Reasoning == nil || m.Reasoning.Disabled {
			continue
		}
		r := Reasoning{State: "known", Source: "configured"}
		seen := map[string]bool{}
		for _, actual := range m.Reasoning.Mapping {
			if actual != nil && *actual != "" && !seen[*actual] {
				seen[*actual] = true
				r.Efforts = append(r.Efforts, *actual)
			}
		}
		sort.Strings(r.Efforts)
		if actual := m.Reasoning.Mapping[m.Reasoning.Default]; actual != nil {
			r.Default = *actual
		}
		if len(r.Efforts) > 0 {
			if previous, ok := s.overrides[m.Model]; ok && !equalReasoning(previous, r) {
				return Source{}, fmt.Errorf("model %q has conflicting configured reasoning", m.Model)
			}
			s.overrides[m.Model] = r
		}
	}
	if s.Protocol == "" {
		return Source{}, errors.New("model source needs an explicit protocol")
	}
	return s, nil
}
func equalReasoning(a, b Reasoning) bool {
	return a.Default == b.Default && strings.Join(a.Efforts, "\x00") == strings.Join(b.Efforts, "\x00")
}
