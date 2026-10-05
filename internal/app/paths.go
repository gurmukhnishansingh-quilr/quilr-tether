package app

import (
	"os"
	"path/filepath"
	"runtime"
)

// Scopes, lowest to highest precedence. `claude --settings` sits between
// local and managed but is per-invocation, so tether never sees it.
var Scopes = []string{"user", "project", "local", "managed"}

// Verified against code.claude.com/docs/en/managed-settings (2026-10).
// Claude Code no longer reads the legacy C:\ProgramData\ClaudeCode path.
var managedDirs = map[string]string{
	"darwin":  "/Library/Application Support/ClaudeCode",
	"windows": `C:\Program Files\ClaudeCode`,
	"linux":   "/etc/claude-code",
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}

// ClaudeHome is ~/.claude, or $CLAUDE_CONFIG_DIR when set (as Claude Code does).
func ClaudeHome() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(homeDir(), ".claude")
}

// ManagedDir honours TETHER_MANAGED_DIR so tests and dry runs never touch system paths.
func ManagedDir() string {
	if d := os.Getenv("TETHER_MANAGED_DIR"); d != "" {
		return d
	}
	if d, ok := managedDirs[runtime.GOOS]; ok {
		return d
	}
	return managedDirs["linux"]
}

// ConfigDir holds profiles.toml, stored keys (file backend) and the model cache.
func ConfigDir() string {
	if d := os.Getenv("TETHER_CONFIG_DIR"); d != "" {
		return d
	}
	if runtime.GOOS == "windows" {
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "tether")
		}
		return filepath.Join(homeDir(), "AppData", "Roaming", "tether")
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "tether")
	}
	return filepath.Join(homeDir(), ".config", "tether")
}

// ClaudeJSONPath is Claude Code's global config, which holds user-scope MCP
// servers (top-level mcpServers): $CLAUDE_CONFIG_DIR/.claude.json when set,
// else ~/.claude.json (next to ~/.claude, not inside it).
func ClaudeJSONPath() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, ".claude.json")
	}
	return filepath.Join(homeDir(), ".claude.json")
}

func BackupsDir() string { return filepath.Join(ClaudeHome(), "backups") }

// FindProjectRoot returns the nearest ancestor containing .git, else start.
func FindProjectRoot(start string) string {
	if start == "" {
		start, _ = os.Getwd()
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		return start
	}
	for dir := abs; ; {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs
		}
		dir = parent
	}
}

// SettingsPath maps a scope to its file.
func SettingsPath(scope, projectDir string) (string, error) {
	switch scope {
	case "user":
		return filepath.Join(ClaudeHome(), "settings.json"), nil
	case "project":
		return filepath.Join(FindProjectRoot(projectDir), ".claude", "settings.json"), nil
	case "local":
		return filepath.Join(FindProjectRoot(projectDir), ".claude", "settings.local.json"), nil
	case "managed":
		return filepath.Join(ManagedDir(), "managed-settings.json"), nil
	}
	return "", usageErr("", "unknown scope %q; expected user, project, local or managed", scope)
}

func adminHint() string {
	if runtime.GOOS == "windows" {
		return "re-run from an elevated shell (Run as administrator)"
	}
	return "re-run with sudo, e.g. `sudo tether ...`"
}

func isManagedPath(path string) bool {
	a, err1 := filepath.Abs(filepath.Dir(path))
	b, err2 := filepath.Abs(ManagedDir())
	return err1 == nil && err2 == nil && a == b
}
