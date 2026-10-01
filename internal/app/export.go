package app

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"strings"

	"github.com/gurmukhnishansingh-quilr/quilr-tether/internal/ojson"
)

const keyNote = "use apiKeyHelper via MDM secret"

// secretEnvNames must never appear in a managed export.
var secretEnvNames = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_BEARER_TOKEN_BEDROCK"}

type ExportOpts struct {
	Enforce       bool
	LockProvider  bool
	APIKeyHelper  string
	KeepUserEmail bool
	NoKeyNote     bool
}

// BuildManaged returns the managed-settings document for a profile. It never
// contains a key: keys reach machines through the MDM secrets channel and an
// apiKeyHelper that the MDM also deploys.
func BuildManaged(p *Profile, o ExportOpts) (*ojson.Object, error) {
	if p.Type == "anthropic" {
		return nil, usageErr("", "anthropic profiles have nothing to roll out")
	}
	if o.LockProvider && p.Type != "quilr" {
		return nil, usageErr("", "--lock-provider pins sessions to a custom endpoint; %q is %s", p.Name, p.Type)
	}
	q := *p
	q.EnforceAvailable = p.EnforceAvailable || o.Enforce
	f := BuildFragment(&q, FragmentOpts{Scope: "managed", HelperCommand: o.APIKeyHelper, OmitEmail: !o.KeepUserEmail})
	if o.Enforce && len(p.AvailableModels) == 0 {
		return nil, usageErr("set an allowlist first: tether allow "+p.Name+" --models ...", "--enforce needs availableModels on the profile")
	}

	doc := ojson.NewObject()
	if f.Env.Len() > 0 {
		doc.Set("env", f.Env)
	}
	for _, k := range f.Top.Keys() {
		v, _ := f.Top.Get(k)
		doc.Set(k, v)
	}
	if o.LockProvider {
		// Requires Claude Code v2.1.285 or later.
		doc.Set("allowedProviders", []any{"customEndpoint"})
	}
	if p.NeedsKey() && o.APIKeyHelper == "" && !o.NoKeyNote {
		doc.Set("__key_delivery", keyNote)
	}
	return doc, nil
}

// AssertNoSecrets fails when a document carries a credential.
func AssertNoSecrets(doc *ojson.Object, secrets []string) error {
	if env, ok := doc.GetObject("env"); ok {
		for _, n := range secretEnvNames {
			if env.Has(n) {
				return usageErr("", "refusing to export: env.%s would be embedded", n)
			}
		}
	}
	raw, _ := ojson.Marshal(doc, "")
	for _, s := range secrets {
		if s != "" && bytes.Contains(raw, []byte(s)) {
			return usageErr("", "refusing to export: the document contains a stored key")
		}
	}
	if keyShaped.Match(raw) {
		return usageErr("", "refusing to export: the document contains something that looks like an API key")
	}
	return nil
}

// plist writes the document for the com.anthropic.claudecode managed
// preferences domain: same keys, nested objects as dicts, arrays as arrays.
func plist(doc *ojson.Object) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	writePlist(&b, doc, 0)
	b.WriteString("</plist>\n")
	return []byte(b.String())
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func writePlist(b *strings.Builder, v any, depth int) {
	pad := strings.Repeat("\t", depth)
	switch t := v.(type) {
	case *ojson.Object:
		b.WriteString(pad + "<dict>\n")
		for _, k := range t.Keys() {
			if strings.HasPrefix(k, "__") {
				continue // comment fields stay out of the plist
			}
			val, _ := t.Get(k)
			b.WriteString(pad + "\t<key>" + xmlEscape(k) + "</key>\n")
			writePlist(b, val, depth+1)
		}
		b.WriteString(pad + "</dict>\n")
	case []any:
		b.WriteString(pad + "<array>\n")
		for _, e := range t {
			writePlist(b, e, depth+1)
		}
		b.WriteString(pad + "</array>\n")
	case bool:
		if t {
			b.WriteString(pad + "<true/>\n")
		} else {
			b.WriteString(pad + "<false/>\n")
		}
	default:
		b.WriteString(pad + "<string>" + xmlEscape(fmt.Sprint(t)) + "</string>\n")
	}
}

func deploymentNotes(hasKey bool) string {
	notes := `Deploying managed-settings.json
  Paths Claude Code reads (verified against code.claude.com/docs/en/managed-settings):
    macOS    /Library/Application Support/ClaudeCode/managed-settings.json
    Windows  C:\Program Files\ClaudeCode\managed-settings.json   (C:\ProgramData is no longer read)
    Linux    /etc/claude-code/managed-settings.json
  The file must be readable by every user and writable only by admins. A file that
  is not valid JSON stops Claude Code from starting, so validate before rollout.

  Intune (Windows)
    - Win32 app or Platform script (runs as SYSTEM) that copies the file to
      C:\Program Files\ClaudeCode\managed-settings.json, or
    - deliver the same JSON as REG_SZ value "Settings" under
      HKLM\SOFTWARE\Policies\ClaudeCode (Settings catalog / custom OMA-URI / remediation).
  Intune (macOS)
    - Custom configuration profile for the com.anthropic.claudecode domain
      (use --plist to generate one), or a shell script that writes the file.
  Jamf Pro (macOS)
    - Configuration Profile > Application & Custom Settings > Upload, preference
      domain com.anthropic.claudecode, with the plist from --plist, or
    - a policy script that installs the file under /Library/Application Support/ClaudeCode/.
  Starter templates: https://github.com/anthropics/claude-code/tree/main/examples/mdm
  Verify on a device: run /status in Claude Code; "Setting sources" names the managed source.`
	if hasKey {
		notes += `

  Keys
    The export carries no key. Deliver each user's key through your MDM secrets
    channel (e.g. Intune/Jamf script that stores it in the login keychain or
    Credential Manager) and deploy an apiKeyHelper that prints it; pass that
    command with --api-key-helper so the managed file references it. With tether
    installed on devices: store the key with ` + "`tether profile add <name> --key-stdin`" + `
    and use --api-key-helper "<path to tether> key <name>".`
	}
	return notes
}

func cmdExportManaged(c *Ctx, args []string) error {
	fs := newFlagSet(c, "export-managed", "export-managed <name> -o FILE [options]")
	out := fs.String("o", "", "file to write (required)")
	fs.StringVar(out, "output", "", "file to write (required)")
	plistOut := fs.String("plist", "", "also write a macOS plist for the com.anthropic.claudecode domain")
	enforce := fs.Bool("enforce", false, "set enforceAvailableModels")
	lock := fs.Bool("lock-provider", false, `set allowedProviders: ["customEndpoint"] (Claude Code v2.1.285+)`)
	helper := fs.String("api-key-helper", "", "apiKeyHelper command your MDM deploys to every machine")
	keepEmail := fs.Bool("keep-user-email", false, "keep X-User-Email (normally per-user, so dropped)")
	noNote := fs.Bool("no-key-note", false, "omit the __key_delivery note field")
	pos, err := parseArgs(c, fs, args, 1, 1)
	if err != nil {
		return err
	}
	if *out == "" {
		return usageErr("", "-o FILE is required")
	}
	p, err := GetProfile(pos[0])
	if err != nil {
		return err
	}
	if p.Email != "" && !*keepEmail {
		c.UI.Warn("dropping X-User-Email (%s): it is per-user; pass --keep-user-email to keep it", p.Email)
	}
	doc, err := BuildManaged(p, ExportOpts{*enforce, *lock, *helper, *keepEmail, *noNote})
	if err != nil {
		return err
	}
	var secrets []string
	if p.NeedsKey() {
		if k, _ := ReadKey(p.Name, p.KeyBackend); k != "" {
			secrets = append(secrets, k)
		}
	}
	if err := AssertNoSecrets(doc, secrets); err != nil {
		return err
	}
	if _, err := os.Stat(*out); err == nil {
		ok, err := c.UI.Confirm(*out+" exists. Overwrite?", c.Yes)
		if err != nil || !ok {
			return err
		}
	}
	body, _ := ojson.Marshal(doc, "  ")
	if err := AtomicWrite(*out, append(body, '\n'), false); err != nil {
		return err
	}
	c.UI.Println(c.UI.C("Wrote "+*out, "green"))
	if *plistOut != "" {
		if err := AtomicWrite(*plistOut, plist(doc), false); err != nil {
			return err
		}
		c.UI.Println(c.UI.C("Wrote "+*plistOut, "green") + " (com.anthropic.claudecode)")
	}
	c.UI.Println("")
	c.UI.Println(deploymentNotes(p.NeedsKey()))
	return nil
}
