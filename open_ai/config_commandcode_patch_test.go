package openai

import (
	"strings"
	"testing"
)

// commandCodePatchFixture keeps a second provider and a model entry after the
// commandcode provider: the patcher must not touch either.
const commandCodePatchFixture = `{
  "listen": "127.0.0.1:8890",
  "providers": [
    {
      "name": "commandcode",
      "kind": "commandcode",
      "credentials": [
        {
          "selected": true,
          "name": "one",
          "home": "/tmp/one"
        },
        {
          "name": "two",
          "home": "/tmp/two"
        }
      ]
    },
    {
      "name": "gateway",
      "kind": "http-proxy",
      "baseUrl": "http://127.0.0.1:9000/v1",
      "credentials": []
    }
  ],
  "models": [
    {"protocol": "anthropic-messages", "provider": "commandcode", "clientModelName": "m1"}
  ]
}
`

func TestPatchCommandCodeSettingsInsertsAndPreservesLayout(t *testing.T) {
	loadBalance := false
	maxInFlight := 2
	enabled := false
	patched, changed, err := patchCommandCodeSettings([]byte(commandCodePatchFixture), commandCodeSettingsUpdate{
		Provider:    "commandcode",
		LoadBalance: &loadBalance,
		Accounts: []commandCodeAccountUpdate{
			{Name: "one", MaxInFlight: &maxInFlight, Enabled: &enabled},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected a change")
	}

	// New members are inserted right after the opening brace, following the
	// object's own indentation (the same rule the selected-flag patcher uses).
	want := strings.Replace(commandCodePatchFixture,
		`    {
      "name": "commandcode",`,
		`    {
      "loadBalance": false,
      "name": "commandcode",`, 1)
	want = strings.Replace(want,
		`        {
          "selected": true,
          "name": "one",`,
		`        {
          "enabled": false,
          "maxInFlight": 2,
          "selected": true,
          "name": "one",`, 1)
	if string(patched) != want {
		t.Fatalf("patched bytes\n--- got ---\n%s\n--- want ---\n%s", patched, want)
	}
	// The untouched provider and the models array survive byte for byte.
	if !strings.Contains(string(patched), `"name": "gateway"`) || !strings.Contains(string(patched), `"clientModelName": "m1"`) {
		t.Fatalf("unrelated config was rewritten:\n%s", patched)
	}
}

func TestPatchCommandCodeSettingsReplacesExistingValues(t *testing.T) {
	withFields := strings.Replace(commandCodePatchFixture,
		`      "kind": "commandcode",`,
		`      "kind": "commandcode",
      "loadBalance": true,`, 1)
	withFields = strings.Replace(withFields,
		`          "name": "one",
          "home": "/tmp/one"`,
		`          "name": "one",
          "maxInFlight": 1,
          "home": "/tmp/one"`, 1)

	loadBalance := false
	maxInFlight := 4
	patched, changed, err := patchCommandCodeSettings([]byte(withFields), commandCodeSettingsUpdate{
		Provider:    "commandcode",
		LoadBalance: &loadBalance,
		Accounts:    []commandCodeAccountUpdate{{Name: "one", MaxInFlight: &maxInFlight}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected a change")
	}
	if !strings.Contains(string(patched), `"loadBalance": false`) {
		t.Fatalf("loadBalance not replaced:\n%s", patched)
	}
	if !strings.Contains(string(patched), `"maxInFlight": 4`) {
		t.Fatalf("maxInFlight not replaced:\n%s", patched)
	}
	if len(patched) != len(withFields)-len("true")+len("false")+len("1")-len("4") {
		t.Fatalf("value replacement changed the document length: %d -> %d", len(withFields), len(patched))
	}
}

func TestPatchCommandCodeSettingsNoopWhenEqual(t *testing.T) {
	loadBalance := true
	withFields := strings.Replace(commandCodePatchFixture,
		`      "kind": "commandcode",`,
		`      "kind": "commandcode",
      "loadBalance": true,`, 1)
	patched, changed, err := patchCommandCodeSettings([]byte(withFields), commandCodeSettingsUpdate{
		Provider:    "commandcode",
		LoadBalance: &loadBalance,
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed || string(patched) != withFields {
		t.Fatalf("equal settings must be a no-op (changed=%v)", changed)
	}
}

func TestPatchCommandCodeSettingsRejectsUnknownProvider(t *testing.T) {
	loadBalance := true
	if _, _, err := patchCommandCodeSettings([]byte(commandCodePatchFixture), commandCodeSettingsUpdate{
		Provider:    "missing",
		LoadBalance: &loadBalance,
	}); err == nil {
		t.Fatal("unknown provider should fail")
	}
}
