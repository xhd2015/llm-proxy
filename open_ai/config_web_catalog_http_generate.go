package openai

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// generateHTTPProxyModels is deliberately independent of proxyConfig. Only the
// normalized upstream list and explicit target identity determine these bytes.
// Neither comparison statistics nor conflicts may change the returned objects.
func generateHTTPProxyModels(provider, protocol string, entries []httpProxyModelEntry) ([]catalogModelObject, error) {
	if err := validateHTTPProxyProtocol(protocol); err != nil {
		return nil, err
	}
	objects := make([]catalogModelObject, 0, len(entries))
	for _, entry := range entries {
		reasoning, err := httpProxyModelReasoning(entry)
		if err != nil {
			return nil, err
		}
		var reasoningValue any = map[string]any{"disabled": true}
		if reasoning != nil {
			reasoningValue = *reasoning
		}
		objects = append(objects, catalogObjectFromMap(map[string]any{
			"protocol": protocol, "provider": provider,
			"providerModelName": entry.ID, "clientModelName": provider + "/" + entry.ID,
			// Missing modality metadata uses the proxy's text + image default;
			// this is local policy, not a claim that inference was verified.
			"displayName": entry.ID, "inputs": []configInput{{Type: "text"}, {Type: "image"}},
			"reasoning": reasoningValue,
		}))
	}
	return objects, nil
}

type httpProxyComparison struct {
	Generated []catalogModelObject
	Stats     catalogStats
	Note      string
}

// compareHTTPProxyModels only observes generated objects. Existing aliases,
// variants, metadata, ordering, and other providers affect diagnostics, never
// generation. A conflict warns about applying a proposed name; it does not hide
// the model from the generated pane.
func compareHTTPProxyModels(doc proxyConfig, provider string, generated []catalogModelObject) (httpProxyComparison, error) {
	result := httpProxyComparison{Generated: generated, Stats: catalogStats{Total: len(generated), Visible: len(generated)}}
	byID := make(map[string]configModel, len(generated))
	key := func(model configModel) string { return model.Protocol + "\x00" + model.ProviderModelName }
	for _, object := range generated {
		var model configModel
		if err := json.Unmarshal([]byte(object.text("")), &model); err != nil {
			return httpProxyComparison{}, err
		}
		byID[key(model)] = model
	}
	type owner struct {
		provider, key string
		variant       bool
	}
	taken := map[string][]owner{}
	matched := map[string]bool{}
	for _, model := range doc.Models {
		served := model.ClientModelName
		if served == "" {
			served = model.ProviderModelName
		}
		taken[served] = append(taken[served], owner{provider: model.Provider, key: key(model)})
		for _, variant := range model.Variants {
			if variant.ClientModelName != "" {
				taken[variant.ClientModelName] = append(taken[variant.ClientModelName], owner{variant: true})
			}
		}
		if model.Provider != provider {
			continue
		}
		result.Stats.Draft++
		proposal, ok := byID[key(model)]
		if !ok {
			result.Stats.Kept++ // On-disk only, not included in Generated.
			continue
		}
		matched[key(model)] = true
		if reflect.DeepEqual(model, proposal) {
			result.Stats.Same++
		} else {
			result.Stats.Changed++
		}
	}
	for _, object := range generated {
		proposal := byID[object.string("protocol")+"\x00"+object.string("providerModelName")]
		if !matched[key(proposal)] {
			result.Stats.Added++
		}
		for _, current := range taken[proposal.ClientModelName] {
			collision := current.variant || current.provider != provider || current.key != key(proposal)
			if collision {
				result.Stats.Conflicts++
				result.Note = appendCatalogNote(result.Note, fmt.Sprintf("Generated name %q conflicts with an existing route; the generated model is still shown. Resolve the name before saving it.", proposal.ClientModelName))
				break
			}
		}
	}
	return result, nil
}

func newHTTPProxyCompareResponse(probe httpProxyProbe, result httpProxyComparison, text string) (catalogCompareResponse, error) {
	draft, err := catalogModelObjects(text, probe.Provider)
	if err != nil {
		return catalogCompareResponse{}, err
	}
	if len(draft) != result.Stats.Draft {
		return catalogCompareResponse{}, fmt.Errorf("read %d on-disk entries but counted %d", len(draft), result.Stats.Draft)
	}
	return catalogCompareResponse{
		Title: probe.Label, Provider: probe.Provider, ProviderKind: "http-proxy", Protocol: probe.Protocol,
		Source: probe.Endpoint, FetchedAt: probe.FetchedAt, Stats: result.Stats,
		Generated: catalogFragmentText(result.Generated), Draft: catalogFragmentText(draft),
		Note: httpProxyCompareNote(probe, result),
	}, nil
}

func httpProxyCompareNote(probe httpProxyProbe, result httpProxyComparison) string {
	note := fmt.Sprintf("%d routes generated only from /models, in upstream order, using the explicitly selected %s protocol. Existing aliases, fields and on-disk-only routes are not merged into the generated pane. The on-disk pane is unchanged until you edit and save it.", len(result.Generated), probe.Protocol)
	if result.Note != "" {
		note += " " + result.Note
	}
	return note + " " + strings.Join(probe.Notes, " ")
}
