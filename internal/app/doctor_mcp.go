package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gurmukhnishansingh-quilr/quilr-tether/internal/ojson"
)

// mcpProtocolVersion is what tether offers in initialize; the gateway may
// answer with another version, which later requests then use.
const mcpProtocolVersion = "2025-06-18"

type mcpDoctorRun struct {
	m       *MCPServer
	timeout time.Duration
	key     string
	session string
	version string
}

// --- local checks ------------------------------------------------------------

func (d *mcpDoctorRun) checkConfig() Check {
	ch := Check{ID: 1, Name: "claude config"}
	servers, path, err := loadUserMCPServers()
	if err != nil {
		ch.Status, ch.Detail = "fail", Redact(err.Error())
		return ch
	}
	switch mcpState(d.m, servers) {
	case "configured":
		ch.Status, ch.Detail = "pass", fmt.Sprintf("%q is in %s (user scope)", d.m.Name, path)
	case "differs":
		ch.Status, ch.Detail = "warn", fmt.Sprintf("%q in %s was edited by hand or is stale", d.m.Name, path)
		ch.Fix = "tether mcp use " + d.m.Name
	case "foreign":
		ch.Status, ch.Detail = "fail", fmt.Sprintf("%s has a different MCP server named %q", path, d.m.Name)
		ch.Fix = "tether mcp use " + d.m.Name + " --force"
	default:
		ch.Status, ch.Detail = "fail", fmt.Sprintf("%q is not in %s", d.m.Name, path)
		ch.Fix = "tether mcp use " + d.m.Name
	}
	return ch
}

func (d *mcpDoctorRun) checkCredentials() Check {
	ch := Check{ID: 2, Name: "credentials"}
	if !d.m.NeedsKey() {
		ch.Status, ch.Detail = "skip", "oauth: Claude Code signs in through /mcp"
		return ch
	}
	key, err := ReadKey(d.m.secretID(), d.m.KeyBackend)
	if err != nil {
		ch.Status, ch.Detail = "fail", err.Error()
		return ch
	}
	if key == "" {
		ch.Status, ch.Detail, ch.Fix = "fail", "no token stored", "tether mcp set-key "+d.m.Name
		return ch
	}
	d.key = key
	ch.Status = "pass"
	ch.Detail = fmt.Sprintf("token %s (%s) via headersHelper", Mask(key), d.m.KeyBackend)
	if d.m.Email != "" {
		ch.Detail += ", mcpuser " + d.m.Email
	}
	return ch
}

// checkPolicy mirrors Claude Code's managed MCP controls: a managed-mcp.json
// takes exclusive control (user servers don't load), and allowedMcpServers /
// deniedMcpServers in managed settings filter by name or URL pattern.
func (d *mcpDoctorRun) checkPolicy() Check {
	ch := Check{ID: 3, Name: "managed policy"}
	exclusive := filepath.Join(ManagedDir(), "managed-mcp.json")
	if _, err := os.Stat(exclusive); err == nil {
		ch.Status = "fail"
		ch.Detail = exclusive + " exists, so Claude Code loads only the MCP servers it defines"
		ch.Fix = "ask your admin to add the server to managed-mcp.json"
		return ch
	}
	path, _ := SettingsPath("managed", "")
	managed, err := LoadSettings(path) // a missing file loads as {}
	if err != nil {
		ch.Status, ch.Detail = "warn", Redact(err.Error())
		return ch
	}
	name, u := d.m.Name, d.m.URL()
	if deny, ok := managed.Get("deniedMcpServers"); ok && mcpPolicyMatch(deny, name, u) {
		ch.Status, ch.Detail = "fail", "blocked by deniedMcpServers in "+path
		ch.Fix = "ask your admin to allow " + u
		return ch
	}
	if allow, ok := managed.Get("allowedMcpServers"); ok && !mcpPolicyMatch(allow, name, u) {
		ch.Status, ch.Detail = "fail", "not in allowedMcpServers in "+path
		ch.Fix = "ask your admin to allow " + u
		return ch
	}
	ch.Status, ch.Detail = "pass", "not blocked by managed MCP settings"
	return ch
}

// mcpPolicyMatch reports whether any allowedMcpServers/deniedMcpServers entry
// matches. serverCommand entries never match a remote server.
func mcpPolicyMatch(list any, name, url string) bool {
	arr, _ := list.([]any)
	for _, item := range arr {
		e, ok := item.(*ojson.Object)
		if !ok {
			continue
		}
		if n, ok := e.GetString("serverName"); ok && n == name {
			return true
		}
		if p, ok := e.GetString("serverUrl"); ok && wildcardMatch(p, url) {
			return true
		}
	}
	return false
}

// wildcardMatch: '*' is any run of characters, '?' one character; matching is
// case-insensitive (good enough for scheme and host, which is what matters).
func wildcardMatch(pattern, s string) bool {
	re := regexp.QuoteMeta(pattern)
	re = strings.ReplaceAll(re, `\*`, ".*")
	re = strings.ReplaceAll(re, `\?`, ".")
	ok, _ := regexp.MatchString("(?i)^"+re+"$", s)
	return ok
}

// --- gateway checks ------------------------------------------------------------

func (d *mcpDoctorRun) headers() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json, text/event-stream")
	// The gateway can scope a token to an agent by a User-Agent keyword. Claude
	// Code sends "claude-cli/<version> (...)", so send the same keyword or a
	// token scoped to Claude Code is refused here but works in Claude Code.
	h.Set("User-Agent", "claude-cli (tether/"+Version+"; mcp doctor)")
	if d.session != "" {
		h.Set("Mcp-Session-Id", d.session)
	}
	if d.version != "" {
		h.Set("MCP-Protocol-Version", d.version)
	}
	if d.key != "" {
		for k, v := range d.m.Headers(d.key) {
			h.Set(k, v)
		}
	}
	return h
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// parseRPC reads a JSON-RPC response from a plain JSON body or from an SSE
// stream (Streamable HTTP may answer either way).
func parseRPC(r *Response) (*rpcResponse, error) {
	if strings.Contains(r.Header.Get("Content-Type"), "text/event-stream") {
		sc := bufio.NewScanner(bytes.NewReader(r.Body))
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		var data strings.Builder
		flush := func() (*rpcResponse, bool) {
			defer data.Reset()
			var out rpcResponse
			if data.Len() == 0 || json.Unmarshal([]byte(data.String()), &out) != nil {
				return nil, false
			}
			return &out, out.Result != nil || out.Error != nil
		}
		for sc.Scan() {
			line := sc.Text()
			if v, ok := strings.CutPrefix(line, "data:"); ok {
				data.WriteString(strings.TrimPrefix(v, " "))
			} else if line == "" {
				if out, ok := flush(); ok {
					return out, nil
				}
			}
		}
		if out, ok := flush(); ok {
			return out, nil
		}
		return nil, fmt.Errorf("no JSON-RPC response in the event stream")
	}
	var out rpcResponse
	if err := json.Unmarshal(r.Body, &out); err != nil || (out.Result == nil && out.Error == nil) {
		return nil, fmt.Errorf("response is not JSON-RPC: %s", truncate(strings.TrimSpace(string(r.Body)), 200))
	}
	return &out, nil
}

func rpcRequest(id int, method string, params any) map[string]any {
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	return req
}

// call posts one JSON-RPC request. A non-nil Check means the call failed and
// that check describes why.
func (d *mcpDoctorRun) call(ch Check, body map[string]any) (*Response, *rpcResponse, *Check) {
	resp, err := httpCall("POST", d.m.URL(), d.headers(), body, d.timeout)
	if err != nil {
		ch.Status, ch.Detail = "fail", "cannot reach the gateway: "+Redact(err.Error())
		ch.Fix = "check the URL and your network/proxy"
		return nil, nil, &ch
	}
	if resp.Status >= 300 && resp.Status < 400 {
		ch.Status, ch.Detail = "fail", fmt.Sprintf("HTTP %d redirect to %s", resp.Status, resp.Location)
		ch.Fix = "use the exact URL from the MCP card in the Quilr dashboard"
		return nil, nil, &ch
	}
	if resp.Status == 401 || resp.Status == 403 {
		ch.Status = "fail"
		ch.Detail = fmt.Sprintf("HTTP %d: %s", resp.Status, Redact(resp.ErrorMessage()))
		ch.Fix = fmt.Sprintf("check the token (tether mcp set-key %s)", d.m.Name)
		if strings.Contains(strings.ToLower(resp.ErrorMessage()), "user-agent") {
			ch.Fix = fmt.Sprintf("the token is scoped to another agent; create one for Claude Code in Quilr (Settings → API Tokens) and run tether mcp set-key %s", d.m.Name)
		} else if d.m.Email != "" {
			ch.Fix += " and that " + d.m.Email + " is on an allowed company domain"
		}
		return resp, nil, &ch
	}
	if resp.Status != 200 {
		ch.Status, ch.Detail = "fail", fmt.Sprintf("HTTP %d: %s", resp.Status, Redact(resp.ErrorMessage()))
		if resp.Status == 404 {
			ch.Fix = fmt.Sprintf("no MCP with slug %q here; copy the URL from the MCP card in the Quilr dashboard", d.m.Slug)
		}
		return resp, nil, &ch
	}
	rpc, err := parseRPC(resp)
	if err != nil {
		ch.Status, ch.Detail = "fail", Redact(err.Error())
		return resp, nil, &ch
	}
	if rpc.Error != nil {
		ch.Status, ch.Detail = "fail", fmt.Sprintf("JSON-RPC error %d: %s", rpc.Error.Code, Redact(rpc.Error.Message))
		return resp, rpc, &ch
	}
	return resp, rpc, nil
}

// checkInitialize returns ok=false when later protocol checks can't run.
func (d *mcpDoctorRun) checkInitialize() (Check, bool) {
	ch := Check{ID: 4, Name: "initialize"}
	if d.m.NeedsKey() && d.key == "" {
		ch.Status, ch.Detail = "skip", "no token to authenticate with"
		return ch, false
	}
	resp, rpc, fail := d.call(ch, rpcRequest(1, "initialize", map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "tether", "version": Version},
	}))
	if fail != nil {
		// OAuth servers answer an anonymous request with 401: reachable, and
		// Claude Code takes it from there.
		if !d.m.NeedsKey() && resp != nil && resp.Status == 401 {
			ch.Status = "pass"
			ch.Detail = fmt.Sprintf("gateway reachable in %s; it asks for OAuth sign-in", ms(resp.Elapsed))
			ch.Fix = ""
			return ch, false
		}
		return *fail, false
	}
	d.session = resp.Header.Get("Mcp-Session-Id")
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	_ = json.Unmarshal(rpc.Result, &init)
	d.version = init.ProtocolVersion
	ch.Status = "pass"
	ch.Detail = fmt.Sprintf("%s in %s", d.m.URL(), ms(resp.Elapsed))
	if init.ServerInfo.Name != "" {
		ch.Detail += fmt.Sprintf(" (%s %s, protocol %s)", init.ServerInfo.Name, init.ServerInfo.Version, init.ProtocolVersion)
	}
	return ch, true
}

func (d *mcpDoctorRun) checkTools() Check {
	ch := Check{ID: 5, Name: "tools/list"}
	// The initialized notification has no response body worth checking.
	_, _ = httpCall("POST", d.m.URL(), d.headers(),
		map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}, d.timeout)
	resp, rpc, fail := d.call(ch, rpcRequest(2, "tools/list", nil))
	if fail != nil {
		return *fail
	}
	var list struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(rpc.Result, &list); err != nil {
		ch.Status, ch.Detail = "fail", "tools/list result is not a tool list"
		return ch
	}
	names := make([]string, 0, len(list.Tools))
	for _, t := range list.Tools {
		names = append(names, t.Name)
	}
	ch.Data = map[string]any{"tools": names}
	more := ""
	if list.NextCursor != "" {
		more = " (first page; more available)"
	}
	if len(names) == 0 {
		ch.Status = "warn"
		ch.Detail = fmt.Sprintf("no tools in %s%s", ms(resp.Elapsed), more)
		ch.Fix = "check Tools Management and Access Control for this MCP in the Quilr dashboard"
		return ch
	}
	ch.Status = "pass"
	ch.Detail = fmt.Sprintf("%d tools in %s%s", len(names), ms(resp.Elapsed), more)
	return ch
}

func (d *mcpDoctorRun) run() []Check {
	checks := []Check{d.checkConfig(), d.checkCredentials(), d.checkPolicy()}
	init, ok := d.checkInitialize()
	checks = append(checks, init)
	switch {
	case ok:
		checks = append(checks, d.checkTools())
	case init.Status == "pass": // OAuth sign-in pending
		checks = append(checks, Check{ID: 5, Name: "tools/list", Status: "skip",
			Detail: "needs OAuth sign-in: restart Claude Code and run /mcp"})
	default:
		checks = append(checks, Check{ID: 5, Name: "tools/list", Status: "skip", Detail: "initialize failed"})
	}
	return checks
}

func cmdMCPDoctor(c *Ctx, args []string) error {
	fs := newFlagSet(c, "mcp doctor", "mcp doctor <name> [--json] [--timeout SECONDS]")
	asJSON := fs.Bool("json", false, "machine-readable output")
	timeout := fs.Float64("timeout", 30, "per-request timeout in seconds")
	pos, err := parseArgs(c, fs, args, 1, 1)
	if err != nil {
		return err
	}
	if err := requireUserScope(c); err != nil {
		return err
	}
	m, err := GetMCPServer(pos[0])
	if err != nil {
		return err
	}
	d := &mcpDoctorRun{m: m, timeout: time.Duration(*timeout * float64(time.Second))}
	started := time.Now()
	checks := d.run()
	failed := false
	for _, ch := range checks {
		if ch.Status == "fail" {
			failed = true
		}
	}
	if *asJSON {
		c.UI.JSON(map[string]any{"mcp": m.Name, "url": m.URL(), "ok": !failed,
			"elapsed_ms": time.Since(started).Milliseconds(), "checks": checks})
	} else {
		c.UI.Println(c.UI.C(fmt.Sprintf("tether mcp doctor: %s (%s, %s auth)", m.Name, m.URL(), m.Auth), "bold"))
		printChecks(c, checks)
	}
	if failed {
		return &Error{Code: ExitDoctorFailed}
	}
	return nil
}
