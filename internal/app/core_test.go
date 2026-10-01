package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gurmukhnishansingh-quilr/quilr-tether/internal/ojson"
)

const existing = `{
  "permissions": {"allow": ["Bash(git status)"], "deny": []},
  "env": {"FOO": "bar", "ANTHROPIC_BASE_URL": "https://old.example/x", "ZED": "1"},
  "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "echo done"}]}]},
  "model": "opus",
  "mcpServers": {"x": {"command": "x"}}
}`

func parse(t *testing.T, s string) *ojson.Object {
	t.Helper()
	o, err := ojson.ParseObject([]byte(s))
	must(t, err)
	return o
}

func TestMergePreservesForeignKeysAndOrder(t *testing.T) {
	in := parse(t, existing)
	f := NewFragment()
	f.Top.Set("apiKeyHelper", "helper")
	f.Env.Set("ANTHROPIC_BASE_URL", "https://new.example/anthropic_messages")
	f.Env.Set("CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY", "1")
	out, err := ApplyFragment(in, f)
	must(t, err)
	if got := strings.Join(out.Keys(), ","); got != "permissions,env,hooks,mcpServers,apiKeyHelper" {
		t.Fatalf("top keys: %s", got)
	}
	for _, k := range []string{"permissions", "hooks", "mcpServers"} {
		a, _ := in.Get(k)
		b, _ := out.Get(k)
		if !ojson.Equal(a, b) {
			t.Errorf("%s changed", k)
		}
	}
	if got := strings.Join(envOf(out).Keys(), ","); got != "FOO,ANTHROPIC_BASE_URL,ZED,CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY" {
		t.Fatalf("env keys: %s", got)
	}
	if v, _ := envOf(out).GetString("ANTHROPIC_BASE_URL"); !strings.HasPrefix(v, "https://new.example") {
		t.Fatal("base url not replaced")
	}
	if out.Has("model") {
		t.Fatal("owned key not in the new profile must be cleared")
	}
	if !in.Has("model") {
		t.Fatal("input mutated")
	}
}

func TestEnvBlockRemovedOnlyWhenWeEmptiedIt(t *testing.T) {
	out, _ := ApplyFragment(parse(t, `{"env": {"ANTHROPIC_API_KEY": "k"}}`), NewFragment())
	if out.Has("env") {
		t.Fatal("env emptied by tether should be removed")
	}
	out, _ = ApplyFragment(parse(t, `{"env": {}}`), NewFragment())
	if !out.Has("env") {
		t.Fatal("user's empty env must be kept")
	}
	out, _ = ApplyFragment(parse(t, existing), NewFragment())
	if got := strings.Join(envOf(out).Keys(), ","); got != "FOO,ZED" {
		t.Fatalf("env = %s", got)
	}
	if _, err := ApplyFragment(parse(t, `{"env": "nope"}`), NewFragment()); err == nil {
		t.Fatal("non-object env must be refused")
	}
}

func TestOwnershipPatterns(t *testing.T) {
	cases := map[string]bool{
		"ANTHROPIC_DEFAULT_OPUS_MODEL": true, "ANTHROPIC_DEFAULT_FABLE_MODEL_NAME": true,
		"ANTHROPIC_DEFAULT_SONNET_MODEL_DESCRIPTION": true, "ANTHROPIC_DEFAULT_HAIKU_MODEL_SUPPORTED_CAPABILITIES": true,
		"ANTHROPIC_CUSTOM_MODEL_OPTION": true, "ANTHROPIC_CUSTOM_MODEL_OPTION_NAME": true,
		"CLAUDE_CODE_USE_BEDROCK": true, "AWS_PROFILE": true,
		"AWS_ACCESS_KEY_ID": false, "CLAUDE_CODE_USE_VERTEX": false, "DISABLE_TELEMETRY": false,
	}
	for name, want := range cases {
		if IsOwnedEnv(name) != want {
			t.Errorf("%s: want %v", name, want)
		}
	}
	f := NewFragment()
	f.Top.Set("permissions", "x")
	if _, err := ApplyFragment(ojson.NewObject(), f); err == nil {
		t.Fatal("fragment with unowned key must be rejected")
	}
}

func TestAtomicWriteKeepsIndent(t *testing.T) {
	s := newSandbox(t)
	p := filepath.Join(s.root, "s.json")
	must(t, os.WriteFile(p, []byte("{\n    \"a\": 1\n}\n"), 0o644))
	o := parse(t, `{"a":1,"b":2}`)
	_, err := WriteSettings(p, o, "user")
	must(t, err)
	b, _ := os.ReadFile(p)
	if string(b) != "{\n    \"a\": 1,\n    \"b\": 2\n}\n" {
		t.Fatalf("got %q", b)
	}
	if m, _ := filepath.Glob(filepath.Join(s.root, ".s.json.*")); len(m) > 0 {
		t.Fatalf("temp files left: %v", m)
	}
}

func TestInvalidJSONIsIOError(t *testing.T) {
	s := newSandbox(t)
	p := filepath.Join(s.root, "bad.json")
	must(t, os.WriteFile(p, []byte("{nope"), 0o644))
	_, err := LoadSettings(p)
	if e, ok := err.(*Error); !ok || e.Code != ExitIO {
		t.Fatalf("got %v", err)
	}
}

func TestBackupRotationKeepsTwenty(t *testing.T) {
	s := newSandbox(t)
	target := s.userPath()
	for i := 0; i < 25; i++ {
		o := ojson.NewObject()
		o.Set("n", i)
		_, err := WriteSettings(target, o, "user")
		must(t, err)
	}
	backups := ListBackups()
	if len(backups) != 20 {
		t.Fatalf("got %d backups", len(backups))
	}
	metas, _ := filepath.Glob(filepath.Join(BackupsDir(), "*.meta"))
	if len(metas) != 20 {
		t.Fatalf("got %d meta files", len(metas))
	}
	last := s.readJSON(backups[19].Path)
	if v, _ := last.Get("n"); v.(interface{ String() string }).String() != "23" {
		t.Fatalf("newest backup = %v", v)
	}
}

func TestFirstBackupRecordsMissingFile(t *testing.T) {
	s := newSandbox(t)
	_, err := WriteSettings(s.userPath(), parse(t, `{"a":1}`), "user")
	must(t, err)
	b, err := FindBackup("", s.userPath())
	must(t, err)
	if b.Existed {
		t.Fatal("backup should record that the file did not exist")
	}
	if data, _ := ReadBackup(b); data != nil {
		t.Fatal("expected nil content")
	}
}

// --- profiles ------------------------------------------------------------------

func quilrProfile() *Profile {
	return &Profile{Name: "qi", Type: "quilr", Region: "india-1", Email: "dev@example.com", ProviderLabel: "claude-code"}
}

func TestQuilrBaseURLs(t *testing.T) {
	p := quilrProfile()
	if p.GatewayRoot() != "https://guardrails-india-1.quilr.ai/anthropic_messages" {
		t.Fatal(p.GatewayRoot())
	}
	p.Region = "auto"
	if p.GatewayRoot() != "https://guardrails.quilr.ai/anthropic_messages" {
		t.Fatal(p.GatewayRoot())
	}
	p.BaseURL = "https://gw.example/anthropic_messages/"
	if p.GatewayRoot() != "https://gw.example/anthropic_messages" {
		t.Fatal(p.GatewayRoot())
	}
	bad := quilrProfile()
	bad.Region = "mars-1"
	if bad.Validate() == nil {
		t.Fatal("unknown region accepted")
	}
}

func TestQuilrFragment(t *testing.T) {
	p := quilrProfile()
	p.BedrockBacked = true
	p.Pins = map[string]string{"sonnet": "claude-sonnet-4-6"}
	f := BuildFragment(p, FragmentOpts{HelperCommand: "tether key qi"})
	want := parse(t, `{"ANTHROPIC_BASE_URL":"https://guardrails-india-1.quilr.ai/anthropic_messages",
		"ANTHROPIC_CUSTOM_HEADERS":"X-User-Email: dev@example.com\nX-Provider-Label: claude-code",
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY":"1","CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS":"1",
		"ANTHROPIC_DEFAULT_SONNET_MODEL":"claude-sonnet-4-6"}`)
	if !ojson.Equal(f.Env, want) {
		t.Fatalf("env = %s", jsonString(f.Env))
	}
	if !ojson.Equal(f.Top, parse(t, `{"apiKeyHelper":"tether key qi"}`)) {
		t.Fatalf("top = %s", jsonString(f.Top))
	}
	off := false
	p.Discovery = &off
	f = BuildFragment(p, FragmentOpts{PlaintextKey: testKey})
	if v, _ := f.Env.GetString("ANTHROPIC_API_KEY"); v != testKey || f.Top.Has("apiKeyHelper") {
		t.Fatal("plaintext mode wrong")
	}
	if f.Env.Has("CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY") || f.Env.Has("ANTHROPIC_AUTH_TOKEN") {
		t.Fatal("unexpected env")
	}
}

func TestBedrockAndAnthropicFragments(t *testing.T) {
	p := &Profile{Name: "br", Type: "bedrock", AWSRegion: "us-east-1", AWSProfile: "dev", SSORefresh: true,
		Pins: map[string]string{"opus": "us.anthropic.claude-opus-4-8"}}
	f := BuildFragment(p, FragmentOpts{})
	if !ojson.Equal(f.Env, parse(t, `{"CLAUDE_CODE_USE_BEDROCK":"1","AWS_REGION":"us-east-1","AWS_PROFILE":"dev",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":"us.anthropic.claude-opus-4-8"}`)) {
		t.Fatalf("env = %s", jsonString(f.Env))
	}
	if v, _ := f.Top.GetString("awsAuthRefresh"); v != "aws sso login --profile dev" {
		t.Fatal("awsAuthRefresh")
	}
	if !BuildFragment(&Profile{Name: "a", Type: "anthropic"}, FragmentOpts{}).IsEmpty() {
		t.Fatal("anthropic fragment must be empty")
	}
}

func TestEnforceOnlyWrittenToManaged(t *testing.T) {
	p := quilrProfile()
	p.AvailableModels = []string{"claude-sonnet-4-6"}
	p.EnforceAvailable = true
	if BuildFragment(p, FragmentOpts{Scope: "user"}).Top.Has("enforceAvailableModels") {
		t.Fatal("enforce written to user scope")
	}
	if !BuildFragment(p, FragmentOpts{Scope: "managed"}).Top.Has("enforceAvailableModels") {
		t.Fatal("enforce missing from managed scope")
	}
}

func TestProfilesTOMLRoundTrip(t *testing.T) {
	newSandbox(t)
	p := quilrProfile()
	p.Pins = map[string]string{"opus": "claude-opus-4-8"}
	p.ModelOverrides = map[string]string{"claude-opus-4-8": `team/opus "x"`}
	p.AvailableModels = []string{"a", "b"}
	p.DiscoveryTimeoutMS = 5000
	off := false
	p.Discovery = &off
	must(t, PutProfile(p))
	must(t, PutProfile(&Profile{Name: "plain", Type: "anthropic"}))
	raw, _ := os.ReadFile(ProfilesPath())
	if strings.Contains(string(raw), "sk-") {
		t.Fatal("key leaked into profiles.toml")
	}
	all, err := LoadProfiles()
	must(t, err)
	got := all["qi"]
	if got.ModelOverrides["claude-opus-4-8"] != `team/opus "x"` || got.DiscoveryOn() || got.DiscoveryTimeoutMS != 5000 ||
		len(got.AvailableModels) != 2 || got.Pins["opus"] != "claude-opus-4-8" {
		t.Fatalf("round trip lost data: %+v", got)
	}
	must(t, os.WriteFile(ProfilesPath(), append(raw, []byte("\n[profiles.x]\ntype = \"quilr\"\nbogus = 1\n")...), 0o644))
	if _, err := LoadProfiles(); err == nil {
		t.Fatal("unknown field accepted")
	}
}

// --- secrets -------------------------------------------------------------------

func TestMask(t *testing.T) {
	cases := map[string]string{
		testKey:                         "sk-quilr-…ABCD",
		"sk-ant-api03-abcdefghijklmnop": "sk-ant-…mnop",
		"short":                         "…",
		"":                              "",
	}
	for in, want := range cases {
		if got := Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRedact(t *testing.T) {
	newSandbox(t)
	RegisterSecret(testKey)
	if strings.Contains(Redact("bad key "+testKey), testKey) {
		t.Fatal("registered key not masked")
	}
	if strings.Contains(Redact("got sk-other-zzzzzzzzzzzzzzzz9999 back"), "zzzzzzzzzzzzzzzz9999") {
		t.Fatal("key-shaped value not masked")
	}
	m := MaskSettings(parse(t, `{"env":{"ANTHROPIC_API_KEY":"`+testKey+`","ANTHROPIC_CUSTOM_HEADERS":"Authorization: Bearer `+testKey+`\nX-A: b"}}`))
	s := jsonString(m)
	if strings.Contains(s, testKey) || !strings.Contains(s, "X-A: b") {
		t.Fatalf("masked settings: %s", s)
	}
}

func TestFileSecretStore(t *testing.T) {
	newSandbox(t)
	backend, err := StoreKey("qi", testKey)
	must(t, err)
	if backend != "file" {
		t.Fatal(backend)
	}
	if k, _ := ReadKey("qi", backend); k != testKey {
		t.Fatal("read back failed")
	}
	if runtimeGOOS() != "windows" {
		st, _ := os.Stat(secretFile("qi"))
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("key file mode %v", st.Mode())
		}
	}
	DeleteKey("qi", backend)
	if k, _ := ReadKey("qi", backend); k != "" {
		t.Fatal("not deleted")
	}
}

func TestHelperCommandShape(t *testing.T) {
	newSandbox(t)
	cmd := HelperCommand("qi")
	if !strings.HasSuffix(cmd, " key qi") {
		t.Fatal(cmd)
	}
	if strings.Contains(cmd, `\`) {
		t.Fatalf("backslashes break under Git Bash: %s", cmd)
	}
}

func TestDiscoveryFilter(t *testing.T) {
	kept, dropped := ClaudeFilter([]string{"claude-sonnet-4-6", "vertex_ai/claude-opus-4-8",
		"bedrock/anthropic.claude-haiku-4-5", "ANTHROPIC-custom", "gpt-4o", "team-default-group", "Claude-Big"})
	if strings.Join(kept, ",") != "claude-sonnet-4-6,vertex_ai/claude-opus-4-8,bedrock/anthropic.claude-haiku-4-5,ANTHROPIC-custom,Claude-Big" {
		t.Fatalf("kept %v", kept)
	}
	if strings.Join(dropped, ",") != "gpt-4o,team-default-group" {
		t.Fatalf("dropped %v", dropped)
	}
}

func TestUnifiedDiff(t *testing.T) {
	a := strings.Split("{\n  a\n  b\n  c\n}", "\n")
	b := strings.Split("{\n  a\n  B\n  c\n  d\n}", "\n")
	got := strings.Join(unifiedDiff(a, b, "x", "y", 1), "\n")
	for _, want := range []string{"--- x", "+++ y", "-  b", "+  B", "+  d"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	if unifiedDiff(a, a, "x", "y", 2) != nil {
		t.Fatal("identical inputs should produce no diff")
	}
}
