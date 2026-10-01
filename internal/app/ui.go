package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gurmukhnishansingh-quilr/quilr-tether/internal/ojson"
	"golang.org/x/term"
)

// UI is the terminal. Everything written through it is redacted.
type UI struct {
	Out, Err io.Writer
	In       io.Reader
	color    bool
	reader   *bufio.Reader
	// Interactive reports whether stdin is a terminal; tests set it false.
	Interactive bool
}

func NewUI(noColor bool) *UI {
	u := &UI{Out: os.Stdout, Err: os.Stderr, In: os.Stdin}
	u.Interactive = term.IsTerminal(int(os.Stdin.Fd()))
	u.color = !noColor && os.Getenv("NO_COLOR") == "" && term.IsTerminal(int(os.Stdout.Fd()))
	if u.color {
		enableVT()
	}
	return u
}

var colorCodes = map[string]string{"red": "31", "green": "32", "yellow": "33", "cyan": "36", "dim": "2", "bold": "1"}

func (u *UI) C(text, name string) string {
	if !u.color {
		return text
	}
	return "\033[" + colorCodes[name] + "m" + text + "\033[0m"
}

func (u *UI) Printf(format string, a ...any) { fmt.Fprint(u.Out, Redact(fmt.Sprintf(format, a...))) }
func (u *UI) Println(a ...any)               { fmt.Fprintln(u.Out, Redact(fmt.Sprint(a...))) }
func (u *UI) Errorf(format string, a ...any) { fmt.Fprint(u.Err, Redact(fmt.Sprintf(format, a...))) }
func (u *UI) Warn(format string, a ...any) {
	fmt.Fprintln(u.Err, Redact(u.C("warning: ", "yellow")+fmt.Sprintf(format, a...)))
}

// JSON prints an indented JSON document (redacted).
func (u *UI) JSON(v any) {
	var b []byte
	if o, ok := v.(*ojson.Object); ok {
		b, _ = ojson.Marshal(o, "  ")
	} else {
		var sb strings.Builder
		enc := json.NewEncoder(&sb)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		_ = enc.Encode(v)
		b = []byte(strings.TrimRight(sb.String(), "\n"))
	}
	fmt.Fprintln(u.Out, Redact(string(b)))
}

func (u *UI) readLine() (string, error) {
	if u.reader == nil {
		u.reader = bufio.NewReader(u.In)
	}
	line, err := u.reader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// Confirm asks y/N unless assumeYes. Without a terminal it refuses rather than guessing.
func (u *UI) Confirm(question string, assumeYes bool) (bool, error) {
	if assumeYes {
		return true, nil
	}
	if !u.Interactive {
		return false, usageErr("pass --yes", "confirmation required but stdin is not a terminal")
	}
	fmt.Fprintf(u.Out, "%s [y/N] ", question)
	ans, err := u.readLine()
	if err != nil {
		return false, nil
	}
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "y" || ans == "yes", nil
}

// Ask prompts for a value. Without a terminal, missing required input is a usage error.
func (u *UI) Ask(label, flag, def string, choices []string, required bool) (string, error) {
	if !u.Interactive {
		if def != "" || !required {
			return def, nil
		}
		return "", usageErr("pass it on the command line (stdin is not a terminal)", "missing %s", flag)
	}
	for {
		opts, shown := "", ""
		if len(choices) > 0 {
			opts = " (" + strings.Join(choices, "/") + ")"
		}
		if def != "" {
			shown = " [" + def + "]"
		} else if !required {
			shown = " (optional)"
		}
		fmt.Fprintf(u.Out, "%s%s%s: ", label, opts, shown)
		ans, err := u.readLine()
		if err != nil {
			return "", usageErr("", "no input for %s", flag)
		}
		ans = strings.TrimSpace(ans)
		if ans == "" {
			ans = def
		}
		if ans == "" && !required {
			return "", nil
		}
		if ans != "" && (len(choices) == 0 || contains(choices, ans)) {
			return ans, nil
		}
		fmt.Fprintln(u.Err, "please enter a valid value")
	}
}

func (u *UI) AskBool(label string, def bool) bool {
	if !u.Interactive {
		return def
	}
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	fmt.Fprintf(u.Out, "%s [%s]: ", label, hint)
	ans, err := u.readLine()
	if err != nil || strings.TrimSpace(ans) == "" {
		return def
	}
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "y" || ans == "yes"
}

// AskSecret reads without echo.
func (u *UI) AskSecret(label string) (string, error) {
	fmt.Fprint(u.Out, label)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(u.Out)
	if err != nil {
		return "", ioErr("", "cannot read key: %v", err)
	}
	return strings.TrimSpace(string(b)), nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// SettingsDiff is a unified diff of two settings objects with secrets masked.
func SettingsDiff(before, after *ojson.Object, label string) []string {
	var a []string
	if before.Len() > 0 {
		b, _ := ojson.Marshal(MaskSettings(before), "  ")
		a = strings.Split(string(b), "\n")
	}
	b, _ := ojson.Marshal(MaskSettings(after), "  ")
	bl := strings.Split(string(b), "\n")
	if before.Len() == 0 && after.Len() == 0 {
		return nil
	}
	return unifiedDiff(a, bl, label+" (current)", label+" (new)", 2)
}

func (u *UI) PrintDiff(lines []string) {
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
			u.Println(u.C(l, "bold"))
		case strings.HasPrefix(l, "+"):
			u.Println(u.C(l, "green"))
		case strings.HasPrefix(l, "-"):
			u.Println(u.C(l, "red"))
		case strings.HasPrefix(l, "@@"):
			u.Println(u.C(l, "cyan"))
		default:
			u.Println(l)
		}
	}
}

// unifiedDiff: LCS-based; settings files are small, so O(n*m) is fine.
func unifiedDiff(a, b []string, fromName, toName string, context int) []string {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type op struct {
		kind byte // ' ', '-', '+'
		text string
		ai   int
		bi   int
	}
	var ops []op
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			ops = append(ops, op{' ', a[i], i, j})
			i++
			j++
		case j < m && (i == n || lcs[i][j+1] >= lcs[i+1][j]):
			ops = append(ops, op{'+', b[j], i, j})
			j++
		default:
			ops = append(ops, op{'-', a[i], i, j})
			i++
		}
	}
	changed := false
	for _, o := range ops {
		if o.kind != ' ' {
			changed = true
			break
		}
	}
	if !changed {
		return nil
	}
	out := []string{"--- " + fromName, "+++ " + toName}
	for k := 0; k < len(ops); {
		if ops[k].kind == ' ' {
			k++
			continue
		}
		start := max(0, k-context)
		end := k
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run == len(ops) || run-end > 2*context {
				end = min(len(ops), end+context)
				break
			}
			end = run
		}
		aCount, bCount := 0, 0
		for _, o := range ops[start:end] {
			if o.kind != '+' {
				aCount++
			}
			if o.kind != '-' {
				bCount++
			}
		}
		out = append(out, fmt.Sprintf("@@ -%d,%d +%d,%d @@", ops[start].ai+1, aCount, ops[start].bi+1, bCount))
		for _, o := range ops[start:end] {
			out = append(out, string(o.kind)+o.text)
		}
		k = end
	}
	return out
}
