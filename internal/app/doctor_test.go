package app

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type doctorReport struct {
	OK     bool    `json:"ok"`
	Tag    string  `json:"tag"`
	Checks []Check `json:"checks"`
}

func (s *sandbox) doctor(args ...string) (doctorReport, result) {
	s.t.Helper()
	r := s.run(append([]string{"doctor", "--json"}, args...)...)
	var rep doctorReport
	if err := json.Unmarshal([]byte(r.out), &rep); err != nil {
		s.t.Fatalf("doctor output not JSON (exit %d): %s\n%s", r.code, r.out, r.err)
	}
	return rep, r
}

func (rep doctorReport) check(id int) Check {
	for _, c := range rep.Checks {
		if c.ID == id {
			return c
		}
	}
	return Check{}
}

func setupDoctor(t *testing.T, extra ...string) (*sandbox, *fakeGateway) {
	s := newSandbox(t)
	g := newFakeGateway(t)
	s.addQuilr("qi", append([]string{"--base-url", g.Base()}, extra...)...)
	s.ok("use", "qi", "--yes")
	return s, g
}

func TestDoctorAllGreen(t *testing.T) {
	s, g := setupDoctor(t, "--sonnet", "claude-sonnet-4-6")
	rep, r := s.doctor("qi")
	if r.code != ExitOK || !rep.OK {
		t.Fatalf("exit %d\n%s", r.code, r.out)
	}
	for id := 1; id <= 9; id++ {
		c := rep.check(id)
		if c.Status != "pass" && !(id == 7 && c.Status == "warn") { // team-default-group is dropped -> warn
			t.Errorf("check %d %s: %s %s", id, c.Name, c.Status, c.Detail)
		}
	}
	if !strings.Contains(rep.check(7).Detail, "team-default-group") {
		t.Error("dropped id not reported")
	}
	// Every request carries the run's tag, the key and the custom headers; nothing follows redirects.
	for _, req := range g.requests {
		if req.Header.Get("X-Conversation-Id") != rep.Tag || !strings.HasPrefix(rep.Tag, "tether-doctor-") {
			t.Errorf("%s %s missing tag", req.Method, req.URL)
		}
		if req.Header.Get("X-Api-Key") != testKey || req.Header.Get("X-User-Email") != "dev@example.com" {
			t.Errorf("%s %s missing auth/custom headers", req.Method, req.URL)
		}
		if req.Header.Get("Anthropic-Version") != "2023-06-01" {
			t.Error("anthropic-version missing")
		}
	}
	assertNoKey(t, r.out, r.err)
	// The human table renders too.
	if r := s.run("doctor", "qi"); r.code != ExitOK || !strings.Contains(r.out, "PASS") {
		t.Fatal(r.out)
	}
}

func TestDoctorUsesActiveProfileAndPicksDiscoveredSonnet(t *testing.T) {
	s, g := setupDoctor(t)
	var model string
	g.messagesHandler = func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		model, _ = body["model"].(string)
		writeJSONResp(w, 200, map[string]any{"id": "m"})
	}
	rep, _ := s.doctor()
	if model != "claude-sonnet-4-6" {
		t.Fatalf("pinged %q", model)
	}
	if rep.check(3).Status != "pass" {
		t.Fatal(rep.check(3))
	}
}

func TestDoctorInferenceFailureExitsOne(t *testing.T) {
	s, g := setupDoctor(t)
	g.messagesHandler = func(w http.ResponseWriter, r *http.Request) {
		writeJSONResp(w, 401, map[string]any{"error": map[string]any{"message": "invalid x-api-key " + testKey}})
	}
	rep, r := s.doctor("qi")
	if r.code != ExitDoctorFailed || rep.OK {
		t.Fatalf("exit %d", r.code)
	}
	if c := rep.check(3); c.Status != "fail" || !strings.Contains(c.Fix, "set-key qi") {
		t.Fatal(c)
	}
	assertNoKey(t, r.out, r.err) // the gateway echoed the key; we must not
}

func TestDoctorDiscoveryRedirectFails(t *testing.T) {
	s, g := setupDoctor(t)
	g.modelsHandler = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.example/v1/models", http.StatusMovedPermanently)
	}
	rep, r := s.doctor("qi")
	c := rep.check(7)
	if c.Status != "fail" || !strings.Contains(c.Detail, "redirect") || r.code != ExitDoctorFailed {
		t.Fatal(c)
	}
	if c8 := rep.check(8); c8.Status != "pass" { // no pins -> nothing to verify
		t.Fatal(c8)
	}
}

func TestDoctorDiscoveryTooSlowAndBadShape(t *testing.T) {
	s, g := setupDoctor(t, "--discovery-timeout-ms", "50")
	g.modelsHandler = func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(120 * time.Millisecond)
		writeJSONResp(w, 200, map[string]any{"data": []any{map[string]any{"id": "claude-x"}}})
	}
	rep, _ := s.doctor("qi")
	if c := rep.check(7); c.Status != "fail" || !strings.Contains(c.Detail, "too slow") {
		t.Fatal(c)
	}
	g.modelsHandler = func(w http.ResponseWriter, r *http.Request) {
		writeJSONResp(w, 200, map[string]any{"models": []string{"claude-x"}})
	}
	rep, _ = s.doctor("qi")
	if c := rep.check(7); c.Status != "fail" || !strings.Contains(c.Detail, "shape") {
		t.Fatal(c)
	}
}

func TestDoctorDiscoveryOffDowngradesToWarn(t *testing.T) {
	s, g := setupDoctor(t, "--no-discovery")
	g.modelsHandler = func(w http.ResponseWriter, r *http.Request) { writeJSONResp(w, 404, map[string]any{}) }
	rep, r := s.doctor("qi")
	if c := rep.check(7); c.Status != "warn" || r.code != ExitOK {
		t.Fatal(c, r.code)
	}
}

func TestDoctorBufferedStreamWarns(t *testing.T) {
	s, g := setupDoctor(t)
	g.messagesHandler = func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] == true {
			streamEvents(w, 0, 700*time.Millisecond) // everything at once, at the end
			return
		}
		writeJSONResp(w, 200, map[string]any{"id": "m"})
	}
	rep, _ := s.doctor("qi")
	if c := rep.check(4); c.Status != "warn" || !strings.Contains(c.Detail, "buffer") {
		t.Fatal(c)
	}
}

func TestDoctorStreamWrongContentType(t *testing.T) {
	s, g := setupDoctor(t)
	g.messagesHandler = func(w http.ResponseWriter, r *http.Request) {
		writeJSONResp(w, 200, map[string]any{"id": "m"})
	}
	rep, _ := s.doctor("qi")
	if c := rep.check(4); c.Status != "fail" || !strings.Contains(c.Detail, "text/event-stream") {
		t.Fatal(c)
	}
}

func TestDoctorBetaRejectedAndBedrockBacked(t *testing.T) {
	reject := func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("anthropic-beta") != "" {
			writeJSONResp(w, 400, map[string]any{"error": map[string]any{"message": "Unexpected value(s) for the anthropic-beta header"}})
			return
		}
		defaultMessages(w, r)
	}
	s, g := setupDoctor(t)
	g.messagesHandler = reject
	rep, r := s.doctor("qi")
	if c := rep.check(5); c.Status != "fail" || r.code != ExitDoctorFailed {
		t.Fatal(c)
	}

	s2, g2 := setupDoctor(t, "--bedrock-backed")
	g2.messagesHandler = reject
	rep, r = s2.doctor("qi")
	if c := rep.check(5); c.Status != "warn" || r.code != ExitOK {
		t.Fatal(c, r.code)
	}
}

func TestDoctorCountTokens404IsWarning(t *testing.T) {
	s, g := setupDoctor(t)
	g.countTokensHandler = func(w http.ResponseWriter, r *http.Request) { writeJSONResp(w, 404, map[string]any{}) }
	rep, r := s.doctor("qi")
	if c := rep.check(6); c.Status != "warn" || r.code != ExitOK {
		t.Fatal(c, r.code)
	}
}

func TestDoctorPinsNotEnabled(t *testing.T) {
	s, _ := setupDoctor(t, "--opus", "claude-opus-9")
	s.ok("override", "qi", "claude-sonnet-4-6=team/sonnet")
	s.ok("use", "qi", "--yes")
	rep, _ := s.doctor("qi")
	c := rep.check(8)
	if c.Status != "warn" || !strings.Contains(c.Detail, "claude-opus-9") || !strings.Contains(c.Detail, "team/sonnet") {
		t.Fatal(c)
	}
}

func TestDoctorLocalChecks(t *testing.T) {
	s, _ := setupDoctor(t)
	s.env = []string{"CLAUDE_CODE_USE_BEDROCK=1", "ANTHROPIC_MODEL=claude-opus-4-8"}
	proj, _ := SettingsPath("project", "")
	s.writeJSON(proj, `{"env": {"ANTHROPIC_BASE_URL": "https://other.example"}}`)
	rep, r := s.doctor("qi")
	if c := rep.check(1); c.Status != "fail" || !strings.Contains(c.Detail, "discovery never runs") {
		t.Fatal(c)
	}
	c2 := rep.check(2)
	if c2.Status != "fail" || !strings.Contains(c2.Detail, "ANTHROPIC_MODEL") || !strings.Contains(c2.Detail, "project settings") {
		t.Fatal(c2)
	}
	if r.code != ExitDoctorFailed {
		t.Fatal(r.code)
	}
}

func TestDoctorNetworkError(t *testing.T) {
	s := newSandbox(t)
	g := newFakeGateway(t)
	s.addQuilr("qi", "--base-url", g.Base())
	g.Close()
	rep, r := s.doctor("qi", "--timeout", "2")
	if rep.check(3).Status != "fail" || r.code != ExitDoctorFailed {
		t.Fatal(rep.check(3))
	}
}

func TestDoctorNonGatewayProfiles(t *testing.T) {
	s := newSandbox(t)
	s.ok("profile", "add", "br", "--type", "bedrock", "--aws-region", "eu-west-1", "--aws-profile", "x", "--sso-refresh")
	s.ok("use", "br", "--yes")
	orig := lookPath
	lookPath = func(string) (string, error) { return "", errNotFound }
	t.Cleanup(func() { lookPath = orig })
	rep, r := s.doctor("br")
	if rep.check(3).Status != "skip" || rep.check(10).Status != "fail" || r.code != ExitDoctorFailed {
		t.Fatalf("%+v", rep.Checks)
	}
	s.ok("profile", "add", "direct", "--type", "anthropic")
	s.ok("use", "direct", "--yes")
	if rep, r := s.doctor("direct"); r.code != ExitOK || rep.check(3).Status != "skip" {
		t.Fatalf("%+v", rep.Checks)
	}
}

var errNotFound = &Error{Msg: "not found"}

func TestDoctorKeyProviderMismatchReportedOnce(t *testing.T) {
	s, g := setupDoctor(t)
	mismatch := func(w http.ResponseWriter, r *http.Request) {
		writeJSONResp(w, 400, map[string]any{"error": map[string]any{"message": "This API key is configured for 'anthropic' provider. " +
			"Use the appropriate endpoint or create a new API key with 'anthropic_messages', 'anthropic_messages_bedrock', or 'anthropic_messages_azure' provider."}})
	}
	g.modelsHandler, g.messagesHandler, g.countTokensHandler = mismatch, mismatch, mismatch
	rep, r := s.doctor("qi")
	if r.code != ExitDoctorFailed {
		t.Fatalf("exit %d", r.code)
	}
	c3 := rep.check(3)
	if c3.Status != "fail" || !strings.Contains(c3.Detail, "'anthropic' provider") || !strings.Contains(c3.Fix, "tether profile set-key qi") {
		t.Fatalf("check 3: %+v", c3)
	}
	for _, id := range []int{4, 5, 6, 7} {
		if c := rep.check(id); c.Status != "skip" || !strings.Contains(c.Detail, "same key problem as check 3") {
			t.Errorf("check %d: %+v", id, c)
		}
	}
	if rep.check(8).Status == "fail" {
		t.Error("check 8 must not fail")
	}
}

func TestDoctorRevokedKey(t *testing.T) {
	s, g := setupDoctor(t)
	revoked := func(w http.ResponseWriter, r *http.Request) {
		writeJSONResp(w, 401, map[string]any{"type": "error", "error": map[string]any{"type": "authentication_error",
			"message": "The provided Quilr API key is invalid or has been revoked"}})
	}
	g.modelsHandler, g.messagesHandler, g.countTokensHandler = revoked, revoked, revoked
	rep, _ := s.doctor("qi")
	if c := rep.check(3); c.Status != "fail" || !strings.Contains(c.Fix, "set-key") {
		t.Fatalf("%+v", c)
	}
	if rep.check(4).Status != "skip" {
		t.Fatalf("%+v", rep.check(4))
	}
}

func TestKeyProblemNamesMatchingProvider(t *testing.T) {
	cases := map[string][]string{
		"anthropic":    {"for 'anthropic_messages'", "set-key qi"},
		"bedrock":      {"'anthropic_messages_bedrock'", "--bedrock-backed"},
		"azure_openai": {"'anthropic_messages_azure'", "set-key qi"},
	}
	for have, wants := range cases {
		summary, fix, ok := keyProblem("HTTP 400: This API key is configured for '"+have+"' provider. Use the appropriate endpoint", "qi")
		if !ok {
			t.Fatalf("%s: not recognised", have)
		}
		for _, w := range wants {
			if !strings.Contains(summary+" "+fix, w) {
				t.Errorf("%s: missing %q in %q / %q", have, w, summary, fix)
			}
		}
	}
}

// --- quilr-bedrock --------------------------------------------------------------

func setupBedrockDoctor(t *testing.T, streaming http.HandlerFunc) (*sandbox, *fakeGateway) {
	s := newSandbox(t)
	g := newFakeGateway(t)
	g.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.requests = append(g.requests, r.Clone(r.Context()))
		g.mu.Unlock()
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential="+testKey+"/") ||
			!strings.Contains(r.Header.Get("Authorization"), "/us-east-1/bedrock/aws4_request") {
			writeJSONResp(w, 401, map[string]any{"__type": "UnrecognizedClientException", "message": "Missing or invalid AWS Signature Version 4 Authorization header"})
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/invoke-with-response-stream"):
			streaming(w, r)
		case strings.HasSuffix(r.URL.Path, "/invoke"):
			if strings.Contains(r.URL.EscapedPath(), "us.anthropic.claude-sonnet-4-6") {
				writeJSONResp(w, 200, map[string]any{"content": []any{map[string]any{"type": "text", "text": "pong"}}})
				return
			}
			writeJSONResp(w, 403, map[string]any{"__type": "AccessDeniedException", "message": "Model is not enabled for this Quilr API key"})
		default:
			http.NotFound(w, r)
		}
	})
	s.t.Setenv("TEST_QUILR_KEY", testKey)
	s.ok("profile", "add", "qb", "--type", "quilr-bedrock", "--base-url", g.URL+"/bedrock-runtime",
		"--sonnet", "us.anthropic.claude-sonnet-4-6", "--key-env", "TEST_QUILR_KEY")
	s.ok("use", "qb", "--yes")
	return s, g
}

func eventStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
	w.WriteHeader(200)
	for i := 0; i < 4; i++ {
		_, _ = w.Write([]byte("\x00\x00\x00\x40:event-type chunk {\"bytes\":\"...\"}"))
		w.(http.Flusher).Flush()
		time.Sleep(5 * time.Millisecond)
	}
}

func TestQuilrBedrockUseWritesBedrockSettings(t *testing.T) {
	s, _ := setupBedrockDoctor(t, eventStream)
	got := s.readJSON(s.userPath())
	env := envOf(got)
	for k, want := range map[string]string{"CLAUDE_CODE_USE_BEDROCK": "1", "AWS_REGION": "us-east-1",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "us.anthropic.claude-sonnet-4-6"} {
		if v, _ := env.GetString(k); v != want {
			t.Errorf("env.%s = %q, want %q", k, v, want)
		}
	}
	if v, _ := env.GetString("ANTHROPIC_BEDROCK_BASE_URL"); !strings.HasSuffix(v, "/bedrock-runtime") {
		t.Errorf("base url %q", v)
	}
	if env.Has("ANTHROPIC_BASE_URL") || env.Has("AWS_ACCESS_KEY_ID") || got.Has("apiKeyHelper") {
		t.Fatal("unexpected keys")
	}
	if h, _ := got.GetString("awsCredentialExport"); !strings.HasSuffix(h, " aws-credentials qb") {
		t.Fatalf("awsCredentialExport = %q", h)
	}
	raw, _ := os.ReadFile(s.userPath())
	assertNoKey(t, string(raw))
	r := s.ok("aws-credentials", "qb")
	if r.out != `{"Credentials":{"AccessKeyId":"`+testKey+`","SecretAccessKey":"`+testKey+`"}}` {
		t.Fatalf("credentials output %q", r.out)
	}
	// Switching to a quilr profile clears every Bedrock key.
	s.addQuilr("qi")
	s.ok("use", "qi", "--yes")
	got = s.readJSON(s.userPath())
	if envOf(got).Has("CLAUDE_CODE_USE_BEDROCK") || envOf(got).Has("ANTHROPIC_BEDROCK_BASE_URL") || got.Has("awsCredentialExport") {
		t.Fatalf("stale bedrock keys: %s", jsonString(got))
	}
}

func TestQuilrBedrockDoctorPasses(t *testing.T) {
	s, g := setupBedrockDoctor(t, eventStream)
	rep, r := s.doctor("qb")
	if r.code != ExitOK {
		t.Fatalf("exit %d\n%s", r.code, r.out)
	}
	for id, want := range map[int]string{3: "pass", 4: "pass", 5: "skip", 6: "skip", 7: "pass", 8: "pass"} {
		if c := rep.check(id); c.Status != want {
			t.Errorf("check %d: %+v", id, c)
		}
	}
	for _, req := range g.requests {
		if req.Header.Get("X-Conversation-Id") != rep.Tag {
			t.Error("missing tag")
		}
	}
}

func TestQuilrBedrockDoctorStreamingDisabled(t *testing.T) {
	s, _ := setupBedrockDoctor(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSONResp(w, 400, map[string]any{"message": "Bedrock boto3 streaming is not enabled on this gateway yet. Use non-streaming converse/invoke_model for now."})
	})
	rep, r := s.doctor("qb")
	c := rep.check(4)
	if r.code != ExitDoctorFailed || c.Status != "fail" || !strings.Contains(c.Fix, "until Quilr enables Bedrock streaming") {
		t.Fatalf("%+v", c)
	}
}

func TestQuilrBedrockDoctorModelNotEnabled(t *testing.T) {
	s, _ := setupBedrockDoctor(t, eventStream)
	s.ok("pin", "qb", "--sonnet", "us.anthropic.claude-other-v1:0")
	rep, _ := s.doctor("qb")
	if c := rep.check(3); c.Status != "fail" || !strings.Contains(c.Fix, "tether pin qb") {
		t.Fatalf("%+v", c)
	}
}
