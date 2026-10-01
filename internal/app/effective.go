package app

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/gurmukhnishansingh-quilr/quilr-tether/internal/ojson"
)

type ScopeState struct {
	Scope string
	Path  string
	Data  *ojson.Object // nil when missing or unreadable
	Error string
}

type Issue struct {
	Level   string `json:"level"` // fail | warn
	Message string `json:"message"`
	Fix     string `json:"fix"`
}

func LoadScopes(projectDir string) []ScopeState {
	var out []ScopeState
	for _, scope := range Scopes {
		path, _ := SettingsPath(scope, projectDir)
		st := ScopeState{Scope: scope, Path: path}
		if _, err := os.Stat(path); err == nil {
			data, err := LoadSettings(path)
			if err != nil {
				st.Error = err.Error()
			} else {
				st.Data = data
			}
		}
		out = append(out, st)
	}
	return out
}

// Sourced is a value plus the scope it came from.
type Sourced struct {
	Value any    `json:"value"`
	From  string `json:"from"`
}

// Effective is the owned keys after precedence: top-level keys from the
// highest scope that sets them, env merged key by key.
type Effective struct {
	Top     map[string]Sourced
	TopKeys []string
	Env     map[string]Sourced
	EnvKeys []string
}

func ComputeEffective(states []ScopeState) Effective {
	e := Effective{Top: map[string]Sourced{}, Env: map[string]Sourced{}}
	for _, st := range states { // lowest precedence first
		if st.Data == nil {
			continue
		}
		view := OwnedView(st.Data)
		for _, k := range view.Top.Keys() {
			v, _ := view.Top.Get(k)
			if _, seen := e.Top[k]; !seen {
				e.TopKeys = append(e.TopKeys, k)
			}
			e.Top[k] = Sourced{v, st.Scope}
		}
		env, _ := st.Data.GetObject("env")
		if env == nil {
			continue
		}
		for _, k := range env.Keys() {
			if IsOwnedEnv(k) || isForeign(k) {
				v, _ := env.Get(k)
				if _, seen := e.Env[k]; !seen {
					e.EnvKeys = append(e.EnvKeys, k)
				}
				e.Env[k] = Sourced{v, st.Scope}
			}
		}
	}
	return e
}

// ShellEnv is a seam for tests.
var ShellEnv = os.Environ

// ShellOverrides returns provider variables exported in the shell.
func ShellOverrides() map[string]string {
	out := map[string]string{}
	for _, kv := range ShellEnv() {
		k, v, _ := strings.Cut(kv, "=")
		if IsOwnedEnv(k) || isForeign(k) {
			out[k] = v
		}
	}
	return out
}

func truthy(v any) bool {
	s := strings.ToLower(strings.TrimSpace(fmt.Sprint(v)))
	return s != "" && s != "0" && s != "false" && s != "no"
}

// FindConflicts inspects what a Claude Code session would see. Settings-file
// env entries replace the shell's value in most sessions
// (code.claude.com/docs/en/env-vars), so the runtime env is the shell
// overlaid with the merged settings env.
func FindConflicts(e Effective) []Issue {
	runtimeEnv := map[string]Sourced{}
	for k, v := range ShellOverrides() {
		runtimeEnv[k] = Sourced{v, "shell"}
	}
	for k, v := range e.Env {
		runtimeEnv[k] = v
	}
	where := func(k string) string {
		if runtimeEnv[k].From == "shell" {
			return "shell environment"
		}
		return runtimeEnv[k].From + " settings"
	}
	var issues []Issue
	base, hasBase := runtimeEnv["ANTHROPIC_BASE_URL"]
	var providers []string
	for _, k := range []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_USE_MANTLE"} {
		if v, ok := runtimeEnv[k]; ok && truthy(v.Value) {
			providers = append(providers, k)
		}
	}
	if hasBase && len(providers) > 0 {
		p := providers[0]
		issues = append(issues, Issue{"fail",
			fmt.Sprintf("%s (%s) is set alongside ANTHROPIC_BASE_URL (%s): Claude Code routes to the cloud provider and gateway model discovery never runs",
				p, runtimeEnv[p].From, runtimeEnv["ANTHROPIC_BASE_URL"].From),
			"remove " + p + " from the " + where(p)})
	}
	if len(providers) > 1 {
		issues = append(issues, Issue{"fail", "several provider switches are on: " + strings.Join(providers, ", "),
			"keep exactly one CLAUDE_CODE_USE_* variable"})
	}
	if _, ok := runtimeEnv["ANTHROPIC_AUTH_TOKEN"]; ok && hasBase && strings.Contains(fmt.Sprint(base.Value), "quilr.ai") {
		issues = append(issues, Issue{"fail",
			fmt.Sprintf("ANTHROPIC_AUTH_TOKEN (%s) is set but Quilr authenticates with x-api-key", runtimeEnv["ANTHROPIC_AUTH_TOKEN"].From),
			"remove ANTHROPIC_AUTH_TOKEN; tether uses apiKeyHelper or ANTHROPIC_API_KEY"})
	}
	if k, ok := runtimeEnv["ANTHROPIC_API_KEY"]; ok {
		if h, ok := e.Top["apiKeyHelper"]; ok {
			issues = append(issues, Issue{"warn",
				fmt.Sprintf("both ANTHROPIC_API_KEY (%s) and apiKeyHelper (%s) are set", k.From, h.From),
				"keep one credential source; re-run `tether use <profile>`"})
		}
	}
	if _, ok := e.Top["awsAuthRefresh"]; ok && !contains(providers, "CLAUDE_CODE_USE_BEDROCK") {
		issues = append(issues, Issue{"warn", "awsAuthRefresh is set but Bedrock is not enabled", "re-run `tether use <profile>`"})
	}
	return issues
}

// ShellShadowing reports provider variables exported in the shell.
func ShellShadowing(e Effective) []Issue {
	shell := ShellOverrides()
	keys := make([]string, 0, len(shell))
	for k := range shell {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var issues []Issue
	for _, k := range keys {
		if src, ok := e.Env[k]; ok {
			issues = append(issues, Issue{"warn",
				fmt.Sprintf("%s is exported in your shell; the %s settings value replaces it in most sessions, but tools that read the shell directly still see the shell value", k, src.From),
				"unset " + k + " in your shell profile"})
			continue
		}
		level := "warn"
		if strings.HasPrefix(k, "CLAUDE_CODE_USE_") && truthy(shell[k]) {
			level = "fail"
		}
		issues = append(issues, Issue{level,
			fmt.Sprintf("%s is exported in your shell and no settings file sets it, so it applies to every session", k),
			"unset " + k + " in your shell profile (or add it to a profile)"})
	}
	return issues
}

// Outranking reports owned keys set, with a different value, by a scope that
// outranks target.
func Outranking(states []ScopeState, target string) []Issue {
	idx := 0
	var tgt *ojson.Object
	for i, st := range states {
		if st.Scope == target {
			idx, tgt = i, st.Data
		}
	}
	if tgt == nil {
		tgt = ojson.NewObject()
	}
	tv := OwnedView(tgt)
	var issues []Issue
	for _, st := range states[idx+1:] {
		if st.Data == nil {
			continue
		}
		v := OwnedView(st.Data)
		var keys []string
		for _, k := range v.Top.Keys() {
			a, _ := v.Top.Get(k)
			b, ok := tv.Top.Get(k)
			if !ok || !ojson.Equal(a, b) {
				keys = append(keys, k)
			}
		}
		for _, k := range v.Env.Keys() {
			a, _ := v.Env.Get(k)
			b, ok := tv.Env.Get(k)
			if !ok || !ojson.Equal(a, b) {
				keys = append(keys, "env."+k)
			}
		}
		if len(keys) > 0 {
			issues = append(issues, Issue{"warn",
				fmt.Sprintf("%s settings (%s) outrank %s and set: %s", st.Scope, st.Path, target, strings.Join(keys, ", ")),
				fmt.Sprintf("remove them from %s, or apply the profile with --scope %s", st.Path, st.Scope)})
		}
	}
	return issues
}
