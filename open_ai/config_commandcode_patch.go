package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// commandCodeAccountUpdate is one credential's writable settings.
type commandCodeAccountUpdate struct {
	Name        string
	Enabled     *bool
	MaxInFlight *int
}

// commandCodeSettingsUpdate is the effect one PUT has on one provider.
type commandCodeSettingsUpdate struct {
	Provider        string
	LoadBalance     *bool
	RefreshInterval *string
	Accounts        []commandCodeAccountUpdate
}

// memberPatcher accumulates JSON edits for one document. Value replacements
// become span edits; absent members are inserted as a block after the opening
// brace so the surrounding layout survives.
type memberPatcher struct {
	edits   []jsonEdit
	inserts map[int][]string
	changed bool
}

func newMemberPatcher() *memberPatcher {
	return &memberPatcher{inserts: map[int][]string{}}
}

// set records `"key": encoded` inside object, replacing an existing value or
// scheduling an insertion.
func (p *memberPatcher) set(data []byte, object jsonSpan, key, encoded string) error {
	members, err := jsonObjectMembers(data, object)
	if err != nil {
		return err
	}
	if span, ok := jsonMemberValue(members, key); ok {
		if string(data[span.start:span.end]) == encoded {
			return nil
		}
		p.edits = append(p.edits, jsonEdit{start: span.start, end: span.end, text: []byte(encoded)})
		p.changed = true
		return nil
	}
	p.inserts[object.start+1] = append(p.inserts[object.start+1], fmt.Sprintf("%q: %s", key, encoded))
	p.changed = true
	return nil
}

// apply renders the insertions with the object's own indentation and applies
// every edit at once.
func (p *memberPatcher) apply(data []byte) []byte {
	edits := append([]jsonEdit(nil), p.edits...)
	for offset, members := range p.inserts {
		edits = append(edits, jsonEdit{start: offset, end: offset, text: memberBlock(data, offset-1, members)})
	}
	return applyJSONEdits(data, edits)
}

// memberBlock renders members placed just after an object's opening brace,
// matching its indentation (the same shape selectedInsertion produces).
func memberBlock(data []byte, objectStart int, members []string) []byte {
	rest := data[objectStart+1:]
	first := skipJSONSpace(data, objectStart+1) - (objectStart + 1)
	empty := objectStart+1+first < len(data) && data[objectStart+1+first] == '}'
	if nl := bytes.LastIndexByte(rest[:first], '\n'); nl >= 0 {
		indent := rest[nl+1 : first]
		var out []byte
		for i, member := range members {
			out = append(out, '\n')
			out = append(out, indent...)
			out = append(out, member...)
			if i < len(members)-1 || !empty {
				out = append(out, ',')
			}
		}
		return out
	}
	joined := strings.Join(members, ", ")
	if empty {
		return []byte(joined)
	}
	return []byte(joined + ", ")
}

// patchCommandCodeSettings rewrites only the members it changes: the config
// holds ~120 model entries and re-encoding the parsed struct would reorder
// every key and bury the real change in a thousand-line diff.
func patchCommandCodeSettings(data []byte, update commandCodeSettingsUpdate) ([]byte, bool, error) {
	root, err := scanJSONValue(data, 0)
	if err != nil {
		return nil, false, err
	}
	rootMembers, err := jsonObjectMembers(data, root)
	if err != nil {
		return nil, false, err
	}
	providersSpan, ok := jsonMemberValue(rootMembers, "providers")
	if !ok {
		return nil, false, fmt.Errorf("config has no providers")
	}
	providers, err := jsonArrayElements(data, providersSpan)
	if err != nil {
		return nil, false, err
	}

	patcher := newMemberPatcher()
	found := false
	for _, providerSpan := range providers {
		members, err := jsonObjectMembers(data, providerSpan)
		if err != nil {
			return nil, false, err
		}
		nameSpan, ok := jsonMemberValue(members, "name")
		if !ok {
			continue
		}
		name, err := jsonStringAt(data, nameSpan)
		if err != nil || name != update.Provider {
			continue
		}
		found = true
		if update.LoadBalance != nil {
			if err := patcher.set(data, providerSpan, "loadBalance", strconv.FormatBool(*update.LoadBalance)); err != nil {
				return nil, false, err
			}
		}
		if update.RefreshInterval != nil {
			encoded, err := jsonText(*update.RefreshInterval)
			if err != nil {
				return nil, false, err
			}
			if err := patcher.set(data, providerSpan, "refreshInterval", encoded); err != nil {
				return nil, false, err
			}
		}
		if len(update.Accounts) == 0 {
			continue
		}
		credentialsSpan, ok := jsonMemberValue(members, "credentials")
		if !ok {
			return nil, false, fmt.Errorf("provider %q has no credentials", update.Provider)
		}
		credentials, err := jsonArrayElements(data, credentialsSpan)
		if err != nil {
			return nil, false, err
		}
		for _, credentialSpan := range credentials {
			credentialMembers, err := jsonObjectMembers(data, credentialSpan)
			if err != nil {
				return nil, false, err
			}
			credentialNameSpan, ok := jsonMemberValue(credentialMembers, "name")
			if !ok {
				continue
			}
			credentialName, err := jsonStringAt(data, credentialNameSpan)
			if err != nil {
				continue
			}
			for _, account := range update.Accounts {
				if account.Name != credentialName {
					continue
				}
				if account.Enabled != nil {
					if err := patcher.set(data, credentialSpan, "enabled", strconv.FormatBool(*account.Enabled)); err != nil {
						return nil, false, err
					}
				}
				if account.MaxInFlight != nil {
					if err := patcher.set(data, credentialSpan, "maxInFlight", strconv.Itoa(*account.MaxInFlight)); err != nil {
						return nil, false, err
					}
				}
			}
		}
	}
	if !found {
		return nil, false, fmt.Errorf("config has no provider %q", update.Provider)
	}
	return patcher.apply(data), patcher.changed, nil
}

// jsonText encodes a Go string as a JSON string literal.
func jsonText(value string) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
