package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// Ctx is shared by every command.
type Ctx struct {
	UI         *UI
	Scope      string
	ProjectDir string
	Yes        bool
	Verbose    bool
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// command describes one subcommand.
type command struct {
	name    string
	summary string
	usage   string
	run     func(c *Ctx, args []string) error
	hidden  bool
}

func commands() []command {
	return []command{
		{"profile", "add, list, show, set-key or remove profiles", "profile add|list|show|set-key|remove ...", cmdProfile, false},
		{"use", "apply a profile to a settings file (backup + diff preview)", "use <name> [--plaintext]", cmdUse, false},
		{"status", "active profile, owned keys, merged view, conflicts", "status [--json]", cmdStatus, false},
		{"diff", "show what `use` would change, without writing", "diff <name> [--plaintext]", cmdDiff, false},
		{"doctor", "live checks against the gateway", "doctor [<name>] [--json] [--timeout SECONDS]", cmdDoctor, false},
		{"models", "list gateway models and mark pinned/allowed ones", "models [<name>] [--refresh] [--json]", cmdModels, false},
		{"pin", "pin ANTHROPIC_DEFAULT_*_MODEL ids on a profile ('' unpins)", "pin <name> [--opus ID] [--sonnet ID] [--haiku ID] [--fable ID] [--clear]", cmdPin, false},
		{"allow", "set availableModels on a profile", "allow <name> --models a,b,c [--enforce] | --clear", cmdAllow, false},
		{"override", "set modelOverrides entries on a profile", "override <name> <anthropic-id>=<gateway-id> ... [--remove ID] [--clear]", cmdOverride, false},
		{"export-managed", "write a managed-settings.json for MDM rollout", "export-managed <name> -o FILE [--plist FILE] [--enforce] [--lock-provider] [--api-key-helper CMD] [--keep-user-email] [--no-key-note]", cmdExportManaged, false},
		{"restore", "restore a settings backup (default: latest for --scope)", "restore [timestamp] [--list]", cmdRestore, false},
		{"key", "print a profile's key (used by Claude Code's apiKeyHelper)", "key <name>", cmdKey, true},
		{"aws-credentials", "print a quilr-bedrock profile's key as AWS credentials (used by awsCredentialExport)", "aws-credentials <name>", cmdAWSCredentials, true},
		{"version", "print the version", "version", func(c *Ctx, _ []string) error { c.UI.Println("tether " + Version); return nil }, false},
	}
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "tether %s: tether your AI coding agents to the Quilr LLM Gateway.\nConnects Claude Code to the Quilr LLM Gateway, Anthropic or Amazon Bedrock.\n\n", Version)
	fmt.Fprintln(w, "Usage: tether <command> [options]\n\nCommands:")
	for _, c := range commands() {
		if !c.hidden {
			fmt.Fprintf(w, "  %-15s %s\n", c.name, c.summary)
		}
	}
	fmt.Fprintln(w, "\nCommon options: --scope user|project|local|managed (default user), --project-dir DIR,")
	fmt.Fprintln(w, "                --yes, --no-color, --verbose. Run `tether <command> -h` for details.")
}

// newFlagSet creates a flag set with the common options bound to c.
func newFlagSet(c *Ctx, name, usageLine string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(c.UI.Err)
	fs.StringVar(&c.Scope, "scope", "user", "settings file to act on: user, project, local or managed")
	fs.StringVar(&c.ProjectDir, "project-dir", "", "repo for project/local scope (default: git root of cwd)")
	fs.BoolVar(&c.Yes, "yes", false, "don't ask for confirmation")
	fs.BoolVar(&c.Yes, "y", false, "shorthand for --yes")
	fs.Bool("no-color", false, "disable ANSI colour")
	fs.BoolVar(&c.Verbose, "verbose", false, "verbose output (secrets are masked)")
	fs.Usage = func() {
		fmt.Fprintf(c.UI.Err, "Usage: tether %s\n\nOptions:\n", usageLine)
		fs.PrintDefaults()
	}
	return fs
}

// parseInterleaved lets flags appear before or after positional arguments.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		if rest[0] == "--" {
			return append(pos, rest[1:]...), nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

func parseArgs(c *Ctx, fs *flag.FlagSet, args []string, min, max int) ([]string, error) {
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, errHelp
		}
		return nil, &Error{Code: ExitUsage, Msg: err.Error()}
	}
	if !contains(Scopes, c.Scope) {
		return nil, usageErr("", "unknown scope %q; expected user, project, local or managed", c.Scope)
	}
	if len(pos) < min || (max >= 0 && len(pos) > max) {
		fs.Usage()
		return nil, &Error{Code: ExitUsage, Msg: "wrong number of arguments"}
	}
	return pos, nil
}

var errHelp = errors.New("help requested")

func hasNoColor(args []string) bool {
	for _, a := range args {
		if a == "--no-color" || a == "-no-color" {
			return true
		}
	}
	return false
}

// Main is the process entry point.
func Main(args []string) int {
	return Run(args, NewUI(hasNoColor(args)))
}

// Run executes a command with a given UI (tests pass buffers).
func Run(args []string, ui *UI) int {
	// A bare `tether` prints help and exits 0: package-manager validators
	// (winget) run the installed command with no arguments and treat a
	// non-zero exit as a broken install.
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		usage(ui.Out)
		return ExitOK
	}
	if args[0] == "--version" || args[0] == "-v" {
		ui.Println("tether " + Version)
		return ExitOK
	}
	var cmd *command
	for _, c := range commands() {
		if c.name == args[0] {
			cmd = &c
			break
		}
	}
	if cmd == nil {
		ui.Errorf("tether: unknown command %q\n\n", args[0])
		usage(ui.Err)
		return ExitUsage
	}
	c := &Ctx{UI: ui, Scope: "user"}
	err := cmd.run(c, args[1:])
	return report(c, err)
}

func report(c *Ctx, err error) int {
	if err == nil || errors.Is(err, errHelp) {
		return ExitOK
	}
	var e *Error
	if errors.As(err, &e) {
		if e.Code == ExitDoctorFailed && e.Msg == "" {
			return e.Code
		}
		c.UI.Errorf("%s%s\n", c.UI.C("error: ", "red"), e.Msg)
		if e.Hint != "" {
			c.UI.Errorf("hint: %s\n", e.Hint)
		}
		return e.Code
	}
	if errors.Is(err, os.ErrPermission) {
		c.UI.Errorf("%spermission denied: %v\n", c.UI.C("error: ", "red"), err)
		hint := "check file permissions"
		if c.Scope == "managed" {
			hint = adminHint()
		}
		c.UI.Errorf("hint: %s\n", hint)
		return ExitIO
	}
	c.UI.Errorf("%s%v\n", c.UI.C("error: ", "red"), err)
	return ExitIO
}

func sortedProfileNames(m map[string]*Profile) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
