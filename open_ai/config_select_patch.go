package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// Switching accounts rewrites the user's own config file, which is hand
// formatted and holds a hundred-odd model entries. Re-encoding the parsed
// struct would reorder every key, drop the layout, and bury the real change in
// a thousand-line diff, so the switch edits only the "selected" literals of the
// chosen provider's credentials and copies every other byte through.

var errConfigJSONShape = errors.New("config file is not valid JSON")

type jsonSpan struct{ start, end int }

type jsonMember struct {
	key   string
	value jsonSpan
}

func skipJSONSpace(data []byte, i int) int {
	for i < len(data) {
		switch data[i] {
		case ' ', '\t', '\r', '\n':
			i++
		default:
			return i
		}
	}
	return i
}

func isJSONValueDelimiter(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', ',', '}', ']':
		return true
	}
	return false
}

// scanJSONString returns the offset just past the closing quote of the string
// that starts at i.
func scanJSONString(data []byte, i int) (int, error) {
	for j := i + 1; j < len(data); j++ {
		switch data[j] {
		case '\\':
			j++
		case '"':
			return j + 1, nil
		}
	}
	return 0, errConfigJSONShape
}

// scanJSONValue returns the span of the value that starts at or after i.
func scanJSONValue(data []byte, i int) (jsonSpan, error) {
	i = skipJSONSpace(data, i)
	if i >= len(data) {
		return jsonSpan{}, errConfigJSONShape
	}
	switch data[i] {
	case '{', '[':
		depth := 0
		for j := i; j < len(data); j++ {
			switch data[j] {
			case '"':
				next, err := scanJSONString(data, j)
				if err != nil {
					return jsonSpan{}, err
				}
				j = next - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return jsonSpan{start: i, end: j + 1}, nil
				}
				if depth < 0 {
					return jsonSpan{}, errConfigJSONShape
				}
			}
		}
		return jsonSpan{}, errConfigJSONShape
	case '"':
		end, err := scanJSONString(data, i)
		if err != nil {
			return jsonSpan{}, err
		}
		return jsonSpan{start: i, end: end}, nil
	default:
		j := i
		for j < len(data) && !isJSONValueDelimiter(data[j]) {
			j++
		}
		if j == i {
			return jsonSpan{}, errConfigJSONShape
		}
		return jsonSpan{start: i, end: j}, nil
	}
}

func jsonObjectMembers(data []byte, object jsonSpan) ([]jsonMember, error) {
	if object.start >= len(data) || data[object.start] != '{' {
		return nil, errConfigJSONShape
	}
	i := object.start + 1
	var members []jsonMember
	for {
		i = skipJSONSpace(data, i)
		if i >= object.end {
			return nil, errConfigJSONShape
		}
		if data[i] == '}' {
			return members, nil
		}
		if data[i] != '"' {
			return nil, errConfigJSONShape
		}
		keyEnd, err := scanJSONString(data, i)
		if err != nil {
			return nil, err
		}
		var key string
		if err := json.Unmarshal(data[i:keyEnd], &key); err != nil {
			return nil, errConfigJSONShape
		}
		i = skipJSONSpace(data, keyEnd)
		if i >= object.end || data[i] != ':' {
			return nil, errConfigJSONShape
		}
		value, err := scanJSONValue(data, i+1)
		if err != nil {
			return nil, err
		}
		members = append(members, jsonMember{key: key, value: value})
		i = skipJSONSpace(data, value.end)
		if i < object.end && data[i] == ',' {
			i++
			continue
		}
	}
}

func jsonArrayElements(data []byte, array jsonSpan) ([]jsonSpan, error) {
	if array.start >= len(data) || data[array.start] != '[' {
		return nil, errConfigJSONShape
	}
	i := array.start + 1
	var elements []jsonSpan
	for {
		i = skipJSONSpace(data, i)
		if i >= array.end {
			return nil, errConfigJSONShape
		}
		if data[i] == ']' {
			return elements, nil
		}
		value, err := scanJSONValue(data, i)
		if err != nil {
			return nil, err
		}
		elements = append(elements, value)
		i = skipJSONSpace(data, value.end)
		if i < array.end && data[i] == ',' {
			i++
			continue
		}
	}
}

func jsonMemberValue(members []jsonMember, key string) (jsonSpan, bool) {
	for _, member := range members {
		if member.key == key {
			return member.value, true
		}
	}
	return jsonSpan{}, false
}

func jsonStringAt(data []byte, span jsonSpan) (string, error) {
	var value string
	if err := json.Unmarshal(data[span.start:span.end], &value); err != nil {
		return "", errConfigJSONShape
	}
	return value, nil
}

func jsonSpanIsTrue(data []byte, span jsonSpan) bool {
	return bytes.Equal(bytes.TrimSpace(data[span.start:span.end]), []byte("true"))
}

type jsonEdit struct {
	start int
	end   int
	text  []byte
}

func applyJSONEdits(data []byte, edits []jsonEdit) []byte {
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	out := append([]byte(nil), data...)
	for _, edit := range edits {
		next := make([]byte, 0, len(out)+len(edit.text))
		next = append(next, out[:edit.start]...)
		next = append(next, edit.text...)
		next = append(next, out[edit.end:]...)
		out = next
	}
	return out
}

// selectedInsertion renders a new "selected": true member that follows the
// object's existing layout: a fresh line at the same indentation when the
// object is multiline, an inline member when it is not.
func selectedInsertion(data []byte, objectStart int) []byte {
	const member = `"selected": true`
	rest := data[objectStart+1:]
	i := skipJSONSpace(data, objectStart+1) - (objectStart + 1)
	empty := objectStart+1+i < len(data) && data[objectStart+1+i] == '}'
	if nl := bytes.LastIndexByte(rest[:i], '\n'); nl >= 0 {
		indent := rest[nl+1 : i]
		out := make([]byte, 0, len(indent)+len(member)+3)
		out = append(out, '\n')
		out = append(out, indent...)
		out = append(out, member...)
		if !empty {
			out = append(out, ',')
		}
		return out
	}
	if empty {
		return []byte(member)
	}
	return []byte(member + ", ")
}

// patchCredentialSelection flips the "selected" flags of one provider's
// credentials so that only the named credential is selected. It returns the
// rewritten document and whether any byte changed.
func patchCredentialSelection(data []byte, providerName, credentialName string) ([]byte, bool, error) {
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
	var credentials []jsonSpan
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
		if err != nil {
			return nil, false, err
		}
		if name != providerName {
			continue
		}
		found = true
		credentialsSpan, ok := jsonMemberValue(members, "credentials")
		if !ok {
			return nil, false, fmt.Errorf("provider %q defines no credentials", providerName)
		}
		credentials, err = jsonArrayElements(data, credentialsSpan)
		if err != nil {
			return nil, false, err
		}
		break
	}
	if !found {
		return nil, false, fmt.Errorf("config has no provider %q", providerName)
	}
	if len(credentials) == 0 {
		return nil, false, fmt.Errorf("provider %q defines no credentials", providerName)
	}

	selected := make([]bool, len(credentials))
	var edits []jsonEdit
	target := -1
	for i, credentialSpan := range credentials {
		members, err := jsonObjectMembers(data, credentialSpan)
		if err != nil {
			return nil, false, err
		}
		nameSpan, hasName := jsonMemberValue(members, "name")
		name := ""
		if hasName {
			if name, err = jsonStringAt(data, nameSpan); err != nil {
				return nil, false, err
			}
		}
		selectedSpan, hasSelected := jsonMemberValue(members, "selected")
		selected[i] = hasSelected && jsonSpanIsTrue(data, selectedSpan)
		if name != credentialName {
			if hasSelected && selected[i] {
				edits = append(edits, jsonEdit{start: selectedSpan.start, end: selectedSpan.end, text: []byte("false")})
			}
			continue
		}
		if target >= 0 {
			return nil, false, fmt.Errorf("provider %q has duplicate credential %q", providerName, credentialName)
		}
		target = i
		if hasSelected {
			if !selected[i] {
				edits = append(edits, jsonEdit{start: selectedSpan.start, end: selectedSpan.end, text: []byte("true")})
			}
			continue
		}
		edits = append(edits, jsonEdit{start: credentialSpan.start + 1, end: credentialSpan.start + 1, text: selectedInsertion(data, credentialSpan.start)})
	}
	if target < 0 {
		return nil, false, fmt.Errorf("provider %q has no credential %q", providerName, credentialName)
	}
	if len(edits) == 0 {
		return data, false, nil
	}
	return applyJSONEdits(data, edits), true, nil
}
