package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gurmukhnishansingh-quilr/quilr-tether/internal/ojson"
)

const testMCPToken = "qmcp-tok-0123456789abcdefWXYZ"

// claudeJSON mimics ~/.claude.json: unrelated state plus one user server.
const claudeJSON = `{
  "numStartups": 42,
  "oauthAccount": {"emailAddress": "you@example.com"},
  "projects": {"/tmp/x": {"allowedTools": []}},
  "mcpServers": {
    "db": {"command": "db-mcp", "env": {"DB_PASSWORD": "hunter2-very-secret"}},
    "other": {"type": "http", "url": "https://x.example/mcp", "headers": {"Authorization": "Bearer other-secret-token-123456"}}
  },
  "tipsHistory": {"a": 1.5e3}
}
`

func (s *sandbox) claudeJSONPath() string { return ClaudeJSONPath() }

func (s *sandbox) addMCP(name string, extra ...string) {
	s.t.Helper()
	s.t.Setenv("TEST_MCP_TOKEN", testMCPToken)
	args := append([]string{"mcp", "add", name, "--key-env", "TEST_MCP_TOKEN"}, extra...)
	s.ok(args...)
}

func assertNoMCPToken(t *testing.T, texts ...string) {
	t.Helper()
	for _, x := range texts {
		for _, secret := range []string{testMCPToken, "hunter2-very-secret", "other-secret-token-123456"} {
			if strings.Contains(x, secret) {
				t.Fatalf("secret %q printed:\n%s", secret, x)
			}
		}
	}
}

func serverEntry(t *testing.T, doc *ojson.Object, name string) *ojson.Object {
	t.Helper()
	servers, ok := doc.GetObject("mcpServers")
	if !ok {
		t.Fatal("no mcpServers")
	}
	e, ok := servers.GetObject(name)
	if !ok {
		t.Fatalf("no mcpServers.%s", name)
	}
	return e
}

func TestMCPAddUseWritesHelperAndPreservesClaudeJSON(t *testing.T) {
	s := newSandbox(t)
	path := s.claudeJSONPath()
	s.writeJSON(path, claudeJSON)
	s.addMCP("gh", "--slug", "github-prod", "--email", "dev@example.com")
	r := s.ok("mcp", "use", "gh", "--yes")
	assertNoMCPToken(t, r.out, r.err)

	got := s.readJSON(path)
	orig := parse(t, claudeJSON)
	for _, k := range []string{"numStartups", "oauthAccount", "projects", "tipsHistory"} {
		a, _ := orig.Get(k)
		b, _ := got.Get(k)
		if !ojson.Equal(a, b) {
			t.Errorf("%s changed", k)
		}
	}
	if strings.Join(got.Keys(), ",") != strings.Join(orig.Keys(), ",") {
		t.Fatalf("top-level order changed: %v", got.Keys())
	}
	servers, _ := got.GetObject("mcpServers")
	if strings.Join(servers.Keys(), ",") != "db,other,gh" {
		t.Fatalf("servers: %v", servers.Keys())
	}
	for _, k := range []string{"db", "other"} {
		a, _ := orig.GetObject("mcpServers")
		av, _ := a.Get(k)
		bv, _ := servers.Get(k)
		if !ojson.Equal(av, bv) {
			t.Errorf("server %s changed", k)
		}
	}
	e := serverEntry(t, got, "gh")
	if u, _ := e.GetString("url"); u != "https://mcpgateway.quilr.ai/github-prod/mcp" {
		t.Fatalf("url %q", u)
	}
	if h, _ := e.GetString("headersHelper"); !strings.HasSuffix(h, " mcp-headers gh") {
		t.Fatalf("headersHelper %q", h)
	}
	if typ, _ := e.GetString("type"); typ != "http" || e.Has("headers") {
		t.Fatalf("entry %s", jsonString(e))
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), testMCPToken) {
		t.Fatal("token written to ~/.claude.json")
	}
	if !strings.Contains(string(raw), "1.5e3") {
		t.Fatal("number formatting changed")
	}
	toml, _ := os.ReadFile(MCPPath())
	if strings.Contains(string(toml), testMCPToken) || !strings.Contains(string(toml), `slug = "github-prod"`) {
		t.Fatalf("mcp.toml:\n%s", toml)
	}
	// Re-applying is a no-op; the diff shows other servers' secrets masked.
	if r := s.ok("mcp", "use", "gh", "--yes"); !strings.Contains(r.out, "nothing to write") {
		t.Fatal(r.out)
	}
}

func TestMCPHeadersCommand(t *testing.T) {
	s := newSandbox(t)
	s.addMCP("gh", "--slug", "github-prod", "--email", "dev@example.com")
	r := s.ok("mcp-headers", "gh")
	var h map[string]string
	must(t, json.Unmarshal([]byte(r.out), &h))
	if h["Authorization"] != "Bearer "+testMCPToken || h["mcpuser"] != "dev@example.com" || len(h) != 2 {
		t.Fatalf("headers %v", h)
	}
}

func TestMCPOneMCPOAuthHasNoHelper(t *testing.T) {
	s := newSandbox(t)
	s.ok("mcp", "add", "one", "--onemcp", "--auth", "oauth", "--domain", "quilrai.com")
	s.ok("mcp", "use", "one", "--yes")
	e := serverEntry(t, s.readJSON(s.claudeJSONPath()), "one")
	if !ojson.Equal(e, parse(t, `{"type":"http","url":"https://mcpgateway.quilrai.com/quilrone/mcp"}`)) {
		t.Fatalf("entry %s", jsonString(e))
	}
	if r := s.run("mcp-headers", "one"); r.code != ExitUsage {
		t.Fatal("oauth servers have no headers")
	}
	t.Setenv("TEST_MCP_TOKEN", testMCPToken)
	if r := s.run("mcp", "add", "one2", "--onemcp", "--auth", "oauth", "--key-env", "TEST_MCP_TOKEN"); r.code != ExitUsage {
		t.Fatal("oauth with a token should be refused")
	}
}

func TestMCPAddValidation(t *testing.T) {
	s := newSandbox(t)
	t.Setenv("TEST_MCP_TOKEN", testMCPToken)
	cases := [][]string{
		{"mcp", "add", "gh", "--slug", "github-prod", "--key-env", "TEST_MCP_TOKEN"}, // token auth without email
		{"mcp", "add", "gh", "--slug", "github-prod", "--email", "dev@example.com"},  // no token
		{"mcp", "add", "gh", "--slug", "bad/slug", "--email", "dev@example.com", "--key-env", "TEST_MCP_TOKEN"},
		{"mcp", "add", "gh", "--slug", "x", "--onemcp"},
		{"mcp", "add", "gh", "--slug", "x", "--domain", "example.com"},
		{"mcp", "add", "gh", "--slug", "x", "--auth", "basic"},
		{"mcp", "add", "gh", "--slug", "x", "--scope", "project"},
		{"mcp", "list", "--scope", "managed"},
	}
	for _, args := range cases {
		if r := s.run(args...); r.code != ExitUsage {
			t.Errorf("%v: exit %d %s", args, r.code, r.err)
		}
	}
	// OneMCP token auth doesn't need mcpuser.
	s.addMCP("one", "--onemcp")
	s.addMCP("gh", "--slug", "github-prod", "--email", "dev@example.com")
	if r := s.run("mcp", "add", "gh", "--slug", "y", "--email", "dev@example.com", "--key-env", "TEST_MCP_TOKEN"); r.code != ExitUsage {
		t.Fatal("duplicate needs --force")
	}
	// --force without a token keeps the stored one.
	s.ok("mcp", "add", "gh", "--slug", "github-new", "--email", "dev@example.com", "--force")
	if r := s.ok("mcp-headers", "gh"); !strings.Contains(r.out, testMCPToken) {
		t.Fatal("token lost on --force")
	}
}

func TestMCPUseRefusesForeignEntry(t *testing.T) {
	s := newSandbox(t)
	path := s.claudeJSONPath()
	s.writeJSON(path, claudeJSON)
	s.addMCP("other", "--slug", "github-prod", "--email", "dev@example.com")
	if r := s.run("mcp", "use", "other", "--yes"); r.code != ExitUsage || !strings.Contains(r.err, "did not create") {
		t.Fatalf("%d %s", r.code, r.err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != claudeJSON {
		t.Fatal("file changed")
	}
	r := s.ok("mcp", "use", "other", "--yes", "--force")
	assertNoMCPToken(t, r.out, r.err)
	if h, _ := serverEntry(t, s.readJSON(path), "other").GetString("headersHelper"); h == "" {
		t.Fatal("--force did not replace")
	}
}

func TestMCPUseRequiresConfirmation(t *testing.T) {
	s := newSandbox(t)
	path := s.claudeJSONPath()
	s.writeJSON(path, claudeJSON)
	s.addMCP("gh", "--slug", "github-prod", "--email", "dev@example.com")
	if r := s.run("mcp", "use", "gh"); r.code != ExitUsage || !strings.Contains(r.err, "--yes") {
		t.Fatalf("%d %s", r.code, r.err)
	}
	r := s.ok("mcp", "diff", "gh")
	assertNoMCPToken(t, r.out, r.err)
	if !strings.Contains(r.out, "+      \"headersHelper\"") || strings.Contains(r.out, "numStartups") {
		t.Fatalf("diff should show only mcpServers:\n%s", r.out)
	}
	if raw, _ := os.ReadFile(path); string(raw) != claudeJSON {
		t.Fatal("file changed")
	}
}

func TestMCPRemoveAndRestore(t *testing.T) {
	s := newSandbox(t)
	path := s.claudeJSONPath()
	s.writeJSON(path, claudeJSON)
	s.addMCP("gh", "--slug", "github-prod", "--email", "dev@example.com")
	s.ok("mcp", "use", "gh", "--yes")

	// restore --mcp undoes the use byte for byte.
	s.ok("restore", "--mcp", "--yes")
	if raw, _ := os.ReadFile(path); string(raw) != claudeJSON {
		t.Fatalf("restore not byte-identical:\n%s", raw)
	}
	s.ok("mcp", "use", "gh", "--yes")
	s.ok("mcp", "remove", "gh", "--yes")
	got := s.readJSON(path)
	if servers, _ := got.GetObject("mcpServers"); servers.Has("gh") || servers.Len() != 2 {
		t.Fatalf("servers after remove: %v", servers.Keys())
	}
	if r := s.run("mcp-headers", "gh"); r.code != ExitUsage {
		t.Fatal("removed server still known")
	}
	if _, err := os.Stat(filepath.Join(ConfigDir(), "secrets", "mcp.gh.key")); err == nil {
		t.Fatal("token not deleted")
	}
	// Removing tether's only server drops the block tether created.
	s2 := newSandbox(t)
	s2.addMCP("gh", "--slug", "github-prod", "--email", "dev@example.com")
	s2.ok("mcp", "use", "gh", "--yes")
	s2.ok("mcp", "remove", "gh", "--yes")
	if s2.readJSON(s2.claudeJSONPath()).Has("mcpServers") {
		t.Fatal("empty mcpServers left behind")
	}
}

func TestMCPListAndShow(t *testing.T) {
	s := newSandbox(t)
	s.addMCP("gh", "--slug", "github-prod", "--email", "dev@example.com")
	s.ok("mcp", "add", "one", "--onemcp", "--auth", "oauth")
	s.ok("mcp", "use", "gh", "--yes")
	r := s.ok("mcp", "list")
	assertNoMCPToken(t, r.out, r.err)
	if !strings.Contains(r.out, "* gh") || !strings.Contains(r.out, "  one") {
		t.Fatal(r.out)
	}
	r = s.ok("mcp", "show", "gh", "--json")
	assertNoMCPToken(t, r.out, r.err)
	var sum map[string]string
	must(t, json.Unmarshal([]byte(r.out), &sum))
	if sum["claude_code"] != "configured" || sum["auth"] != "token" || !strings.Contains(sum["key"], "…") {
		t.Fatalf("%v", sum)
	}
}

func TestMaskMCPServers(t *testing.T) {
	servers, _ := parse(t, claudeJSON).GetObject("mcpServers")
	out, _ := ojson.Marshal(MaskMCPServers(servers), "")
	assertNoMCPToken(t, string(out))
	if !strings.Contains(string(out), "db-mcp") {
		t.Fatal(string(out))
	}
}

// --- doctor ------------------------------------------------------------------

type fakeMCP struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	sse      bool
	status   int    // non-zero: answer every request with this status
	agent    string // non-empty: refuse a User-Agent without this keyword, as the gateway does for agent-scoped tokens
}

func newFakeMCP(t *testing.T) *fakeMCP {
	f := &fakeMCP{}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeMCP) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Clone(r.Context()))
	f.mu.Unlock()
	if f.agent != "" && !strings.Contains(strings.ToLower(r.UserAgent()), f.agent) {
		writeJSONResp(w, 403, map[string]any{"detail": "This API token is scoped to the '" + f.agent + "' agent. Request user-agent does not match."})
		return
	}
	if f.status != 0 {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="x"`)
		writeJSONResp(w, f.status, map[string]any{"error": "unauthorized"})
		return
	}
	var req map[string]any
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &req)
	var result any
	switch req["method"] {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "sess-1")
		result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
			"serverInfo": map[string]any{"name": "quilr-mcp", "version": "1.0"}}
	case "notifications/initialized":
		w.WriteHeader(202)
		return
	case "tools/list":
		if r.Header.Get("Mcp-Session-Id") != "sess-1" {
			writeJSONResp(w, 400, map[string]any{"error": "missing session"})
			return
		}
		result = map[string]any{"tools": []any{map[string]any{"name": "find_relevant_tools"}, map[string]any{"name": "call_tool"}}}
	}
	resp := map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": result}
	if f.sse {
		b, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message\ndata: "+string(b)+"\n\n")
		return
	}
	writeJSONResp(w, 200, resp)
}

type mcpDoctorReport struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

func (s *sandbox) mcpDoctor(name string) (mcpDoctorReport, result) {
	s.t.Helper()
	r := s.run("mcp", "doctor", name, "--json")
	var rep mcpDoctorReport
	if err := json.Unmarshal([]byte(r.out), &rep); err != nil {
		s.t.Fatalf("mcp doctor output not JSON (exit %d): %s\n%s", r.code, r.out, r.err)
	}
	return rep, r
}

func (rep mcpDoctorReport) status() string {
	parts := make([]string, len(rep.Checks))
	for i, c := range rep.Checks {
		parts[i] = c.Name + "=" + c.Status
	}
	return strings.Join(parts, " ")
}

func TestMCPDoctorAllGreen(t *testing.T) {
	for _, sse := range []bool{false, true} {
		s := newSandbox(t)
		f := newFakeMCP(t)
		f.sse = sse
		s.addMCP("gh", "--slug", "github-prod", "--email", "dev@example.com", "--base-url", f.URL)
		s.ok("mcp", "use", "gh", "--yes")
		rep, r := s.mcpDoctor("gh")
		assertNoMCPToken(t, r.out, r.err)
		want := "claude config=pass credentials=pass managed policy=pass initialize=pass tools/list=pass"
		if r.code != ExitOK || !rep.OK || rep.status() != want {
			t.Fatalf("sse=%v: %s\n%s", sse, rep.status(), r.out)
		}
		if !strings.Contains(rep.Checks[4].Detail, "2 tools") {
			t.Fatal(rep.Checks[4].Detail)
		}
		for _, req := range f.requests {
			if req.Header.Get("Authorization") != "Bearer "+testMCPToken || req.Header.Get("mcpuser") != "dev@example.com" {
				t.Fatalf("%s missing auth headers", req.Method)
			}
			if req.URL.Path != "/github-prod/mcp" {
				t.Fatalf("path %s", req.URL.Path)
			}
		}
	}
}

// A token scoped to Claude Code passes: doctor sends Claude Code's User-Agent
// keyword. A token scoped to another agent fails with a fix that says so.
func TestMCPDoctorAgentScopedToken(t *testing.T) {
	s := newSandbox(t)
	f := newFakeMCP(t)
	f.agent = "claude"
	s.addMCP("gh", "--slug", "github-prod", "--email", "dev@example.com", "--base-url", f.URL)
	s.ok("mcp", "use", "gh", "--yes")
	if rep, r := s.mcpDoctor("gh"); r.code != ExitOK || !rep.OK {
		t.Fatalf("%s\n%s", rep.status(), r.out)
	}

	f.agent = "cursor"
	rep, r := s.mcpDoctor("gh")
	if r.code != ExitDoctorFailed || rep.Checks[3].Status != "fail" {
		t.Fatalf("%s\n%s", rep.status(), r.out)
	}
	if fix := rep.Checks[3].Fix; !strings.Contains(fix, "scoped to another agent") || strings.Contains(fix, "company domain") {
		t.Fatal(fix)
	}
}

func TestMCPDoctorFailures(t *testing.T) {
	s := newSandbox(t)
	f := newFakeMCP(t)
	f.status = 401
	s.addMCP("gh", "--slug", "github-prod", "--email", "dev@example.com", "--base-url", f.URL)
	rep, r := s.mcpDoctor("gh")
	if r.code != ExitDoctorFailed || rep.OK {
		t.Fatal(r.out)
	}
	if want := "claude config=fail credentials=pass managed policy=pass initialize=fail tools/list=skip"; rep.status() != want {
		t.Fatal(rep.status())
	}
	if !strings.Contains(rep.Checks[3].Fix, "allowed company domain") {
		t.Fatal(rep.Checks[3].Fix)
	}

	// OAuth: a 401 means reachable and waiting for sign-in, not a failure.
	s.ok("mcp", "add", "one", "--onemcp", "--auth", "oauth", "--base-url", f.URL)
	s.ok("mcp", "use", "one", "--yes")
	rep, r = s.mcpDoctor("one")
	if want := "claude config=pass credentials=skip managed policy=pass initialize=pass tools/list=skip"; r.code != ExitOK || rep.status() != want {
		t.Fatalf("%s\n%s", rep.status(), r.out)
	}

	// Managed policy: a deny pattern on the URL fails, and so does managed-mcp.json.
	s.writeJSON(filepath.Join(ManagedDir(), "managed-settings.json"),
		`{"deniedMcpServers": [{"serverUrl": "`+strings.ToUpper(f.URL[:7])+f.URL[7:]+`/*"}]}`)
	rep, _ = s.mcpDoctor("one")
	if rep.Checks[2].Status != "fail" || !strings.Contains(rep.Checks[2].Detail, "deniedMcpServers") {
		t.Fatal(rep.status())
	}
	s.writeJSON(filepath.Join(ManagedDir(), "managed-settings.json"), `{"allowedMcpServers": [{"serverName": "one"}]}`)
	if rep, _ = s.mcpDoctor("one"); rep.Checks[2].Status != "pass" {
		t.Fatal(rep.status())
	}
	if rep, _ = s.mcpDoctor("gh"); rep.Checks[2].Status != "fail" {
		t.Fatal("gh is not in the allowlist")
	}
	s.writeJSON(filepath.Join(ManagedDir(), "managed-mcp.json"), `{"mcpServers": {}}`)
	if rep, _ = s.mcpDoctor("one"); rep.Checks[2].Status != "fail" {
		t.Fatal("managed-mcp.json should block user servers")
	}
}
