package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/zalando/go-keyring"
)

const keyringService = "tether"

// SecretBackend returns "keyring" (macOS Keychain, Windows Credential
// Manager, Linux Secret Service) or "file" (owner-only file fallback).
// TETHER_SECRET_BACKEND forces one.
func SecretBackend() string {
	switch os.Getenv("TETHER_SECRET_BACKEND") {
	case "keyring":
		return "keyring"
	case "file":
		return "file"
	}
	if keyringUsable() {
		return "keyring"
	}
	return "file"
}

var keyringProbe *bool

func keyringUsable() bool {
	if keyringProbe != nil {
		return *keyringProbe
	}
	// A lookup of a missing item succeeds with ErrNotFound when a keyring
	// service exists; anything else (no D-Bus, headless Linux) means unusable.
	_, err := keyring.Get(keyringService, "__quilr_probe__")
	ok := err == nil || errors.Is(err, keyring.ErrNotFound)
	keyringProbe = &ok
	return ok
}

func secretFile(name string) string { return filepath.Join(ConfigDir(), "secrets", name+".key") }

// StoreKey saves a profile's key and returns the backend used.
func StoreKey(name, key string) (string, error) {
	RegisterSecret(key)
	backend := SecretBackend()
	if backend == "keyring" {
		if err := keyring.Set(keyringService, name, key); err != nil {
			return "", ioErr("set TETHER_SECRET_BACKEND=file to use the file fallback", "cannot store key in keyring: %v", err)
		}
		return backend, nil
	}
	return backend, AtomicWrite(secretFile(name), []byte(key+"\n"), true)
}

// ReadKey returns "" when no key is stored. backend "" means the default;
// profiles record the backend their key went to, so pass that.
func ReadKey(name, backend string) (string, error) {
	if backend == "" {
		backend = SecretBackend()
	}
	var key string
	if backend == "keyring" {
		v, err := keyring.Get(keyringService, name)
		if err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return "", ioErr("", "cannot read key from keyring: %v", err)
		}
		key = v
	} else {
		b, err := os.ReadFile(secretFile(name))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", wrapIO("read", secretFile(name), err)
		}
		key = strings.TrimSpace(string(b))
	}
	RegisterSecret(key)
	return key, nil
}

func DeleteKey(name, backend string) {
	if backend == "" {
		backend = SecretBackend()
	}
	if backend == "keyring" {
		_ = keyring.Delete(keyringService, name)
		return
	}
	os.Remove(secretFile(name))
}

// RequireKey is ReadKey that fails when nothing is stored.
func RequireKey(name, backend string) (string, error) {
	key, err := ReadKey(name, backend)
	if err != nil {
		return "", err
	}
	if key == "" {
		return "", usageErr(fmt.Sprintf("store one with `tether profile set-key %s`", name),
			"no key stored for profile %q", name)
	}
	return key, nil
}

// selfPath is the tether binary to reference from apiKeyHelper. When `tether` on
// PATH is this same binary, prefer the PATH location: Homebrew and similar
// keep a stable symlink while the resolved target changes on upgrade.
var selfPath = func() string {
	exe, err := os.Executable()
	if err != nil {
		return "tether"
	}
	if onPath, err := exec.LookPath("tether"); err == nil {
		if abs, err := filepath.Abs(onPath); err == nil {
			a, e1 := os.Stat(abs)
			b, e2 := os.Stat(exe)
			if e1 == nil && e2 == nil && os.SameFile(a, b) {
				return abs
			}
		}
	}
	return exe
}

func quoteArg(arg string) string {
	if runtime.GOOS == "windows" {
		// Forward slashes work in cmd.exe, PowerShell and Git Bash alike.
		arg = filepath.ToSlash(arg)
		if strings.ContainsAny(arg, " &()^%!;,") {
			return `"` + arg + `"`
		}
		return arg
	}
	if arg != "" && !strings.ContainsAny(arg, " \t\n'\"\\$`!*?[]{}()<>|&;#~") {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// HelperCommand is the apiKeyHelper value: this binary, printing the key.
// Claude Code runs it and uses stdout as the key; the settings file never
// holds the secret.
func HelperCommand(name string) string {
	return quoteArg(selfPath()) + " key " + name
}
