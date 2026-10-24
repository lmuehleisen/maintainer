package maintainer

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// TerminalSafe renders agent-written text safe to print: ANSI escape sequences
// are stripped and control characters removed, except ordinary newlines and
// tabs. Agent output is data, never terminal control sequences (including OSC
// links and clipboard writes).
func TerminalSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, ansi.Strip(s))
}

// UnsafePlan refuses approval of a stored plan containing terminal control
// characters: they could make the displayed plan differ from the plan that
// executes. It returns nil when the plan is terminal-safe.
func UnsafePlan(r Run) error {
	if TerminalSafe(r.Result.Plan) == r.Result.Plan {
		return nil
	}
	return fmt.Errorf("run %s: stored plan contains terminal control characters; review the sanitized plan with `maintain inspect %s`, then reject it with `maintain reject %s`", r.ID, r.ID, r.ID)
}
