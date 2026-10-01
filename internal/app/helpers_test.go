package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurmukhnishansingh-quilr/quilr-tether/internal/ojson"
)

const testKey = "sk-quilr-0123456789abcdefABCD"

type sandbox struct {
	t    *testing.T
	root string
	repo string
	env  []string // what ShellEnv returns
}

// newSandbox points every tether path at a temp dir and gives the test an empty shell env.
func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	repo := filepath.Join(root, "repo")
	must(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	must(t, os.MkdirAll(home, 0o755))
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("TETHER_CONFIG_DIR", filepath.Join(home, ".config", "tether"))
	t.Setenv("TETHER_MANAGED_DIR", filepath.Join(root, "managed"))
	t.Setenv("TETHER_SECRET_BACKEND", "file")
	t.Chdir(repo)
	s := &sandbox{t: t, root: root, repo: repo}
	oldEnv := ShellEnv
	ShellEnv = func() []string { return s.env }
	t.Cleanup(func() { ShellEnv = oldEnv })
	resetSecrets()
	return s
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type result struct {
	code     int
	out, err string
}

func (s *sandbox) run(args ...string) result {
	return s.runStdin("", args...)
}

func (s *sandbox) runStdin(stdin string, args ...string) result {
	var out, errb bytes.Buffer
	ui := &UI{Out: &out, Err: &errb, In: strings.NewReader(stdin)}
	code := Run(args, ui)
	return result{code, out.String(), errb.String()}
}

func (s *sandbox) ok(args ...string) result {
	s.t.Helper()
	r := s.run(args...)
	if r.code != 0 {
		s.t.Fatalf("tether %v: exit %d\nstdout: %s\nstderr: %s", args, r.code, r.out, r.err)
	}
	return r
}

func (s *sandbox) addQuilr(name string, extra ...string) {
	s.t.Helper()
	s.t.Setenv("TEST_QUILR_KEY", testKey)
	args := append([]string{"profile", "add", name, "--type", "quilr", "--region", "india-1",
		"--email", "dev@example.com", "--label", "claude-code", "--key-env", "TEST_QUILR_KEY"}, extra...)
	s.ok(args...)
}

func (s *sandbox) userPath() string {
	p, _ := SettingsPath("user", "")
	return p
}

func (s *sandbox) writeJSON(path, body string) {
	s.t.Helper()
	must(s.t, os.MkdirAll(filepath.Dir(path), 0o755))
	must(s.t, os.WriteFile(path, []byte(body), 0o644))
}

func (s *sandbox) readJSON(path string) *ojson.Object {
	s.t.Helper()
	b, err := os.ReadFile(path)
	must(s.t, err)
	o, err := ojson.ParseObject(b)
	must(s.t, err)
	return o
}

func envOf(o *ojson.Object) *ojson.Object {
	e, _ := o.GetObject("env")
	if e == nil {
		return ojson.NewObject()
	}
	return e
}

// --- fake gateway --------------------------------------------------------------

type fakeGateway struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	models   []string

	modelsHandler      http.HandlerFunc
	messagesHandler    http.HandlerFunc
	countTokensHandler http.HandlerFunc
}

func newFakeGateway(t *testing.T) *fakeGateway {
	g := &fakeGateway{models: []string{"claude-sonnet-4-6", "claude-opus-4-8", "claude-haiku-4-5",
		"bedrock/anthropic.claude-sonnet-4-5", "team-default-group"}}
	g.Server = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.Close)
	return g
}

// Base is the value for --base-url.
func (g *fakeGateway) Base() string { return g.URL + "/anthropic_messages" }

func (g *fakeGateway) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.requests = append(g.requests, r.Clone(r.Context()))
	g.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/anthropic_messages")
	switch {
	case r.Method == "GET" && path == "/v1/models":
		if g.modelsHandler != nil {
			g.modelsHandler(w, r)
			return
		}
		data := []map[string]string{}
		for _, m := range g.models {
			data = append(data, map[string]string{"id": m})
		}
		writeJSONResp(w, 200, map[string]any{"data": data})
	case r.Method == "POST" && path == "/v1/messages":
		if g.messagesHandler != nil {
			g.messagesHandler(w, r)
			return
		}
		defaultMessages(w, r)
	case r.Method == "POST" && path == "/v1/messages/count_tokens":
		if g.countTokensHandler != nil {
			g.countTokensHandler(w, r)
			return
		}
		writeJSONResp(w, 200, map[string]any{"input_tokens": 8})
	default:
		writeJSONResp(w, 404, map[string]any{"error": map[string]any{"message": "not found"}})
	}
}

func (g *fakeGateway) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.requests)
}

func writeJSONResp(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

var sseEvents = []string{"message_start", "content_block_start", "ping", "content_block_delta",
	"content_block_delta", "content_block_stop", "message_delta", "message_stop"}

func defaultMessages(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	if body["stream"] == true {
		streamEvents(w, 5*time.Millisecond, 0)
		return
	}
	writeJSONResp(w, 200, map[string]any{"id": "msg_1", "content": []any{map[string]any{"type": "text", "text": "pong"}}})
}

// streamEvents writes SSE, flushing each event after gap; delay holds
// everything back first (to simulate a buffering gateway use gap 0).
func streamEvents(w http.ResponseWriter, gap, delay time.Duration) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	fl := w.(http.Flusher)
	time.Sleep(delay)
	for _, e := range sseEvents {
		_, _ = io.WriteString(w, "event: "+e+"\ndata: {\"type\":\""+e+"\"}\n\n")
		if gap > 0 {
			fl.Flush()
			time.Sleep(gap)
		}
	}
	fl.Flush()
}

func runtimeGOOS() string { return runtime.GOOS }
