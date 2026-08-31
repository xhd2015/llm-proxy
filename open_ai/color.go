package openai

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

// ColorMode selects when ANSI color is emitted, per the three-mode policy
// (auto / always / never). See go-best-practice cli/color.
type ColorMode int

const (
	// ColorAuto enables color on a TTY unless NO_COLOR is set (default).
	ColorAuto ColorMode = iota
	// ColorAlways forces color on (--color).
	ColorAlways
	// ColorNever forces color off (--no-color).
	ColorNever
)

const grayPrefix = "\033[90m"
const colorReset = "\033[0m"

// colorModeFromFlags resolves the mode from the --color / --no-color flags,
// each a **bool so "not provided" is distinguishable from "provided". It
// returns an error when both are set.
func colorModeFromFlags(color, noColor *bool) (ColorMode, error) {
	isSet := func(p *bool) bool { return p != nil && *p }
	if isSet(color) && isSet(noColor) {
		return ColorAuto, fmt.Errorf("--color and --no-color cannot be specified together")
	}
	if isSet(color) {
		return ColorAlways, nil
	}
	if isSet(noColor) {
		return ColorNever, nil
	}
	return ColorAuto, nil
}

// resolveColor reports whether ANSI escapes should be emitted for stderr.
// noColorEnv is os.Getenv("NO_COLOR"); it applies only in auto mode.
func resolveColor(mode ColorMode, stderrIsTTY bool, noColorEnv string) bool {
	switch mode {
	case ColorAlways:
		return true
	case ColorNever:
		return false
	}
	if noColorEnv != "" {
		return false
	}
	return stderrIsTTY
}

// stderrColorEnabled resolves color for the real stderr fd.
func stderrColorEnabled(mode ColorMode) bool {
	return resolveColor(mode, term.IsTerminal(int(os.Stderr.Fd())), os.Getenv("NO_COLOR"))
}

// grayNotice formats a "notice:" line for stderr, graying the prefix when
// color is enabled. The message itself is uncolored.
func grayNotice(colorEnabled bool, format string, args ...any) string {
	msg := fmt.Sprintf(format, args...)
	if !colorEnabled {
		return "notice: " + msg
	}
	return grayPrefix + "notice:" + colorReset + " " + msg
}
