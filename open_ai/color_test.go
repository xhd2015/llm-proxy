package openai

import (
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func TestColorModeFromFlags(t *testing.T) {
	tests := []struct {
		name    string
		color   *bool
		noColor *bool
		want    ColorMode
		wantErr bool
	}{
		{name: "neither set", color: nil, noColor: nil, want: ColorAuto},
		{name: "color set", color: boolPtr(true), noColor: nil, want: ColorAlways},
		{name: "no-color set", color: nil, noColor: boolPtr(true), want: ColorNever},
		{name: "color explicitly false", color: boolPtr(false), noColor: nil, want: ColorAuto},
		{name: "both set", color: boolPtr(true), noColor: boolPtr(true), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := colorModeFromFlags(tt.color, tt.noColor)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if err.Error() != "--color and --no-color cannot be specified together" {
					t.Fatalf("error = %q", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("mode = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolveColor(t *testing.T) {
	tests := []struct {
		name       string
		mode       ColorMode
		isTTY      bool
		noColorEnv string
		want       bool
	}{
		{name: "auto tty no env", mode: ColorAuto, isTTY: true, noColorEnv: "", want: true},
		{name: "auto tty NO_COLOR set", mode: ColorAuto, isTTY: true, noColorEnv: "1", want: false},
		{name: "auto non-tty", mode: ColorAuto, isTTY: false, noColorEnv: "", want: false},
		{name: "always overrides non-tty", mode: ColorAlways, isTTY: false, noColorEnv: "", want: true},
		{name: "always overrides NO_COLOR", mode: ColorAlways, isTTY: false, noColorEnv: "1", want: true},
		{name: "never overrides tty", mode: ColorNever, isTTY: true, noColorEnv: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveColor(tt.mode, tt.isTTY, tt.noColorEnv); got != tt.want {
				t.Fatalf("resolveColor(%v, %v, %q) = %v, want %v", tt.mode, tt.isTTY, tt.noColorEnv, got, tt.want)
			}
		})
	}
}

func TestGrayNotice(t *testing.T) {
	plain := grayNotice(false, "full proxy log: %s", "/tmp/x.log")
	if plain != "notice: full proxy log: /tmp/x.log" {
		t.Fatalf("plain = %q", plain)
	}
	colored := grayNotice(true, "full proxy log: %s", "/tmp/x.log")
	want := "\033[90mnotice:\033[0m full proxy log: /tmp/x.log"
	if colored != want {
		t.Fatalf("colored = %q, want %q", colored, want)
	}
}
