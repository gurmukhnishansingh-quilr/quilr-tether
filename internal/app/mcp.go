package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/gurmukhnishansingh-quilr/quilr-tether/internal/ojson"
	"golang.org/x/term"
)

// Quilr MCP Gateway servers, written to Claude Code's user-scope MCP config
// (top-level mcpServers in ~/.claude.json). Only user scope is supported for
// now: project (.mcp.json), local (per-project ~/.claude.json) and managed
// (managed-mcp.json) are not.
//
// Unlike LLM profiles, MCP servers are additive: tether owns only the entries
// it created, by name, and never touches the user's other servers.

var (
	MCPDomains = map[string]string{
		"quilr.ai":    "https://mcpgateway.quilr.ai",
		"quilrai.com": "https://mcpgateway.quilrai.com",
	}
	MCPDomainNames = []string{"quilr.ai", "quilrai.com"}
	MCPAuthModes   = []string{"token", "oauth"}

	mcpSlugRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

// OneMCPSlug is the OneMCP endpoint: every MCP the user may access, behind one URL.
const OneMCPSlug = "quilrone"

// MCPServer is one entry in mcp.toml. Tokens are never stored here.
type MCPServer struct {
	Name       string `toml:"-"`
	BaseURL    string `toml:"base_url"`
	Slug       string `toml:"slug"`
	Auth       string `toml:"auth"`            // token | oauth
	Email      string `toml:"email,omitempty"` // sent as mcpuser
	KeyBackend string `toml:"key_backend,omitempty"`
}

func (m *MCPServer) URL() string { return strings.TrimRight(m.BaseURL, "/") + "/" + m.Slug + "/mcp" }

func (m *MCPServer) NeedsKey() bool { return m.Auth == "token" }

func (m *MCPServer) IsOneMCP() bool { return m.Slug == OneMCPSlug }

// secretID is the keychain account (or secrets file name) for the token.
// Profile names can't contain '.', so this never collides with an LLM profile key.
func (m *MCPServer) secretID() string { return "mcp." + m.Name }

func (m *MCPServer) Validate() error {
	if err := validateName(m.Name); err != nil {
		return err
	}
	u, err := url.Parse(m.BaseURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return usageErr("e.g. https://mcpgateway.quilr.ai", "invalid MCP gateway URL %q", m.BaseURL)
	}
	if !mcpSlugRE.MatchString(m.Slug) {
		return usageErr("copy it from the MCP card: https://<gateway>/<slug>/mcp", "invalid MCP slug %q", m.Slug)
	}
	if !contains(MCPAuthModes, m.Auth) {
		return usageErr("", "unknown auth %q; expected token or oauth", m.Auth)
	}
	if m.Email != "" && !strings.Contains(m.Email, "@") {
		return usageErr("", "invalid email %q", m.Email)
	}
	// The per-MCP token route identifies the user by mcpuser; OneMCP tokens carry the user.
	if m.NeedsKey() && m.Email == "" && !m.IsOneMCP() {
		return usageErr("pass --email", "token auth needs the user's email (sent as mcpuser)")
	}
	return nil
}

// Headers are what `tether mcp-headers` prints for Claude Code's headersHelper.
func (m *MCPServer) Headers(key string) map[string]string {
	h := map[string]string{"Authorization": "Bearer " + key}
	if m.Email != "" {
		h["mcpuser"] = m.Email
	}
	return h
}

// MCPHeadersCommand is the headersHelper value: this binary, printing the headers.
func MCPHeadersCommand(name string) string {
	return quoteArg(selfPath()) + " mcp-headers " + name
}

// Entry is the mcpServers value tether writes. Token auth goes through
// headersHelper, so ~/.claude.json never holds the token. OAuth entries have
// no headers: Claude Code signs in through /mcp.
func (m *MCPServer) Entry() *ojson.Object {
	e := ojson.NewObject()
	e.Set("type", "http")
	e.Set("url", m.URL())
	if m.NeedsKey() {
		e.Set("headersHelper", MCPHeadersCommand(m.Name))
	}
	return e
}

// ownsEntry reports whether an existing mcpServers value is one tether wrote for m.
func (m *MCPServer) ownsEntry(v any) bool {
	e, ok := v.(*ojson.Object)
	if !ok {
		return false
	}
	if h, ok := e.GetString("headersHelper"); ok && strings.HasSuffix(h, " mcp-headers "+m.Name) {
		return true
	}
	return ojson.Equal(e, m.Entry())
}

// --- storage ---------------------------------------------------------------

type mcpFile struct {
	Servers map[string]*MCPServer `toml:"servers"`
}

// MCPPath is kept apart from profiles.toml so an older tether, which rejects
// unknown fields there, keeps working.
func MCPPath() string { return filepath.Join(ConfigDir(), "mcp.toml") }

func LoadMCPServers() (map[string]*MCPServer, error) {
	path := MCPPath()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]*MCPServer{}, nil
	}
	if err != nil {
		return nil, wrapIO("read", path, err)
	}
	var mf mcpFile
	md, err := toml.Decode(string(data), &mf)
	if err != nil {
		return nil, ioErr("", "%s is not valid TOML: %v", path, err)
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		return nil, ioErr("", "%s has unknown field(s): %v", path, undec)
	}
	out := map[string]*MCPServer{}
	for name, m := range mf.Servers {
		m.Name = name
		if err := m.Validate(); err != nil {
			return nil, ioErr("", "MCP server %q in %s: %v", name, path, err)
		}
		out[name] = m
	}
	return out, nil
}

func SaveMCPServers(all map[string]*MCPServer) error {
	var buf bytes.Buffer
	buf.WriteString("# tether MCP servers. Managed by `tether mcp ...`; tokens are never stored here.\n\n")
	if err := toml.NewEncoder(&buf).Encode(mcpFile{Servers: all}); err != nil {
		return ioErr("", "cannot encode MCP servers: %v", err)
	}
	return AtomicWrite(MCPPath(), buf.Bytes(), false)
}

func GetMCPServer(name string) (*MCPServer, error) {
	all, err := LoadMCPServers()
	if err != nil {
		return nil, err
	}
	m, ok := all[name]
	if !ok {
		names := make([]string, 0, len(all))
		for n := range all {
			names = append(names, n)
		}
		sort.Strings(names)
		known := strings.Join(names, ", ")
		if known == "" {
			known = "none yet"
		}
		return nil, usageErr("create one with `tether mcp add`", "no MCP server named %q (known: %s)", name, known)
	}
	return m, nil
}

func sortedMCPNames(m map[string]*MCPServer) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func requireMCPKey(m *MCPServer) (string, error) {
	key, err := ReadKey(m.secretID(), m.KeyBackend)
	if err != nil {
		return "", err
	}
	if key == "" {
		return "", usageErr(fmt.Sprintf("store one with `tether mcp set-key %s`", m.Name),
			"no token stored for MCP server %q", m.Name)
	}
	return key, nil
}

// --- ~/.claude.json --------------------------------------------------------------

func requireUserScope(c *Ctx) error {
	if c.Scope != "user" {
		return usageErr("drop --scope; the server is written to "+ClaudeJSONPath(),
			"MCP servers support user scope only for now, not %s", c.Scope)
	}
	return nil
}

// mcpServersOf returns a copy of the mcpServers object (empty when absent).
func mcpServersOf(doc *ojson.Object, path string) (*ojson.Object, error) {
	raw, ok := doc.Get("mcpServers")
	if !ok {
		return ojson.NewObject(), nil
	}
	o, isObj := raw.(*ojson.Object)
	if !isObj {
		return nil, ioErr("", "%s: \"mcpServers\" is not an object; refusing to touch it", path)
	}
	return o.Clone(), nil
}

// withServers returns a copy of doc with mcpServers replaced, or removed when
// tether emptied it (it only ever empties a block that held its own entry).
func withServers(doc, servers *ojson.Object) *ojson.Object {
	next := doc.Clone()
	if servers.Len() == 0 {
		next.Delete("mcpServers")
	} else {
		next.Set("mcpServers", servers)
	}
	return next
}

// mcpDiff shows only mcpServers: ~/.claude.json is large and holds state
// unrelated to tether. The user's other servers may carry plaintext tokens in
// headers or env, so those are masked.
func mcpDiff(current, next *ojson.Object, path string) []string {
	view := func(doc *ojson.Object) *ojson.Object {
		o := ojson.NewObject()
		if v, ok := doc.Get("mcpServers"); ok {
			if servers, isObj := v.(*ojson.Object); isObj {
				v = MaskMCPServers(servers)
			}
			o.Set("mcpServers", v)
		}
		return o
	}
	return SettingsDiff(view(current), view(next), path)
}

func (c *Ctx) writeClaudeJSON(path string, current, next *ojson.Object) (bool, error) {
	lines := mcpDiff(current, next, path)
	if len(lines) == 0 {
		c.UI.Println(path + " already matches; nothing to write.")
		return false, nil
	}
	c.UI.PrintDiff(lines)
	ok, err := c.UI.Confirm("Write "+path+"?", c.Yes)
	if err != nil {
		return false, err
	}
	if !ok {
		c.UI.Println("Aborted; nothing written.")
		return false, nil
	}
	if err := EnsureWritable(path); err != nil {
		return false, err
	}
	backup, err := WriteSettings(path, next, "user")
	if err != nil {
		return false, err
	}
	c.UI.Println(c.UI.C("Wrote "+path, "green") + describeBackup(backup))
	return true, nil
}

type mcpPlan struct {
	server        *MCPServer
	path          string
	current, next *ojson.Object
}

func planMCPUse(name string, force bool) (*mcpPlan, error) {
	m, err := GetMCPServer(name)
	if err != nil {
		return nil, err
	}
	if m.NeedsKey() {
		if _, err := requireMCPKey(m); err != nil {
			return nil, err
		}
	}
	path := ClaudeJSONPath()
	current, err := LoadSettings(path)
	if err != nil {
		return nil, err
	}
	servers, err := mcpServersOf(current, path)
	if err != nil {
		return nil, err
	}
	if v, ok := servers.Get(m.Name); ok && !force && !m.ownsEntry(v) {
		return nil, usageErr("pick another name, or pass --force to replace it",
			"%s already has an MCP server named %q that tether did not create", path, m.Name)
	}
	servers.Set(m.Name, m.Entry())
	return &mcpPlan{m, path, current, withServers(current, servers)}, nil
}

// mcpState describes the server's entry in ~/.claude.json: "configured",
// "differs" (hand-edited or stale), "foreign" (same name, not tether's) or "absent".
func mcpState(m *MCPServer, servers *ojson.Object) string {
	v, ok := servers.Get(m.Name)
	switch {
	case !ok:
		return "absent"
	case ojson.Equal(v, m.Entry()):
		return "configured"
	case m.ownsEntry(v):
		return "differs"
	}
	return "foreign"
}

func loadUserMCPServers() (*ojson.Object, string, error) {
	path := ClaudeJSONPath()
	doc, err := LoadSettings(path)
	if err != nil {
		return nil, path, err
	}
	servers, err := mcpServersOf(doc, path)
	return servers, path, err
}

// --- commands ----------------------------------------------------------------------

func cmdMCP(c *Ctx, args []string) error {
	if len(args) == 0 {
		return usageErr("", "usage: tether mcp add|list|show|use|diff|doctor|set-key|remove ...")
	}
	switch args[0] {
	case "add":
		return cmdMCPAdd(c, args[1:])
	case "list":
		return cmdMCPList(c, args[1:])
	case "show":
		return cmdMCPShow(c, args[1:])
	case "use":
		return cmdMCPUse(c, args[1:])
	case "diff":
		return cmdMCPDiff(c, args[1:])
	case "doctor":
		return cmdMCPDoctor(c, args[1:])
	case "set-key":
		return cmdMCPSetKey(c, args[1:])
	case "remove", "rm":
		return cmdMCPRemove(c, args[1:])
	}
	return usageErr("", "unknown mcp action %q; expected add, list, show, use, diff, doctor, set-key or remove", args[0])
}

func cmdMCPAdd(c *Ctx, args []string) error {
	fs := newFlagSet(c, "mcp add", "mcp add <name> (--slug SLUG | --onemcp) [--email EMAIL] [--auth token|oauth] [options]")
	slug := fs.String("slug", "", "MCP slug from the endpoint URL https://<gateway>/<slug>/mcp")
	onemcp := fs.Bool("onemcp", false, "use the OneMCP endpoint (/"+OneMCPSlug+"/mcp)")
	domain := fs.String("domain", "", "gateway domain: quilr.ai (default) or quilrai.com")
	baseURL := fs.String("base-url", "", "explicit gateway URL instead of --domain")
	auth := fs.String("auth", "", "token (API token via headersHelper, default) or oauth (Claude Code signs in)")
	email := fs.String("email", "", "user email, sent as mcpuser")
	force := fs.Bool("force", false, "replace an existing MCP server")
	key := fs.String("key", "", "API token (visible in shell history; prefer --key-stdin)")
	keyStdin := fs.Bool("key-stdin", false, "read the API token from stdin")
	keyEnv := fs.String("key-env", "", "read the API token from this environment variable")
	pos, err := parseArgs(c, fs, args, 1, 1)
	if err != nil {
		return err
	}
	if err := requireUserScope(c); err != nil {
		return err
	}
	name := pos[0]
	if err := validateName(name); err != nil {
		return err
	}
	all, err := LoadMCPServers()
	if err != nil {
		return err
	}
	existing, exists := all[name]
	if exists && !*force {
		return usageErr("pass --force to replace it", "MCP server %q already exists", name)
	}
	keySources := 0
	for _, set := range []bool{*key != "", *keyStdin, *keyEnv != ""} {
		if set {
			keySources++
		}
	}
	if keySources > 1 {
		return usageErr("", "use only one of --key, --key-stdin, --key-env")
	}
	if *slug != "" && *onemcp {
		return usageErr("", "--slug and --onemcp are mutually exclusive")
	}
	if *domain != "" && *baseURL != "" {
		return usageErr("", "--domain and --base-url are mutually exclusive")
	}

	m := &MCPServer{Name: name, Slug: *slug, Auth: *auth, Email: *email}
	if *onemcp {
		m.Slug = OneMCPSlug
	}
	if m.Slug == "" {
		if m.Slug, err = c.UI.Ask("MCP slug (from https://<gateway>/<slug>/mcp; "+OneMCPSlug+" for OneMCP)", "--slug", "", nil, true); err != nil {
			return err
		}
	}
	switch {
	case *baseURL != "":
		m.BaseURL = strings.TrimRight(*baseURL, "/")
	case *domain != "":
		u, ok := MCPDomains[*domain]
		if !ok {
			return usageErr("", "unknown MCP gateway domain %q; expected %s", *domain, strings.Join(MCPDomainNames, ", "))
		}
		m.BaseURL = u
	default:
		m.BaseURL = MCPDomains["quilr.ai"]
	}
	if m.Auth == "" {
		m.Auth = "token"
	}
	if m.NeedsKey() && m.Email == "" && !m.IsOneMCP() {
		if m.Email, err = c.UI.Ask("User email for mcpuser", "--email", "", nil, true); err != nil {
			return err
		}
	}
	if err := m.Validate(); err != nil {
		return err
	}

	var newKey string
	if m.NeedsKey() {
		if newKey, err = readMCPKeyInput(c, *key, *keyStdin, *keyEnv); err != nil {
			return err
		}
		if newKey == "" {
			// --force without a new token keeps the stored one.
			if exists && existing.NeedsKey() {
				if k, _ := ReadKey(existing.secretID(), existing.KeyBackend); k != "" {
					m.KeyBackend = existing.KeyBackend
				}
			}
			if m.KeyBackend == "" {
				return usageErr("pass --key-stdin, --key-env VAR, or run interactively", "an MCP API token is required for --auth token")
			}
		}
	} else if keySources > 0 {
		return usageErr("drop the token, or use --auth token", "oauth MCP servers take no token; Claude Code signs in through /mcp")
	}
	if newKey != "" {
		backend, err := StoreKey(m.secretID(), newKey)
		if err != nil {
			return err
		}
		m.KeyBackend = backend
		if backend == "file" {
			c.UI.Warn("no OS keychain available; the token is stored owner-only under %s",
				filepath.Join(ConfigDir(), "secrets"))
		}
		c.UI.Println(fmt.Sprintf("Stored token %s in %s.", Mask(newKey), backend))
	}
	if exists && existing.NeedsKey() && !m.NeedsKey() {
		DeleteKey(existing.secretID(), existing.KeyBackend)
	}
	all[name] = m
	if err := SaveMCPServers(all); err != nil {
		return err
	}
	c.UI.Println(c.UI.C(fmt.Sprintf("Saved MCP server %q (%s, %s auth).", name, m.URL(), m.Auth), "green") +
		" Add it to Claude Code with: tether mcp use " + name)
	return nil
}

func readMCPKeyInput(c *Ctx, key string, fromStdin bool, envVar string) (string, error) {
	if key == "" && !fromStdin && envVar == "" && c.UI.Interactive {
		return c.UI.AskSecret("Quilr MCP API token (input hidden): ")
	}
	return readKeyInput(c, key, fromStdin, envVar)
}

func mcpSummary(m *MCPServer, servers *ojson.Object) *ojson.Object {
	o := ojson.NewObject()
	o.Set("name", m.Name)
	o.Set("url", m.URL())
	o.Set("auth", m.Auth)
	if m.Email != "" {
		o.Set("email", m.Email)
	}
	if m.NeedsKey() {
		k, _ := ReadKey(m.secretID(), m.KeyBackend)
		if k == "" {
			o.Set("key", "missing")
		} else {
			o.Set("key", Mask(k)+" ("+m.KeyBackend+")")
		}
	}
	if servers != nil {
		o.Set("claude_code", mcpState(m, servers))
	}
	return o
}

func cmdMCPList(c *Ctx, args []string) error {
	fs := newFlagSet(c, "mcp list", "mcp list [--json]")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if _, err := parseArgs(c, fs, args, 0, 0); err != nil {
		return err
	}
	if err := requireUserScope(c); err != nil {
		return err
	}
	all, err := LoadMCPServers()
	if err != nil {
		return err
	}
	servers, _, serr := loadUserMCPServers()
	if serr != nil {
		servers = nil
	}
	if *asJSON {
		arr := []any{}
		for _, n := range sortedMCPNames(all) {
			arr = append(arr, mcpSummary(all[n], servers))
		}
		c.UI.JSON(ojson.Normalize(arr))
		return nil
	}
	if len(all) == 0 {
		c.UI.Println("No MCP servers yet. Create one with `tether mcp add <name> --slug SLUG --email you@company.com`.")
		return nil
	}
	anyOn := false
	for _, n := range sortedMCPNames(all) {
		m := all[n]
		mark := " "
		if servers != nil && mcpState(m, servers) == "configured" {
			mark, anyOn = "*", true
		}
		c.UI.Printf("%s %-20s %-6s %s\n", mark, n, m.Auth, m.URL())
	}
	if anyOn {
		c.UI.Println(c.UI.C("* configured in Claude Code (user scope)", "dim"))
	}
	if serr != nil {
		c.UI.Warn("%v", serr)
	}
	return nil
}

func cmdMCPShow(c *Ctx, args []string) error {
	fs := newFlagSet(c, "mcp show", "mcp show <name> [--json]")
	asJSON := fs.Bool("json", false, "machine-readable output")
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
	servers, _, err := loadUserMCPServers()
	if err != nil {
		return err
	}
	s := mcpSummary(m, servers)
	if *asJSON {
		c.UI.JSON(s)
		return nil
	}
	for _, k := range s.Keys() {
		v, _ := s.GetString(k)
		c.UI.Printf("%-12s %s\n", k, v)
	}
	return nil
}

func cmdMCPUse(c *Ctx, args []string) error {
	fs := newFlagSet(c, "mcp use", "mcp use <name> [--force]")
	force := fs.Bool("force", false, "replace an mcpServers entry of the same name that tether did not create")
	pos, err := parseArgs(c, fs, args, 1, 1)
	if err != nil {
		return err
	}
	if err := requireUserScope(c); err != nil {
		return err
	}
	plan, err := planMCPUse(pos[0], *force)
	if err != nil {
		return err
	}
	wrote, err := c.writeClaudeJSON(plan.path, plan.current, plan.next)
	if err != nil {
		return err
	}
	if wrote {
		next := "Restart Claude Code to load it. Check it with: tether mcp doctor " + plan.server.Name
		if !plan.server.NeedsKey() {
			next = "Restart Claude Code, then sign in with /mcp. Check it with: tether mcp doctor " + plan.server.Name
		}
		c.UI.Println(fmt.Sprintf("MCP server %q is configured in user scope. %s", plan.server.Name, next))
	}
	return nil
}

func cmdMCPDiff(c *Ctx, args []string) error {
	fs := newFlagSet(c, "mcp diff", "mcp diff <name> [--force]")
	force := fs.Bool("force", false, "preview replacing an entry tether did not create")
	pos, err := parseArgs(c, fs, args, 1, 1)
	if err != nil {
		return err
	}
	if err := requireUserScope(c); err != nil {
		return err
	}
	plan, err := planMCPUse(pos[0], *force)
	if err != nil {
		return err
	}
	lines := mcpDiff(plan.current, plan.next, plan.path)
	if len(lines) == 0 {
		c.UI.Println(fmt.Sprintf("No changes: %s already has MCP server %q.", plan.path, pos[0]))
	}
	c.UI.PrintDiff(lines)
	return nil
}

func cmdMCPSetKey(c *Ctx, args []string) error {
	fs := newFlagSet(c, "mcp set-key", "mcp set-key <name> [--key TOKEN | --key-stdin | --key-env VAR]")
	key := fs.String("key", "", "API token (visible in shell history; prefer --key-stdin)")
	keyStdin := fs.Bool("key-stdin", false, "read the API token from stdin")
	keyEnv := fs.String("key-env", "", "read the API token from this environment variable")
	pos, err := parseArgs(c, fs, args, 1, 1)
	if err != nil {
		return err
	}
	if err := requireUserScope(c); err != nil {
		return err
	}
	n := 0
	for _, set := range []bool{*key != "", *keyStdin, *keyEnv != ""} {
		if set {
			n++
		}
	}
	if n > 1 {
		return usageErr("", "use only one of --key, --key-stdin, --key-env")
	}
	all, err := LoadMCPServers()
	if err != nil {
		return err
	}
	m, err := GetMCPServer(pos[0])
	if err != nil {
		return err
	}
	if !m.NeedsKey() {
		return usageErr("", "%q uses oauth; it has no token", m.Name)
	}
	newKey, err := readMCPKeyInput(c, *key, *keyStdin, *keyEnv)
	if err != nil {
		return err
	}
	if newKey == "" {
		return usageErr("pass --key-stdin, --key-env VAR, or run interactively", "no token given")
	}
	if m.KeyBackend != "" {
		DeleteKey(m.secretID(), m.KeyBackend)
	}
	backend, err := StoreKey(m.secretID(), newKey)
	if err != nil {
		return err
	}
	m.KeyBackend = backend
	all[m.Name] = m
	if err := SaveMCPServers(all); err != nil {
		return err
	}
	c.UI.Println(c.UI.C(fmt.Sprintf("Stored new token %s for %q in %s.", Mask(newKey), m.Name, backend), "green") +
		" Claude Code picks it up when it reconnects. Check it with: tether mcp doctor " + m.Name)
	return nil
}

func cmdMCPRemove(c *Ctx, args []string) error {
	fs := newFlagSet(c, "mcp remove", "mcp remove <name>")
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
	path := ClaudeJSONPath()
	current, err := LoadSettings(path)
	if err != nil {
		return err
	}
	servers, err := mcpServersOf(current, path)
	if err != nil {
		return err
	}
	if v, ok := servers.Get(m.Name); ok {
		if m.ownsEntry(v) {
			servers.Delete(m.Name)
			wrote, err := c.writeClaudeJSON(path, current, withServers(current, servers))
			if err != nil || !wrote {
				return err
			}
		} else {
			c.UI.Warn("%s has an MCP server named %q that tether did not create; leaving it", path, m.Name)
		}
	}
	ok, err := c.UI.Confirm(fmt.Sprintf("Remove MCP server %q from tether and its stored token?", m.Name), c.Yes)
	if err != nil || !ok {
		if err == nil {
			c.UI.Println("Aborted.")
		}
		return err
	}
	all, err := LoadMCPServers()
	if err != nil {
		return err
	}
	delete(all, m.Name)
	if err := SaveMCPServers(all); err != nil {
		return err
	}
	if m.NeedsKey() {
		DeleteKey(m.secretID(), m.KeyBackend)
	}
	c.UI.Println(fmt.Sprintf("Removed MCP server %q.", m.Name))
	return nil
}

// cmdMCPHeaders is what headersHelper runs: a JSON object of headers on
// stdout. Like `key`, it refuses to print to a terminal.
func cmdMCPHeaders(c *Ctx, args []string) error {
	fs := newFlagSet(c, "mcp-headers", "mcp-headers <name>")
	pos, err := parseArgs(c, fs, args, 1, 1)
	if err != nil {
		return err
	}
	if f, ok := c.UI.Out.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return usageErr("this command exists for Claude Code's headersHelper; use `tether mcp show` to see the masked token",
			"refusing to print a token to a terminal")
	}
	m, err := GetMCPServer(pos[0])
	if err != nil {
		return err
	}
	if !m.NeedsKey() {
		return usageErr("", "%q uses oauth; it has no headers", m.Name)
	}
	key, err := requireMCPKey(m)
	if err != nil {
		return err
	}
	out, _ := json.Marshal(m.Headers(key))
	_, err = fmt.Fprint(c.UI.Out, string(out))
	return err
}
