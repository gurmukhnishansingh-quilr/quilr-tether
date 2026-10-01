package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gurmukhnishansingh-quilr/quilr-tether/internal/ojson"
)

func TestExportManagedContainsNoSecrets(t *testing.T) {
	s := newSandbox(t)
	s.addQuilr("qi", "--opus", "claude-opus-4-8", "--bedrock-backed")
	s.ok("allow", "qi", "--models", "claude-opus-4-8,claude-sonnet-4-6")
	s.ok("override", "qi", "claude-opus-4-8=team/opus")
	out := filepath.Join(s.root, "managed-settings.json")
	pl := filepath.Join(s.root, "claudecode.plist")
	r := s.ok("export-managed", "qi", "-o", out, "--enforce", "--lock-provider", "--plist", pl)

	raw, err := os.ReadFile(out)
	must(t, err)
	assertNoKey(t, string(raw), r.out, r.err)
	doc := s.readJSON(out)
	env := envOf(doc)
	for _, banned := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		if env.Has(banned) {
			t.Fatalf("%s exported", banned)
		}
	}
	if doc.Has("apiKeyHelper") {
		t.Fatal("a per-machine helper path must not be exported unless --api-key-helper is given")
	}
	if v, _ := doc.GetString("__key_delivery"); v != "use apiKeyHelper via MDM secret" {
		t.Fatal("key note missing")
	}
	if v, _ := doc.Get("allowedProviders"); !ojson.Equal(v, []any{"customEndpoint"}) {
		t.Fatal("allowedProviders")
	}
	if v, _ := doc.Get("enforceAvailableModels"); v != true {
		t.Fatal("enforceAvailableModels")
	}
	if v, _ := env.GetString("ANTHROPIC_CUSTOM_HEADERS"); strings.Contains(v, "X-User-Email") || !strings.Contains(v, "X-Provider-Label") {
		t.Fatalf("headers = %q", v)
	}
	if v, _ := env.GetString("CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS"); v != "1" {
		t.Fatal("bedrock-backed flag lost")
	}
	if !doc.Has("modelOverrides") || !doc.Has("availableModels") {
		t.Fatal("model settings missing")
	}
	plist, _ := os.ReadFile(pl)
	if !strings.Contains(string(plist), "<key>allowedProviders</key>") || strings.Contains(string(plist), "__key_delivery") {
		t.Fatalf("plist:\n%s", plist)
	}
	assertNoKey(t, string(plist))
	for _, want := range []string{"Intune", "Jamf", `C:\Program Files\ClaudeCode`, "/etc/claude-code"} {
		if !strings.Contains(r.out, want) {
			t.Errorf("notes missing %q", want)
		}
	}
}

func TestExportWithHelperAndOptions(t *testing.T) {
	s := newSandbox(t)
	s.addQuilr("qi")
	out := filepath.Join(s.root, "m.json")
	s.ok("export-managed", "qi", "-o", out, "--api-key-helper", "/usr/local/bin/quilr-key", "--keep-user-email")
	doc := s.readJSON(out)
	if v, _ := doc.GetString("apiKeyHelper"); v != "/usr/local/bin/quilr-key" || doc.Has("__key_delivery") {
		t.Fatal(jsonString(doc))
	}
	if v, _ := envOf(doc).GetString("ANTHROPIC_CUSTOM_HEADERS"); !strings.Contains(v, "X-User-Email") {
		t.Fatal("--keep-user-email ignored")
	}
	// Existing file needs confirmation.
	if r := s.run("export-managed", "qi", "-o", out); r.code != ExitUsage {
		t.Fatal("overwrite without --yes should be refused")
	}
	if r := s.run("export-managed", "qi", "-o", out, "--enforce", "--yes"); r.code != ExitUsage {
		t.Fatal("--enforce without an allowlist should fail")
	}
}

func TestExportRejections(t *testing.T) {
	s := newSandbox(t)
	s.ok("profile", "add", "br", "--type", "bedrock", "--aws-region", "us-east-1", "--aws-profile", "p")
	s.ok("profile", "add", "direct", "--type", "anthropic")
	out := filepath.Join(s.root, "m.json")
	if r := s.run("export-managed", "br", "-o", out, "--lock-provider"); r.code != ExitUsage {
		t.Fatal("lock-provider on bedrock")
	}
	if r := s.run("export-managed", "direct", "-o", out); r.code != ExitUsage {
		t.Fatal("anthropic export")
	}
	if r := s.run("export-managed", "br"); r.code != ExitUsage {
		t.Fatal("-o is required")
	}
	s.ok("export-managed", "br", "-o", out)
	if v, _ := envOf(s.readJSON(out)).GetString("CLAUDE_CODE_USE_BEDROCK"); v != "1" {
		t.Fatal("bedrock export")
	}
}

func TestAssertNoSecrets(t *testing.T) {
	newSandbox(t)
	doc := parse(t, `{"env":{"ANTHROPIC_API_KEY":"x"}}`)
	if AssertNoSecrets(doc, nil) == nil {
		t.Fatal("env key not caught")
	}
	doc = parse(t, `{"apiKeyHelper":"echo `+testKey+`"}`)
	if AssertNoSecrets(doc, []string{testKey}) == nil {
		t.Fatal("embedded key not caught")
	}
	doc = parse(t, `{"x":"sk-other-abcdefghijklmnop1234"}`)
	if AssertNoSecrets(doc, nil) == nil {
		t.Fatal("key-shaped value not caught")
	}
	if AssertNoSecrets(parse(t, `{"env":{"ANTHROPIC_BASE_URL":"https://x"}}`), []string{testKey}) != nil {
		t.Fatal("false positive")
	}
}
