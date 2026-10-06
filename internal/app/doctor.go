package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	fallbackSonnet = "claude-sonnet-4-5"
	betaProbe      = "interleaved-thinking-2025-05-14"
	streamPrompt   = "Count from 1 to 30, separated by spaces."
)

type Check struct {
	ID     int            `json:"id"`
	Name   string         `json:"name"`
	Status string         `json:"status"` // pass | warn | fail | skip
	Detail string         `json:"detail"`
	Fix    string         `json:"fix,omitempty"`
	Data   map[string]any `json:"data,omitempty"`
}

type doctorRun struct {
	p         *Profile
	scope     string
	project   string
	timeout   time.Duration
	tag       string
	key       string
	discovery *Discovery
}

func ms(d time.Duration) string { return fmt.Sprintf("%d ms", d.Milliseconds()) }

func worst(issues []Issue) string {
	for _, i := range issues {
		if i.Level == "fail" {
			return "fail"
		}
	}
	return "warn"
}

func joinIssues(issues []Issue) string {
	msgs := make([]string, len(issues))
	for i, is := range issues {
		msgs[i] = is.Message
	}
	return strings.Join(msgs, "; ")
}

// --- local checks ------------------------------------------------------------

func (d *doctorRun) checkSettings(states []ScopeState) Check {
	for _, st := range states {
		if st.Error != "" {
			return Check{ID: 1, Name: "settings files", Status: "fail", Detail: st.Error, Fix: "fix the JSON by hand or run `tether restore`"}
		}
	}
	if issues := FindConflicts(ComputeEffective(states)); len(issues) > 0 {
		return Check{ID: 1, Name: "settings files", Status: worst(issues), Detail: joinIssues(issues), Fix: issues[0].Fix,
			Data: map[string]any{"issues": issues}}
	}
	var present []string
	applied := false
	for _, st := range states {
		if st.Data != nil {
			present = append(present, st.Scope)
		}
		if st.Scope == d.scope && st.Data != nil {
			applied = DetectActive(st.Data, d.scope, map[string]*Profile{d.p.Name: d.p}) == d.p.Name
		}
	}
	if !applied {
		return Check{ID: 1, Name: "settings files", Status: "warn",
			Detail: fmt.Sprintf("all files parse, but %s scope does not contain profile %q", d.scope, d.p.Name),
			Fix:    fmt.Sprintf("tether use %s --scope %s", d.p.Name, d.scope)}
	}
	return Check{ID: 1, Name: "settings files", Status: "pass",
		Detail: fmt.Sprintf("valid JSON (%s); no provider conflicts", strings.Join(present, ", "))}
}

func (d *doctorRun) checkPrecedence(states []ScopeState) Check {
	issues := append(ShellShadowing(ComputeEffective(states)), Outranking(states, d.scope)...)
	if len(issues) == 0 {
		return Check{ID: 2, Name: "precedence", Status: "pass",
			Detail: fmt.Sprintf("nothing outranks %s scope; no provider variables in the shell", d.scope)}
	}
	return Check{ID: 2, Name: "precedence", Status: worst(issues), Detail: joinIssues(issues), Fix: issues[0].Fix,
		Data: map[string]any{"issues": issues}}
}

// --- gateway checks ------------------------------------------------------------

func (d *doctorRun) headers(extra map[string]string) http.Header {
	h := d.p.HeaderMap()
	h["X-Conversation-Id"] = d.tag
	for k, v := range extra {
		h[k] = v
	}
	return buildHeaders(d.key, h, false)
}

func pingBody(model string, maxTokens int, text string) map[string]any {
	return map[string]any{"model": model, "max_tokens": maxTokens,
		"messages": []any{map[string]any{"role": "user", "content": text}}}
}

func (d *doctorRun) pickModel() string {
	if m, ok := d.p.Pins["sonnet"]; ok {
		return m
	}
	if d.discovery != nil && d.discovery.Error == "" {
		kept, _ := ClaudeFilter(d.discovery.IDs())
		var sonnets []string
		for _, m := range kept {
			if strings.Contains(strings.ToLower(m), "sonnet") {
				sonnets = append(sonnets, m)
			}
		}
		if len(sonnets) > 0 {
			sort.Sort(sort.Reverse(sort.StringSlice(sonnets)))
			return sonnets[0]
		}
	}
	return fallbackSonnet
}

func (d *doctorRun) checkDiscovery() Check {
	budget := 3 * time.Second
	if d.p.DiscoveryTimeoutMS > 0 {
		budget = time.Duration(d.p.DiscoveryTimeoutMS) * time.Millisecond
	}
	level, suffix := "fail", ""
	if !d.p.DiscoveryOn() {
		level, suffix = "warn", " (discovery is off for this profile)"
	}
	extra := d.p.HeaderMap()
	extra["X-Conversation-Id"] = d.tag
	disc, err := Discover(d.p.GatewayRoot(), d.key, extra, max(d.timeout, budget))
	if err != nil {
		return Check{ID: 7, Name: "model discovery", Status: level, Detail: "GET /v1/models failed: " + err.Error() + suffix,
			Fix: "check network / proxy settings"}
	}
	d.discovery = disc
	if disc.Status >= 300 && disc.Status < 400 {
		return Check{ID: 7, Name: "model discovery", Status: level,
			Detail: fmt.Sprintf("/v1/models answered %d %s; Claude Code treats any redirect as failure%s", disc.Status, disc.Error, suffix),
			Fix:    "serve /v1/models directly at the base URL (no http->https or path redirects)"}
	}
	if disc.Error != "" {
		fix := "check the gateway's /v1/models route"
		if disc.Status == 401 || disc.Status == 403 {
			fix = "the key may lack model access"
		}
		return Check{ID: 7, Name: "model discovery", Status: level, Detail: fmt.Sprintf("HTTP %d: %s%s", disc.Status, disc.Error, suffix), Fix: fix}
	}
	kept, dropped := ClaudeFilter(disc.IDs())
	data := map[string]any{"latency_ms": disc.Elapsed.Milliseconds(), "budget_ms": budget.Milliseconds(),
		"picker": nonNil(kept), "dropped": nonNil(dropped)}
	shown := strings.Join(kept, ", ")
	if shown == "" {
		shown = "nothing"
	}
	detail := fmt.Sprintf("%d models in %s (budget %s); /model will show: %s", len(disc.Entries), ms(disc.Elapsed), ms(budget), shown)
	switch {
	case disc.Elapsed > budget:
		return Check{ID: 7, Name: "model discovery", Status: level,
			Detail: detail + fmt.Sprintf("; too slow, Claude Code gives up after %s%s", ms(budget), suffix),
			Fix:    fmt.Sprintf("raise the timeout: tether profile add %s --force --discovery-timeout-ms %d ...", d.p.Name, disc.Elapsed.Milliseconds()*3/2),
			Data:   data}
	case len(kept) == 0:
		return Check{ID: 7, Name: "model discovery", Status: level, Detail: detail + suffix,
			Fix: "no id contains 'claude' or 'anthropic'; pin models with `tether pin`", Data: data}
	case len(dropped) > 0:
		return Check{ID: 7, Name: "model discovery", Status: "warn",
			Detail: detail + "; dropped by Claude Code's filter: " + strings.Join(dropped, ", "),
			Fix:    "ids without 'claude'/'anthropic' never reach /model; use `tether override` or ANTHROPIC_CUSTOM_MODEL_OPTION",
			Data:   data}
	}
	return Check{ID: 7, Name: "model discovery", Status: "pass", Detail: detail, Data: data}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (d *doctorRun) checkInference(model string) Check {
	resp, err := httpCall("POST", d.p.GatewayRoot()+"/v1/messages", d.headers(nil), pingBody(model, 16, "ping"), d.timeout)
	if err != nil {
		return Check{ID: 3, Name: "inference", Status: "fail", Detail: "POST /v1/messages: " + err.Error(),
			Fix: "check DNS, proxy (HTTPS_PROXY) and the region"}
	}
	data := map[string]any{"status": resp.Status, "latency_ms": resp.Elapsed.Milliseconds(), "model": model}
	if resp.Status == 200 {
		return Check{ID: 3, Name: "inference", Status: "pass", Detail: fmt.Sprintf("%s: 200 in %s", model, ms(resp.Elapsed)), Data: data}
	}
	fixes := map[int]string{
		400: "pin a model the key can use: `tether pin <name> --sonnet ID`",
		401: "the gateway rejected the key; replace it with `tether profile set-key <name>`",
		403: "the key is not allowed to use this model or route",
		404: "wrong region or base URL; Quilr's route ends in /anthropic_messages",
		429: "rate limited; retry later",
	}
	fix, ok := fixes[resp.Status]
	if !ok {
		fix = "see the gateway logs"
	}
	return Check{ID: 3, Name: "inference", Status: "fail",
		Detail: fmt.Sprintf("%s: HTTP %d: %s", model, resp.Status, resp.ErrorMessage()), Fix: fix, Data: data}
}

func (d *doctorRun) checkStreaming(model string) Check {
	res, err := httpStream(d.p.GatewayRoot()+"/v1/messages", d.headers(nil), pingBody(model, 64, streamPrompt), d.timeout)
	if err != nil {
		return Check{ID: 4, Name: "streaming", Status: "fail", Detail: "stream request failed: " + err.Error(),
			Fix: "check proxy and gateway streaming support"}
	}
	data := map[string]any{"status": res.Status, "content_type": res.ContentType, "events": len(res.EventTimes),
		"total_ms": res.Total.Milliseconds()}
	if res.FirstEvent >= 0 {
		data["first_event_ms"] = res.FirstEvent.Milliseconds()
	}
	if res.Status != 200 {
		return Check{ID: 4, Name: "streaming", Status: "fail", Detail: fmt.Sprintf("HTTP %d: %s", res.Status, res.Error),
			Fix: "see the gateway logs", Data: data}
	}
	if !strings.Contains(res.ContentType, "text/event-stream") {
		return Check{ID: 4, Name: "streaming", Status: "fail",
			Detail: fmt.Sprintf("content-type is %q, expected text/event-stream", res.ContentType),
			Fix:    "the gateway must relay SSE unchanged", Data: data}
	}
	if res.FirstEvent < 0 {
		return Check{ID: 4, Name: "streaming", Status: "fail", Detail: "no SSE events received",
			Fix: "the gateway must relay SSE unchanged", Data: data}
	}
	span := res.EventTimes[len(res.EventTimes)-1] - res.FirstEvent
	detail := fmt.Sprintf("first event after %s, %d events over %s", ms(res.FirstEvent), len(res.EventTimes), ms(span))
	// A buffering gateway delivers every event in one burst at the very end.
	if len(res.EventTimes) >= 4 && res.Total > 500*time.Millisecond && span < 50*time.Millisecond &&
		res.FirstEvent > res.Total*8/10 {
		return Check{ID: 4, Name: "streaming", Status: "warn",
			Detail: detail + "; all events arrived at once, so the gateway appears to buffer",
			Fix:    "disable response buffering for /v1/messages (Claude Code stalls on buffered streams)", Data: data}
	}
	if res.FirstEvent > 10*time.Second {
		return Check{ID: 4, Name: "streaming", Status: "warn", Detail: detail + "; slow first event", Fix: "check gateway queueing", Data: data}
	}
	if !contains(res.EventNames, "message_stop") && !contains(res.EventNames, "message_delta") {
		return Check{ID: 4, Name: "streaming", Status: "warn", Detail: detail + "; stream ended without message_delta/message_stop",
			Fix: "relay the full event sequence", Data: data}
	}
	return Check{ID: 4, Name: "streaming", Status: "pass", Detail: detail, Data: data}
}

func (d *doctorRun) checkBeta(model string) Check {
	resp, err := httpCall("POST", d.p.GatewayRoot()+"/v1/messages", d.headers(map[string]string{"anthropic-beta": betaProbe}),
		pingBody(model, 16, "ping"), d.timeout)
	if err != nil {
		return Check{ID: 5, Name: "beta passthrough", Status: "fail", Detail: err.Error(), Fix: "check network"}
	}
	switch resp.Status {
	case 200:
		return Check{ID: 5, Name: "beta passthrough", Status: "pass", Detail: "anthropic-beta: " + betaProbe + " accepted"}
	case 400:
		if d.p.BedrockBacked {
			return Check{ID: 5, Name: "beta passthrough", Status: "warn",
				Detail: fmt.Sprintf("400 with anthropic-beta (%s); acceptable because this profile sets CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS", resp.ErrorMessage())}
		}
		return Check{ID: 5, Name: "beta passthrough", Status: "fail", Detail: "400 with anthropic-beta: " + resp.ErrorMessage(),
			Fix: fmt.Sprintf("forward anthropic-beta verbatim, or re-add the profile with --bedrock-backed (tether profile add %s --force ...)", d.p.Name)}
	}
	return Check{ID: 5, Name: "beta passthrough", Status: "warn", Detail: fmt.Sprintf("HTTP %d: %s", resp.Status, resp.ErrorMessage()),
		Fix: "see the gateway logs"}
}

func (d *doctorRun) checkCountTokens(model string) Check {
	body := map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": "ping"}}}
	resp, err := httpCall("POST", d.p.GatewayRoot()+"/v1/messages/count_tokens", d.headers(nil), body, d.timeout)
	if err != nil {
		return Check{ID: 6, Name: "token counting", Status: "warn", Detail: err.Error(), Fix: "optional; Claude Code estimates instead"}
	}
	if resp.Status == 200 {
		var r struct {
			InputTokens *int `json:"input_tokens"`
		}
		_ = json.Unmarshal(resp.Body, &r)
		n := "?"
		if r.InputTokens != nil {
			n = fmt.Sprint(*r.InputTokens)
		}
		return Check{ID: 6, Name: "token counting", Status: "pass", Detail: "count_tokens works (" + n + " input tokens)"}
	}
	note := fmt.Sprintf("HTTP %d: %s", resp.Status, resp.ErrorMessage())
	if resp.Status == 404 || resp.Status == 405 {
		note = "endpoint not implemented"
	}
	return Check{ID: 6, Name: "token counting", Status: "warn", Detail: note + "; optional, /context will show estimates",
		Fix: "expose /v1/messages/count_tokens for exact counts"}
}

func (d *doctorRun) checkModelsEnabled() Check {
	type want struct{ label, id string }
	var wanted []want
	for _, t := range Tiers {
		if id, ok := d.p.Pins[t]; ok {
			wanted = append(wanted, want{"pin:" + t, id})
		}
	}
	for _, a := range sortedKeys(d.p.ModelOverrides) {
		wanted = append(wanted, want{"override:" + a, d.p.ModelOverrides[a]})
	}
	if len(wanted) == 0 {
		return Check{ID: 8, Name: "pinned models", Status: "pass", Detail: "no pins or overrides to verify"}
	}
	if d.discovery == nil || d.discovery.Error != "" {
		return Check{ID: 8, Name: "pinned models", Status: "skip", Detail: "discovery failed, cannot verify pins", Fix: "fix check 7 first"}
	}
	ids := d.discovery.IDs()
	var missing []string
	for _, w := range wanted {
		if !contains(ids, w.id) {
			missing = append(missing, fmt.Sprintf("%s (%s)", w.id, w.label))
		}
	}
	if len(missing) > 0 {
		return Check{ID: 8, Name: "pinned models", Status: "warn", Detail: "not listed for this key: " + strings.Join(missing, ", "),
			Fix: "enable them on the key in Quilr, or change the pin/override", Data: map[string]any{"missing": missing}}
	}
	return Check{ID: 8, Name: "pinned models", Status: "pass", Detail: fmt.Sprintf("all %d pinned/overridden ids are enabled", len(wanted))}
}

// --- runner ----------------------------------------------------------------------

func (d *doctorRun) run() ([]Check, error) {
	states := LoadScopes(d.project)
	checks := []Check{d.checkSettings(states), d.checkPrecedence(states)}
	if !d.p.IsQuilr() {
		return append(checks, nonGatewayChecks(d.p)...), nil
	}
	key, err := ReadKey(d.p.Name, d.p.KeyBackend)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return append(checks, Check{ID: 3, Name: "inference", Status: "fail", Detail: "no API key stored for this profile",
			Fix: fmt.Sprintf("tether profile set-key %s", d.p.Name)}), nil
	}
	d.key = key
	if d.p.Type == "quilr-bedrock" {
		checks = append(checks, d.bedrockChecks()...)
		sort.SliceStable(checks, func(i, j int) bool { return checks[i].ID < checks[j].ID })
		collapseKeyProblems(checks, d.p.Name)
		return checks, nil
	}
	discovery := d.checkDiscovery() // first, so the ping can use a discovered model
	model := d.pickModel()
	checks = append(checks, d.checkInference(model), d.checkStreaming(model), d.checkBeta(model),
		d.checkCountTokens(model), discovery, d.checkModelsEnabled(),
		Check{ID: 9, Name: "log tag", Status: "pass", Detail: "requests tagged X-Conversation-Id: " + d.tag,
			Fix: "search the Quilr logs for this id"})
	sort.SliceStable(checks, func(i, j int) bool { return checks[i].ID < checks[j].ID })
	collapseKeyProblems(checks, d.p.Name)
	return checks, nil
}

var lookPath = exec.LookPath

func nonGatewayChecks(p *Profile) []Check {
	if p.Type == "bedrock" {
		out := []Check{{ID: 3, Name: "gateway checks", Status: "skip", Detail: "Bedrock profile: no gateway to probe"}}
		if p.SSORefresh {
			if _, err := lookPath("aws"); err != nil {
				out = append(out, Check{ID: 10, Name: "aws cli", Status: "fail",
					Detail: "awsAuthRefresh needs the aws CLI, which is not on PATH", Fix: "install AWS CLI v2"})
			} else {
				out = append(out, Check{ID: 10, Name: "aws cli", Status: "pass", Detail: "aws CLI found for awsAuthRefresh"})
			}
		}
		return out
	}
	return []Check{{ID: 3, Name: "gateway checks", Status: "skip", Detail: "anthropic profile: Claude Code talks to Anthropic directly"}}
}

func cmdDoctor(c *Ctx, args []string) error {
	fs := newFlagSet(c, "doctor", "doctor [<name>] [--scope ...] [--json] [--timeout SECONDS]")
	asJSON := fs.Bool("json", false, "machine-readable output")
	timeout := fs.Float64("timeout", 30, "per-request timeout in seconds")
	pos, err := parseArgs(c, fs, args, 0, 1)
	if err != nil {
		return err
	}
	p, err := activeOrNamed(c, pos)
	if err != nil {
		return err
	}
	d := &doctorRun{p: p, scope: c.Scope, project: c.ProjectDir,
		timeout: time.Duration(*timeout * float64(time.Second)),
		tag:     "tether-doctor-" + time.Now().UTC().Format("20060102T150405Z")}
	started := time.Now()
	checks, err := d.run()
	if err != nil {
		return err
	}
	failed := false
	for _, ch := range checks {
		if ch.Status == "fail" {
			failed = true
		}
	}
	if *asJSON {
		c.UI.JSON(map[string]any{"profile": p.Name, "tag": d.tag, "ok": !failed,
			"elapsed_ms": time.Since(started).Milliseconds(), "checks": checks})
	} else {
		c.UI.Println(c.UI.C(fmt.Sprintf("tether doctor: %s (%s)", p.Name, p.Target()), "bold"))
		printChecks(c, checks)
	}
	if failed {
		return &Error{Code: ExitDoctorFailed}
	}
	return nil
}

func printChecks(c *Ctx, checks []Check) {
	colors := map[string]string{"pass": "green", "warn": "yellow", "fail": "red", "skip": "dim"}
	counts := map[string]int{}
	for _, ch := range checks {
		counts[ch.Status]++
		c.UI.Printf(" %s  %2d. %-17s %s\n", c.UI.C(fmt.Sprintf("%-4s", strings.ToUpper(ch.Status)), colors[ch.Status]), ch.ID, ch.Name, ch.Detail)
		if ch.Fix != "" && (ch.Status == "warn" || ch.Status == "fail") {
			c.UI.Printf("%26sfix: %s\n", "", ch.Fix)
		}
	}
	c.UI.Printf("\n%d passed, %d warnings, %d failed\n", counts["pass"], counts["warn"], counts["fail"])
}

var (
	keyProviderRE = regexp.MustCompile(`configured for '([^']+)' provider`)
	keyInvalidRE  = regexp.MustCompile(`(?i)(api key is invalid|has been revoked|invalid x-api-key|invalid api key)`)
)

// keyProblem recognises gateway errors caused by the key itself rather than
// the endpoint or model, so doctor can name the real fix once.
func keyProblem(detail, profile string) (summary, fix string, ok bool) {
	setKey := "tether profile set-key " + profile
	if m := keyProviderRE.FindStringSubmatch(detail); m != nil {
		have := m[1]
		// Point at the anthropic_messages* variant for the same upstream.
		want, extra := "anthropic_messages", ""
		switch {
		case strings.Contains(strings.ToLower(have), "bedrock"):
			want = "anthropic_messages_bedrock"
			extra = fmt.Sprintf(", and mark the profile Bedrock-backed: tether profile add %s --type quilr --bedrock-backed --force ... && tether use %s --yes", profile, profile)
		case strings.Contains(strings.ToLower(have), "azure"):
			want = "anthropic_messages_azure"
		}
		return fmt.Sprintf("this API key was created in Quilr for the '%s' provider, but Claude Code uses the "+
				"/anthropic_messages route, which needs a key created for '%s'", have, want),
			fmt.Sprintf("in Quilr, create a key with provider '%s', then run: %s%s", want, setKey, extra), true
	}
	if keyInvalidRE.MatchString(detail) {
		return "the gateway says this API key is invalid or revoked",
			"check the key in Quilr, then run: " + setKey, true
	}
	return "", "", false
}

// collapseKeyProblems reports a key problem once, on the first gateway check
// that hit it, and marks later checks with the same cause as skipped.
func collapseKeyProblems(checks []Check, profile string) {
	first := -1
	for i := range checks {
		if checks[i].ID < 3 || checks[i].ID > 8 || checks[i].Status == "pass" {
			continue
		}
		summary, fix, ok := keyProblem(checks[i].Detail, profile)
		if !ok {
			continue
		}
		if first < 0 {
			first = i
			checks[i].Status = "fail"
			checks[i].Detail = summary
			checks[i].Fix = fix
			continue
		}
		checks[i].Status = "skip"
		checks[i].Detail = fmt.Sprintf("not tested: same key problem as check %d", checks[first].ID)
		checks[i].Fix = ""
	}
}
