package app

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gurmukhnishansingh-quilr/quilr-tether/internal/ojson"
)

const userSettings = `{
  "permissions": {"allow": ["Bash(npm test)"]},
  "env": {"MY_VAR": "keep-me"},
  "hooks": {"Stop": []},
  "mcpServers": {"db": {"command": "db-mcp"}}
}`

func (s *sandbox) seedUser() string {
	p := s.userPath()
	s.writeJSON(p, userSettings)
	return p
}

func assertNoKey(t *testing.T, texts ...string) {
	t.Helper()
	for _, x := range texts {
		if strings.Contains(x, testKey) {
			t.Fatalf("plaintext key printed:\n%s", x)
		}
	}
}

func TestProfileAddListShowRemove(t *testing.T) {
	s := newSandbox(t)
	s.addQuilr("qi")
	r := s.ok("profile", "list")
	if !strings.Contains(r.out, "qi") || !strings.Contains(r.out, "guardrails-india-1") {
		t.Fatal(r.out)
	}
	r = s.ok("profile", "show", "qi", "--json")
	var shown map[string]any
	must(t, json.Unmarshal([]byte(r.out), &shown))
	if !strings.HasPrefix(shown["key"].(string), "sk-quilr-…ABCD") {
		t.Fatalf("key = %v", shown["key"])
	}
	assertNoKey(t, r.out, r.err)
	s.ok("profile", "remove", "qi", "--yes")
	if all, _ := LoadProfiles(); len(all) != 0 {
		t.Fatal("not removed")
	}
	if k, _ := ReadKey("qi", "file"); k != "" {
		t.Fatal("key not deleted")
	}
}

func TestProfileAddKeyFromStdin(t *testing.T) {
	s := newSandbox(t)
	r := s.runStdin(testKey+"\n", "profile", "add", "q2", "--type", "quilr", "--region", "jp-1", "--key-stdin")
	if r.code != 0 {
		t.Fatal(r.err)
	}
	assertNoKey(t, r.out, r.err)
	if k, _ := ReadKey("q2", "file"); k != testKey {
		t.Fatal("stdin key not stored")
	}
}

func TestProfileAddMissingOptionsNonInteractive(t *testing.T) {
	s := newSandbox(t)
	r := s.run("profile", "add", "q3", "--type", "quilr", "--region", "auto")
	if r.code != ExitUsage || !strings.Contains(r.err, "key") {
		t.Fatalf("%d %s", r.code, r.err)
	}
	r = s.run("profile", "add", "q4")
	if r.code != ExitUsage || !strings.Contains(r.err, "--type") {
		t.Fatalf("%d %s", r.code, r.err)
	}
}

func TestProfileAddDuplicateNeedsForce(t *testing.T) {
	s := newSandbox(t)
	s.addQuilr("qi")
	r := s.run("profile", "add", "qi", "--type", "anthropic")
	if r.code != ExitUsage || !strings.Contains(r.err, "--force") {
		t.Fatal(r.err)
	}
	// --force without a new key keeps the stored key.
	s.ok("profile", "add", "qi", "--type", "quilr", "--region", "usa-1", "--force", "--no-discovery")
	if k, _ := ReadKey("qi", "file"); k != testKey {
		t.Fatal("key lost on --force")
	}
}

func TestUseWritesHelperAndPreservesEverything(t *testing.T) {
	s := newSandbox(t)
	path := s.seedUser()
	s.addQuilr("qi", "--sonnet", "claude-sonnet-4-6")
	r := s.ok("use", "qi", "--yes")
	assertNoKey(t, r.out, r.err)
	got := s.readJSON(path)
	orig := parse(t, userSettings)
	for _, k := range []string{"permissions", "hooks", "mcpServers"} {
		a, _ := orig.Get(k)
		b, _ := got.Get(k)
		if !ojson.Equal(a, b) {
			t.Errorf("%s changed", k)
		}
	}
	if strings.Join(got.Keys()[:4], ",") != "permissions,env,hooks,mcpServers" {
		t.Fatalf("order: %v", got.Keys())
	}
	env := envOf(got)
	checks := map[string]string{
		"MY_VAR":                   "keep-me",
		"ANTHROPIC_BASE_URL":       "https://guardrails-india-1.quilr.ai/anthropic_messages",
		"ANTHROPIC_CUSTOM_HEADERS": "X-User-Email: dev@example.com\nX-Provider-Label: claude-code",
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1",
		"ANTHROPIC_DEFAULT_SONNET_MODEL":             "claude-sonnet-4-6",
	}
	for k, want := range checks {
		if v, _ := env.GetString(k); v != want {
			t.Errorf("env.%s = %q, want %q", k, v, want)
		}
	}
	if env.Has("ANTHROPIC_API_KEY") || env.Has("ANTHROPIC_AUTH_TOKEN") {
		t.Fatal("credential in env")
	}
	if h, _ := got.GetString("apiKeyHelper"); !strings.HasSuffix(h, " key qi") {
		t.Fatalf("apiKeyHelper = %q", h)
	}
	raw, _ := os.ReadFile(path)
	assertNoKey(t, string(raw))
	if len(ListBackups()) != 1 {
		t.Fatal("expected one backup")
	}
	if !strings.Contains(r.out, "+") {
		t.Fatal("diff preview missing")
	}
}

func TestKeyCommandPrintsKeyForHelper(t *testing.T) {
	s := newSandbox(t)
	s.addQuilr("qi")
	r := s.ok("key", "qi")
	if r.out != testKey {
		t.Fatalf("got %q", r.out)
	}
}

func TestUseRequiresConfirmationWithoutYes(t *testing.T) {
	s := newSandbox(t)
	path := s.seedUser()
	s.addQuilr("qi")
	r := s.run("use", "qi")
	if r.code != ExitUsage || !strings.Contains(r.err, "--yes") {
		t.Fatalf("%d %s", r.code, r.err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != userSettings {
		t.Fatal("file changed without confirmation")
	}
}

func TestSwitchClearsStaleProviderKeys(t *testing.T) {
	s := newSandbox(t)
	path := s.seedUser()
	s.addQuilr("qi", "--bedrock-backed")
	s.ok("profile", "add", "br", "--type", "bedrock", "--aws-region", "us-east-1", "--aws-profile", "dev",
		"--sso-refresh", "--opus", "us.anthropic.claude-opus-4-8")
	s.ok("profile", "add", "direct", "--type", "anthropic")
	s.ok("allow", "qi", "--models", "claude-sonnet-4-6")

	s.ok("use", "br", "--yes")
	got := s.readJSON(path)
	if v, _ := envOf(got).GetString("CLAUDE_CODE_USE_BEDROCK"); v != "1" {
		t.Fatal("bedrock not applied")
	}

	s.ok("use", "qi", "--yes")
	got = s.readJSON(path)
	for _, stale := range []string{"CLAUDE_CODE_USE_BEDROCK", "AWS_REGION", "AWS_PROFILE", "ANTHROPIC_DEFAULT_OPUS_MODEL"} {
		if envOf(got).Has(stale) {
			t.Errorf("stale %s survived the switch", stale)
		}
	}
	if got.Has("awsAuthRefresh") {
		t.Error("stale awsAuthRefresh")
	}
	if v, _ := got.Get("availableModels"); !ojson.Equal(v, []any{"claude-sonnet-4-6"}) {
		t.Errorf("availableModels = %v", v)
	}

	s.ok("use", "direct", "--yes")
	if !ojson.Equal(s.readJSON(path), parse(t, userSettings)) {
		t.Fatalf("switching to anthropic should restore the user's own keys exactly:\n%s", jsonString(s.readJSON(path)))
	}
}

func TestPlaintextRefusedForProjectAndManaged(t *testing.T) {
	s := newSandbox(t)
	s.addQuilr("qi")
	r := s.run("use", "qi", "--scope", "project", "--plaintext", "--yes")
	if r.code != ExitUsage || !strings.Contains(r.err, "committed to git") {
		t.Fatalf("%d %s", r.code, r.err)
	}
	p, _ := SettingsPath("project", "")
	if _, err := os.Stat(p); err == nil {
		t.Fatal("project file written")
	}
	if r := s.run("use", "qi", "--scope", "managed", "--plaintext", "--yes"); r.code != ExitUsage {
		t.Fatal("managed plaintext not refused")
	}
}

func TestPlaintextUserScopeNeverPrintsKey(t *testing.T) {
	s := newSandbox(t)
	path := s.seedUser()
	s.addQuilr("qi")
	r := s.ok("use", "qi", "--plaintext", "--yes")
	got := s.readJSON(path)
	if v, _ := envOf(got).GetString("ANTHROPIC_API_KEY"); v != testKey || got.Has("apiKeyHelper") {
		t.Fatal("plaintext not written")
	}
	assertNoKey(t, r.out, r.err)
	if !strings.Contains(r.out, "sk-quilr-…ABCD") {
		t.Fatal("masked key not shown in diff")
	}
	r = s.ok("status", "--json")
	assertNoKey(t, r.out)
	var st map[string]any
	must(t, json.Unmarshal([]byte(r.out), &st))
	if st["active_profile"] != "qi" {
		t.Fatalf("active = %v", st["active_profile"])
	}
	r = s.ok("status")
	assertNoKey(t, r.out)
}

func TestLocalScopeWithProjectDir(t *testing.T) {
	s := newSandbox(t)
	s.addQuilr("qi")
	s.ok("use", "qi", "--scope", "local", "--yes", "--project-dir", s.repo)
	if _, err := os.Stat(filepath.Join(s.repo, ".claude", "settings.local.json")); err != nil {
		t.Fatal(err)
	}
}

func TestDiffDoesNotWrite(t *testing.T) {
	s := newSandbox(t)
	path := s.seedUser()
	s.addQuilr("qi")
	r := s.ok("diff", "qi")
	if !strings.Contains(r.out, "+") || !strings.Contains(r.out, "ANTHROPIC_BASE_URL") {
		t.Fatal(r.out)
	}
	if raw, _ := os.ReadFile(path); string(raw) != userSettings || len(ListBackups()) != 0 {
		t.Fatal("diff wrote something")
	}
}

func TestBackupRestoreRoundTrip(t *testing.T) {
	s := newSandbox(t)
	path := s.seedUser()
	s.addQuilr("qi")
	s.ok("use", "qi", "--yes")
	s.ok("restore", "--yes")
	if raw, _ := os.ReadFile(path); string(raw) != userSettings {
		t.Fatalf("restore not byte-identical:\n%s", raw)
	}
	backups := ListBackups()
	if len(backups) != 2 {
		t.Fatalf("restore should itself be backed up; have %d", len(backups))
	}
	s.ok("restore", backups[1].Timestamp, "--yes")
	if v, _ := envOf(s.readJSON(path)).GetString("ANTHROPIC_BASE_URL"); !strings.Contains(v, "india-1") {
		t.Fatal("undo of restore failed")
	}
	if r := s.ok("restore", "--list"); !strings.Contains(r.out, backups[0].Timestamp) {
		t.Fatal(r.out)
	}
}

func TestRestoreOfMissingFileRemovesIt(t *testing.T) {
	s := newSandbox(t)
	s.addQuilr("qi")
	s.ok("use", "qi", "--yes")
	s.ok("restore", "--yes")
	if _, err := os.Stat(s.userPath()); err == nil {
		t.Fatal("file should be removed")
	}
	if r := s.run("restore", "19990101"); r.code != ExitUsage {
		t.Fatal("unknown timestamp should be a usage error")
	}
}

func TestStatusReportsConflicts(t *testing.T) {
	s := newSandbox(t)
	s.seedUser()
	s.addQuilr("qi")
	s.ok("use", "qi", "--yes")
	s.env = []string{"CLAUDE_CODE_USE_BEDROCK=1", "PATH=/bin"}
	proj, _ := SettingsPath("project", "")
	s.writeJSON(proj, `{"model": "opus"}`)
	r := s.ok("status", "--json")
	var st struct {
		Active    string                     `json:"active_profile"`
		Effective map[string]json.RawMessage `json:"effective"`
		Issues    []Issue                    `json:"issues"`
	}
	must(t, json.Unmarshal([]byte(r.out), &st))
	if st.Active != "qi" {
		t.Fatal("active")
	}
	if string(st.Effective["model"]) != `{"value":"opus","from":"project"}` && !strings.Contains(string(st.Effective["model"]), `"project"`) {
		t.Fatalf("effective model %s", st.Effective["model"])
	}
	var msgs []string
	for _, i := range st.Issues {
		msgs = append(msgs, i.Message)
	}
	all := strings.Join(msgs, " | ")
	if !strings.Contains(all, "CLAUDE_CODE_USE_BEDROCK (shell)") || !strings.Contains(all, "project settings") {
		t.Fatalf("issues: %s", all)
	}
	if r := s.ok("status"); !strings.Contains(r.out, "FAIL") {
		t.Fatal(r.out)
	}
}

func TestStatusOnInvalidJSON(t *testing.T) {
	s := newSandbox(t)
	s.writeJSON(s.userPath(), "{oops")
	r := s.ok("status", "--json")
	var st struct{ Issues []Issue }
	must(t, json.Unmarshal([]byte(r.out), &st))
	if len(st.Issues) == 0 || st.Issues[0].Level != "fail" {
		t.Fatal(r.out)
	}
	s.addQuilr("qi")
	if r := s.run("use", "qi", "--yes"); r.code != ExitIO {
		t.Fatalf("use on invalid JSON: %d %s", r.code, r.err)
	}
}

func TestPinAllowOverride(t *testing.T) {
	s := newSandbox(t)
	s.addQuilr("qi")
	s.ok("pin", "qi", "--opus", "claude-opus-4-8", "--haiku", "claude-haiku-4-5", "--fable", "claude-fable-5-1")
	s.ok("pin", "qi", "--haiku", "")
	s.ok("allow", "qi", "--models", "claude-opus-4-8, sonnet", "--enforce")
	s.ok("override", "qi", "claude-opus-4-8=team/opus", "claude-haiku-4-5=team/haiku")
	s.ok("override", "qi", "--remove", "claude-haiku-4-5")
	p, err := GetProfile("qi")
	must(t, err)
	if len(p.Pins) != 2 || p.Pins["opus"] != "claude-opus-4-8" || p.Pins["fable"] != "claude-fable-5-1" {
		t.Fatalf("pins %v", p.Pins)
	}
	if strings.Join(p.AvailableModels, ",") != "claude-opus-4-8,sonnet" || !p.EnforceAvailable {
		t.Fatalf("allow %v", p.AvailableModels)
	}
	if len(p.ModelOverrides) != 1 || p.ModelOverrides["claude-opus-4-8"] != "team/opus" {
		t.Fatalf("overrides %v", p.ModelOverrides)
	}
	for _, args := range [][]string{{"override", "qi", "nonsense"}, {"pin", "qi"}, {"allow", "qi"}} {
		if r := s.run(args...); r.code != ExitUsage {
			t.Errorf("%v: exit %d", args, r.code)
		}
	}
}

func TestModelsMarksAndCaches(t *testing.T) {
	s := newSandbox(t)
	g := newFakeGateway(t)
	s.addQuilr("qi", "--base-url", g.Base(), "--sonnet", "claude-sonnet-4-6", "--opus", "claude-opus-9")
	r := s.ok("models", "qi", "--json")
	var res struct {
		Models []map[string]any `json:"models"`
	}
	must(t, json.Unmarshal([]byte(r.out), &res))
	rows := map[string]map[string]any{}
	for _, m := range res.Models {
		rows[m["id"].(string)] = m
	}
	if rows["claude-sonnet-4-6"]["pinned_as"] != "sonnet" || rows["team-default-group"]["dropped_by_filter"] != true ||
		rows["bedrock/anthropic.claude-sonnet-4-5"]["in_picker"] != true {
		t.Fatalf("rows %v", rows)
	}
	if g.count() != 1 {
		t.Fatal("expected one request")
	}
	s.ok("models", "qi")
	if g.count() != 1 {
		t.Fatal("second call should use the cache")
	}
	r = s.ok("models", "qi", "--refresh")
	if g.count() != 2 || !strings.Contains(r.err, "claude-opus-9") {
		t.Fatalf("refresh: %d requests, stderr %s", g.count(), r.err)
	}
	// No name: uses the profile active in the highest scope.
	s.ok("use", "qi", "--yes")
	s.ok("models", "--json")
}

func TestManagedScopePermissionError(t *testing.T) {
	s := newSandbox(t)
	s.addQuilr("qi")
	orig := createTemp
	createTemp = func(dir, pattern string) (*os.File, error) {
		if strings.HasPrefix(dir, ManagedDir()) {
			return nil, &fs.PathError{Op: "open", Path: dir, Err: fs.ErrPermission}
		}
		return orig(dir, pattern)
	}
	t.Cleanup(func() { createTemp = orig })
	r := s.run("use", "qi", "--scope", "managed", "--yes")
	if r.code != ExitIO {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
	if !strings.Contains(r.err, "sudo") && !strings.Contains(r.err, "elevated") {
		t.Fatal(r.err)
	}
	if len(ListBackups()) != 0 {
		t.Fatal("must fail before writing anything")
	}
}

func TestUsageErrors(t *testing.T) {
	s := newSandbox(t)
	for _, args := range [][]string{{"frobnicate"}, {"use"}, {"use", "x", "--scope", "galaxy"}, {"use", "--bogus"}} {
		if r := s.run(args...); r.code != ExitUsage {
			t.Errorf("%v: exit %d", args, r.code)
		}
	}
	for _, args := range [][]string{{}, {"--help"}, {"help"}, {"--version"}, {"-v"}, {"version"}} {
		if r := s.run(args...); r.code != ExitOK || r.out == "" {
			t.Errorf("%v: exit %d, must print and exit 0 (winget runs the bare command)", args, r.code)
		}
	}
	if r := s.run("use", "-h"); r.code != ExitOK {
		t.Error("-h should exit 0")
	}
	if r := s.run("version"); !strings.Contains(r.out, "tether") {
		t.Error("version")
	}
}

func TestProfileSetKey(t *testing.T) {
	s := newSandbox(t)
	s.addQuilr("qi", "--sonnet", "claude-sonnet-4-6")
	before, _ := GetProfile("qi")
	newKey := "sk-quilr-NEWKEY0000000000wxyz"
	r := s.runStdin(newKey+"\n", "profile", "set-key", "qi", "--key-stdin")
	if r.code != 0 {
		t.Fatal(r.err)
	}
	if strings.Contains(r.out+r.err, newKey) {
		t.Fatal("new key printed")
	}
	if k, _ := ReadKey("qi", "file"); k != newKey {
		t.Fatal("key not replaced")
	}
	after, _ := GetProfile("qi")
	if after.Region != before.Region || after.Email != before.Email || after.Pins["sonnet"] != "claude-sonnet-4-6" {
		t.Fatalf("other settings changed: %+v", after)
	}
	s.ok("profile", "add", "direct", "--type", "anthropic")
	if r := s.run("profile", "set-key", "direct", "--key", "x"); r.code != ExitUsage {
		t.Fatal("anthropic profile has no key")
	}
	if r := s.run("profile", "set-key", "qi"); r.code != ExitUsage {
		t.Fatal("missing key must be a usage error")
	}
}
