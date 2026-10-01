package app

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gurmukhnishansingh-quilr/quilr-tether/internal/ojson"
	"golang.org/x/term"
)

func (c *Ctx) target() (string, error) { return SettingsPath(c.Scope, c.ProjectDir) }

// DetectActive reports which profile produced the owned keys in data.
func DetectActive(data *ojson.Object, scope string, known map[string]*Profile) string {
	view := OwnedView(data)
	for _, name := range sortedProfileNames(known) {
		p := known[name]
		opts := FragmentOpts{Scope: scope}
		if p.NeedsKey() {
			if k, ok := view.Env.GetString("ANTHROPIC_API_KEY"); ok {
				opts.PlaintextKey = k
			} else if h, ok := view.Top.GetString("apiKeyHelper"); ok {
				if !strings.HasSuffix(h, " key "+name) {
					continue
				}
				opts.HelperCommand = h
			}
		}
		f := BuildFragment(p, opts)
		if ojson.Equal(f.Top, view.Top) && ojson.Equal(f.Env, view.Env) {
			return name
		}
	}
	return ""
}

func checkPlaintextScope(scope string, plaintext bool) error {
	if !plaintext {
		return nil
	}
	switch scope {
	case "project":
		return usageErr("drop --plaintext (apiKeyHelper) or use --scope local",
			"refusing to write a plaintext key to project scope: .claude/settings.json is committed to git")
	case "managed":
		return usageErr("deliver the key with apiKeyHelper; see `tether export-managed`",
			"refusing to write a plaintext key to managed settings: the file is readable by every user")
	}
	return nil
}

func gitIgnored(path string) (ignored, known bool) {
	cmd := exec.Command("git", "check-ignore", "-q", path)
	cmd.Dir = filepath.Dir(filepath.Dir(path))
	err := cmd.Run()
	if err == nil {
		return true, true
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return false, true
	}
	return false, false
}

func (c *Ctx) writeWithPreview(path string, current, next *ojson.Object) (bool, error) {
	lines := SettingsDiff(current, next, path)
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
	backup, err := WriteSettings(path, next, c.Scope)
	if err != nil {
		return false, err
	}
	c.UI.Println(c.UI.C("Wrote "+path, "green") + describeBackup(backup))
	return true, nil
}

// --- profile -------------------------------------------------------------------

func cmdProfile(c *Ctx, args []string) error {
	if len(args) == 0 {
		return usageErr("", "usage: tether profile add|list|show|remove ...")
	}
	switch args[0] {
	case "add":
		return cmdProfileAdd(c, args[1:])
	case "list":
		return cmdProfileList(c, args[1:])
	case "show":
		return cmdProfileShow(c, args[1:])
	case "remove", "rm":
		return cmdProfileRemove(c, args[1:])
	}
	return usageErr("", "unknown profile action %q; expected add, list, show or remove", args[0])
}

func cmdProfileAdd(c *Ctx, args []string) error {
	fs := newFlagSet(c, "profile add", "profile add <name> --type quilr|anthropic|bedrock [options]")
	ptype := fs.String("type", "", "quilr, anthropic or bedrock")
	force := fs.Bool("force", false, "replace an existing profile")
	region := fs.String("region", "", "Quilr region (auto, usa-1, usa-2, india-1, jp-1); for bedrock, the AWS region")
	baseURL := fs.String("base-url", "", "explicit gateway URL instead of a region")
	email := fs.String("email", "", "sent as X-User-Email")
	label := fs.String("label", "", "sent as X-Provider-Label")
	disc := fs.Bool("discovery", false, "enable /v1/models discovery (default)")
	noDisc := fs.Bool("no-discovery", false, "disable /v1/models discovery")
	discTimeout := fs.Int("discovery-timeout-ms", 0, "CLAUDE_CODE_GATEWAY_MODEL_DISCOVERY_TIMEOUT_MS")
	bedrockBacked := fs.Bool("bedrock-backed", false, "gateway forwards to Bedrock: disable experimental betas")
	key := fs.String("key", "", "API key (visible in shell history; prefer --key-stdin)")
	keyStdin := fs.Bool("key-stdin", false, "read the API key from stdin")
	keyEnv := fs.String("key-env", "", "read the API key from this environment variable")
	awsRegion := fs.String("aws-region", "", "AWS region (bedrock)")
	awsProfile := fs.String("aws-profile", "", "AWS profile (bedrock)")
	sso := fs.Bool("sso-refresh", false, `set awsAuthRefresh to "aws sso login --profile <p>"`)
	model := fs.String("model", "", "default model (settings `model`)")
	pins := map[string]*string{}
	for _, t := range Tiers {
		pins[t] = fs.String(t, "", "pin ANTHROPIC_DEFAULT_"+strings.ToUpper(t)+"_MODEL")
	}
	pos, err := parseArgs(c, fs, args, 1, 1)
	if err != nil {
		return err
	}
	name := pos[0]
	if err := validateName(name); err != nil {
		return err
	}
	all, err := LoadProfiles()
	if err != nil {
		return err
	}
	existing, exists := all[name]
	if exists && !*force {
		return usageErr("pass --force to replace it", "profile %q already exists", name)
	}
	if *disc && *noDisc {
		return usageErr("", "--discovery and --no-discovery are mutually exclusive")
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

	t := *ptype
	if t == "" {
		if t, err = c.UI.Ask("Provider type", "--type", "", ProfileTypes, true); err != nil {
			return err
		}
	}
	p := &Profile{Name: name, Type: t, Pins: map[string]string{}}
	for _, tier := range Tiers {
		if *pins[tier] != "" {
			id, err := validateModelID(*pins[tier])
			if err != nil {
				return err
			}
			p.Pins[tier] = id
		}
	}
	if *model != "" {
		if p.Model, err = validateModelID(*model); err != nil {
			return err
		}
	}

	var newKey string
	switch t {
	case "quilr":
		if *baseURL != "" {
			p.BaseURL = *baseURL
		} else if p.Region = *region; p.Region == "" {
			if p.Region, err = c.UI.Ask("Quilr region", "--region", "auto", QuilrRegionNames, true); err != nil {
				return err
			}
		}
		if p.Email = *email; p.Email == "" {
			if p.Email, err = c.UI.Ask("User email for X-User-Email", "--email", "", nil, false); err != nil {
				return err
			}
		}
		if p.ProviderLabel = *label; p.ProviderLabel == "" {
			if p.ProviderLabel, err = c.UI.Ask("Provider label for X-Provider-Label", "--label", "", nil, false); err != nil {
				return err
			}
		}
		on := true
		switch {
		case *noDisc:
			on = false
		case !*disc:
			on = c.UI.AskBool("Enable gateway model discovery?", true)
		}
		if !on {
			p.Discovery = &on
		}
		p.DiscoveryTimeoutMS = *discTimeout
		p.BedrockBacked = *bedrockBacked
		if newKey, err = readKeyInput(c, *key, *keyStdin, *keyEnv); err != nil {
			return err
		}
		if newKey == "" {
			if exists && existing.NeedsKey() {
				p.KeyBackend = existing.KeyBackend // --force without a new key keeps the stored one
				if k, _ := ReadKey(name, existing.KeyBackend); k == "" {
					return usageErr("pass --key-stdin, --key-env VAR, or run interactively", "a Quilr API key is required")
				}
			} else {
				return usageErr("pass --key-stdin, --key-env VAR, or run interactively", "a Quilr API key is required")
			}
		}
	case "bedrock":
		p.AWSRegion = *awsRegion
		if p.AWSRegion == "" {
			p.AWSRegion = *region
		}
		if p.AWSRegion == "" {
			if p.AWSRegion, err = c.UI.Ask("AWS region", "--aws-region", "", nil, true); err != nil {
				return err
			}
		}
		if p.AWSProfile = *awsProfile; p.AWSProfile == "" {
			if p.AWSProfile, err = c.UI.Ask("AWS profile", "--aws-profile", "", nil, true); err != nil {
				return err
			}
		}
		p.SSORefresh = *sso
		if len(p.Pins) == 0 && c.UI.Interactive {
			for _, tier := range []string{"opus", "sonnet", "haiku"} {
				v, err := c.UI.Ask("Bedrock model id for "+tier, "--"+tier, "", nil, false)
				if err != nil {
					return err
				}
				if v != "" {
					if p.Pins[tier], err = validateModelID(v); err != nil {
						return err
					}
				}
			}
		}
	default:
		if keySources > 0 {
			return usageErr("", "anthropic profiles take no key; Claude Code uses your claude.ai login or Console key")
		}
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if newKey != "" {
		backend, err := StoreKey(name, newKey)
		if err != nil {
			return err
		}
		p.KeyBackend = backend
		if backend == "file" {
			c.UI.Warn("no OS keychain available; the key is stored owner-only under %s",
				filepath.Join(ConfigDir(), "secrets"))
		}
		c.UI.Println(fmt.Sprintf("Stored key %s in %s.", Mask(newKey), backend))
	}
	if exists && existing.NeedsKey() && !p.NeedsKey() {
		DeleteKey(name, existing.KeyBackend)
	}
	if len(p.Pins) == 0 {
		p.Pins = nil
	}
	all[name] = p
	if err := SaveProfiles(all); err != nil {
		return err
	}
	c.UI.Println(c.UI.C(fmt.Sprintf("Saved profile %q (%s).", name, t), "green") + " Apply it with: tether use " + name)
	return nil
}

func readKeyInput(c *Ctx, key string, fromStdin bool, envVar string) (string, error) {
	switch {
	case fromStdin:
		line, err := c.UI.readLine()
		if err != nil {
			return "", usageErr("", "no key on stdin")
		}
		return strings.TrimSpace(line), nil
	case envVar != "":
		v := os.Getenv(envVar)
		if v == "" {
			return "", usageErr("", "environment variable %s is empty or unset", envVar)
		}
		return v, nil
	case key != "":
		c.UI.Warn("--key leaves the key in your shell history; prefer --key-stdin or the prompt")
		return key, nil
	case c.UI.Interactive:
		return c.UI.AskSecret("Quilr API key (input hidden): ")
	}
	return "", nil
}

func profileSummary(p *Profile) *ojson.Object {
	o := ojson.NewObject()
	o.Set("name", p.Name)
	o.Set("type", p.Type)
	o.Set("target", p.Target())
	if p.NeedsKey() {
		k, _ := ReadKey(p.Name, p.KeyBackend)
		if k == "" {
			o.Set("key", "missing")
		} else {
			o.Set("key", Mask(k)+" ("+p.KeyBackend+")")
		}
		if p.Email != "" {
			o.Set("email", p.Email)
		}
		if p.ProviderLabel != "" {
			o.Set("provider_label", p.ProviderLabel)
		}
		o.Set("discovery", p.DiscoveryOn())
		if p.DiscoveryTimeoutMS > 0 {
			o.Set("discovery_timeout_ms", p.DiscoveryTimeoutMS)
		}
		if p.BedrockBacked {
			o.Set("bedrock_backed", true)
		}
	}
	if p.Type == "bedrock" && p.SSORefresh {
		o.Set("sso_refresh", true)
	}
	if p.Model != "" {
		o.Set("model", p.Model)
	}
	if len(p.Pins) > 0 {
		pins := ojson.NewObject()
		for _, t := range Tiers {
			if id, ok := p.Pins[t]; ok {
				pins.Set(t, id)
			}
		}
		o.Set("pins", pins)
	}
	if len(p.AvailableModels) > 0 {
		o.Set("available_models", p.AvailableModels)
		o.Set("enforce_available", p.EnforceAvailable)
	}
	if len(p.ModelOverrides) > 0 {
		o.Set("model_overrides", NewOrderedStrings(p.ModelOverrides))
	}
	return o
}

func cmdProfileList(c *Ctx, args []string) error {
	fs := newFlagSet(c, "profile list", "profile list [--json]")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if _, err := parseArgs(c, fs, args, 0, 0); err != nil {
		return err
	}
	all, err := LoadProfiles()
	if err != nil {
		return err
	}
	if *asJSON {
		arr := []any{}
		for _, n := range sortedProfileNames(all) {
			arr = append(arr, profileSummary(all[n]))
		}
		c.UI.JSON(ojson.Normalize(arr))
		return nil
	}
	if len(all) == 0 {
		c.UI.Println("No profiles yet. Create one with `tether profile add <name> --type quilr`.")
		return nil
	}
	userPath, _ := SettingsPath("user", "")
	userData, err := LoadSettings(userPath)
	active := ""
	if err == nil {
		active = DetectActive(userData, "user", all)
	}
	for _, n := range sortedProfileNames(all) {
		mark := " "
		if n == active {
			mark = "*"
		}
		c.UI.Printf("%s %-20s %-9s %s\n", mark, n, all[n].Type, all[n].Target())
	}
	if active != "" {
		c.UI.Println(c.UI.C("* active in user scope", "dim"))
	}
	return nil
}

func cmdProfileShow(c *Ctx, args []string) error {
	fs := newFlagSet(c, "profile show", "profile show <name> [--json]")
	asJSON := fs.Bool("json", false, "machine-readable output")
	pos, err := parseArgs(c, fs, args, 1, 1)
	if err != nil {
		return err
	}
	p, err := GetProfile(pos[0])
	if err != nil {
		return err
	}
	s := profileSummary(p)
	if *asJSON {
		c.UI.JSON(s)
		return nil
	}
	for _, k := range s.Keys() {
		v, _ := s.Get(k)
		if str, ok := v.(string); ok {
			c.UI.Printf("%-22s %s\n", k, str)
		} else {
			c.UI.Printf("%-22s %s\n", k, jsonString(v))
		}
	}
	return nil
}

func cmdProfileRemove(c *Ctx, args []string) error {
	fs := newFlagSet(c, "profile remove", "profile remove <name>")
	pos, err := parseArgs(c, fs, args, 1, 1)
	if err != nil {
		return err
	}
	p, err := GetProfile(pos[0])
	if err != nil {
		return err
	}
	for _, st := range LoadScopes(c.ProjectDir) {
		if st.Data != nil && DetectActive(st.Data, st.Scope, map[string]*Profile{p.Name: p}) == p.Name {
			c.UI.Warn("%q is still applied in %s scope (%s); switch away first", p.Name, st.Scope, st.Path)
		}
	}
	ok, err := c.UI.Confirm(fmt.Sprintf("Remove profile %q and its stored key?", p.Name), c.Yes)
	if err != nil || !ok {
		if err == nil {
			c.UI.Println("Aborted.")
		}
		return err
	}
	all, err := LoadProfiles()
	if err != nil {
		return err
	}
	delete(all, p.Name)
	if err := SaveProfiles(all); err != nil {
		return err
	}
	if p.NeedsKey() {
		DeleteKey(p.Name, p.KeyBackend)
	}
	c.UI.Println(fmt.Sprintf("Removed profile %q.", p.Name))
	return nil
}

// --- use / diff / restore / key ---------------------------------------------------

type usePlan struct {
	profile       *Profile
	path          string
	current, next *ojson.Object
}

func planUse(c *Ctx, name string, plaintext bool) (*usePlan, error) {
	p, err := GetProfile(name)
	if err != nil {
		return nil, err
	}
	if err := checkPlaintextScope(c.Scope, plaintext); err != nil {
		return nil, err
	}
	if plaintext && !p.NeedsKey() {
		return nil, usageErr("", "--plaintext only applies to Quilr profiles; %q is %s", p.Name, p.Type)
	}
	path, err := c.target()
	if err != nil {
		return nil, err
	}
	current, err := LoadSettings(path)
	if err != nil {
		return nil, err
	}
	opts := FragmentOpts{Scope: c.Scope}
	if p.NeedsKey() {
		key, err := RequireKey(p.Name, p.KeyBackend)
		if err != nil {
			return nil, err
		}
		if plaintext {
			opts.PlaintextKey = key
		} else {
			opts.HelperCommand = HelperCommand(p.Name)
		}
	}
	next, err := ApplyFragment(current, BuildFragment(p, opts))
	if err != nil {
		return nil, err
	}
	return &usePlan{p, path, current, next}, nil
}

func cmdUse(c *Ctx, args []string) error {
	fs := newFlagSet(c, "use", "use <name> [--scope ...] [--plaintext]")
	plaintext := fs.Bool("plaintext", false, "write env.ANTHROPIC_API_KEY instead of apiKeyHelper")
	pos, err := parseArgs(c, fs, args, 1, 1)
	if err != nil {
		return err
	}
	plan, err := planUse(c, pos[0], *plaintext)
	if err != nil {
		return err
	}
	p := plan.profile
	if c.Scope == "project" && p.NeedsKey() {
		c.UI.Warn("apiKeyHelper points at the tether binary on this machine and project settings are committed; " +
			"teammates need tether at the same path. Prefer --scope local or user.")
	}
	if c.Scope == "local" && *plaintext {
		if ignored, known := gitIgnored(plan.path); known && !ignored {
			c.UI.Warn("%s is not git-ignored; add it to .gitignore before committing", plan.path)
		}
	}
	if p.EnforceAvailable && c.Scope != "managed" {
		c.UI.Warn("enforceAvailableModels is read from managed settings only; not writing it here (use `tether export-managed --enforce`)")
	}
	for _, issue := range Outranking(LoadScopes(c.ProjectDir), c.Scope) {
		c.UI.Warn("%s", issue.Message)
	}
	wrote, err := c.writeWithPreview(plan.path, plan.current, plan.next)
	if err != nil {
		return err
	}
	if wrote {
		c.UI.Println(fmt.Sprintf("Profile %q is active in %s scope. Next: tether doctor %s", p.Name, c.Scope, p.Name))
	}
	return nil
}

func cmdDiff(c *Ctx, args []string) error {
	fs := newFlagSet(c, "diff", "diff <name> [--scope ...] [--plaintext]")
	plaintext := fs.Bool("plaintext", false, "preview the --plaintext variant")
	pos, err := parseArgs(c, fs, args, 1, 1)
	if err != nil {
		return err
	}
	plan, err := planUse(c, pos[0], *plaintext)
	if err != nil {
		return err
	}
	lines := SettingsDiff(plan.current, plan.next, plan.path)
	if len(lines) == 0 {
		c.UI.Println(fmt.Sprintf("No changes: %s already matches profile %q.", plan.path, pos[0]))
	}
	c.UI.PrintDiff(lines)
	return nil
}

func cmdRestore(c *Ctx, args []string) error {
	fs := newFlagSet(c, "restore", "restore [timestamp] [--scope ...] [--list]")
	list := fs.Bool("list", false, "list backups")
	pos, err := parseArgs(c, fs, args, 0, 1)
	if err != nil {
		return err
	}
	if *list {
		backups := ListBackups()
		if len(backups) == 0 {
			c.UI.Println("No backups yet.")
		}
		for _, b := range backups {
			absent := ""
			if !b.Existed {
				absent = "  (file absent)"
			}
			c.UI.Printf("%s  %s  %-8s %s%s\n", b.Timestamp, b.When(), b.Scope, b.Source, absent)
		}
		return nil
	}
	ts := ""
	if len(pos) == 1 {
		ts = pos[0]
	}
	target, err := c.target()
	if err != nil {
		return err
	}
	b, err := FindBackup(ts, target)
	if err != nil {
		return err
	}
	content, err := ReadBackup(b)
	if err != nil {
		return err
	}
	path := b.Source
	if content == nil {
		c.UI.Println(fmt.Sprintf("Backup %s records that %s did not exist; restoring removes it.", b.Timestamp, path))
		ok, err := c.UI.Confirm("Remove "+path+"?", c.Yes)
		if err != nil || !ok {
			return err
		}
		if _, err := CreateBackup(path, b.Scope); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return wrapIO("remove", path, err)
		}
		c.UI.Println(c.UI.C("Removed "+path+".", "green"))
		return nil
	}
	current, err := LoadSettings(path)
	if err != nil {
		current = ojson.NewObject()
	}
	if restored, perr := ojson.ParseObject(content); perr == nil {
		c.UI.PrintDiff(SettingsDiff(current, restored, path))
	} else {
		c.UI.Warn("the backup is not valid JSON; restoring it verbatim")
	}
	ok, err := c.UI.Confirm(fmt.Sprintf("Restore %s from %s?", path, b.When()), c.Yes)
	if err != nil || !ok {
		if err == nil {
			c.UI.Println("Aborted.")
		}
		return err
	}
	if err := EnsureWritable(path); err != nil {
		return err
	}
	if _, err := CreateBackup(path, b.Scope); err != nil { // so the restore can be undone
		return err
	}
	if err := AtomicWrite(path, content, false); err != nil {
		return err
	}
	c.UI.Println(c.UI.C(fmt.Sprintf("Restored %s from backup %s.", path, b.Timestamp), "green"))
	return nil
}

// cmdKey is what apiKeyHelper runs. It writes the raw key to stdout, so it
// refuses to print to a terminal: only Claude Code (a pipe) should see it.
func cmdKey(c *Ctx, args []string) error {
	fs := newFlagSet(c, "key", "key <name>")
	pos, err := parseArgs(c, fs, args, 1, 1)
	if err != nil {
		return err
	}
	if f, ok := c.UI.Out.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return usageErr("this command exists for Claude Code's apiKeyHelper; use `tether profile show` to see the masked key",
			"refusing to print a key to a terminal")
	}
	p, err := GetProfile(pos[0])
	if err != nil {
		return err
	}
	key, err := RequireKey(p.Name, p.KeyBackend)
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(c.UI.Out, key)
	return err
}

// --- status ----------------------------------------------------------------------

func cmdStatus(c *Ctx, args []string) error {
	fs := newFlagSet(c, "status", "status [--scope ...] [--json]")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if _, err := parseArgs(c, fs, args, 0, 0); err != nil {
		return err
	}
	states := LoadScopes(c.ProjectDir)
	var target ScopeState
	for _, st := range states {
		if st.Scope == c.Scope {
			target = st
		}
	}
	known, err := LoadProfiles()
	if err != nil {
		return err
	}
	data := target.Data
	if data == nil {
		data = ojson.NewObject()
	}
	active := ""
	if target.Error == "" {
		active = DetectActive(data, c.Scope, known)
	}
	view := OwnedView(data)
	eff := ComputeEffective(states)
	issues := append(append(FindConflicts(eff), ShellShadowing(eff)...), Outranking(states, c.Scope)...)
	for _, st := range states {
		if st.Error != "" {
			issues = append([]Issue{{"fail", st.Error, "fix the JSON or run `tether restore`"}}, issues...)
		}
	}
	_, statErr := os.Stat(target.Path)
	exists := statErr == nil

	if *asJSON {
		owned := view.Top.Clone()
		maskedEnv := ojson.NewObject()
		for _, k := range view.Env.Keys() {
			v, _ := view.Env.GetString(k)
			maskedEnv.Set(k, maskEnvValue(k, v))
		}
		owned.Set("env", maskedEnv)
		effObj := ojson.NewObject()
		for _, k := range eff.TopKeys {
			s := ojson.NewObject()
			s.Set("value", eff.Top[k].Value)
			s.Set("from", eff.Top[k].From)
			effObj.Set(k, s)
		}
		effEnv := ojson.NewObject()
		for _, k := range eff.EnvKeys {
			s := ojson.NewObject()
			s.Set("value", maskEnvValue(k, fmt.Sprint(eff.Env[k].Value)))
			s.Set("from", eff.Env[k].From)
			effEnv.Set(k, s)
		}
		effObj.Set("env", effEnv)
		shell := ojson.NewObject()
		so := ShellOverrides()
		for _, k := range sortedKeys(so) {
			shell.Set(k, maskEnvValue(k, so[k]))
		}
		issueArr := []any{}
		for _, i := range issues {
			o := ojson.NewObject()
			o.Set("level", i.Level)
			o.Set("message", i.Message)
			o.Set("fix", i.Fix)
			issueArr = append(issueArr, o)
		}
		r := ojson.NewObject()
		r.Set("scope", c.Scope)
		r.Set("path", target.Path)
		r.Set("exists", exists)
		if active != "" {
			r.Set("active_profile", active)
		} else {
			r.Set("active_profile", nil)
		}
		r.Set("owned", owned)
		r.Set("effective", effObj)
		r.Set("shell", shell)
		r.Set("issues", issueArr)
		c.UI.JSON(r)
		return nil
	}

	missing := ""
	if !exists {
		missing = " (missing)"
	}
	c.UI.Println(c.UI.C(c.Scope+" settings: ", "bold") + target.Path + missing)
	switch {
	case active != "":
		c.UI.Println(fmt.Sprintf("Active profile:  %s (%s)", c.UI.C(active, "green"), known[active].Type))
	case !view.IsEmpty():
		c.UI.Println("Active profile:  " + c.UI.C("none matches", "yellow") + " (owned keys edited by hand or profile changed)")
	default:
		c.UI.Println("Active profile:  none (Claude Code uses the claude.ai login or Console key)")
	}
	c.UI.Println("")
	c.UI.Println(c.UI.C("Keys tether owns in this file:", "bold"))
	if view.IsEmpty() {
		c.UI.Println("  (none)")
	}
	for _, k := range view.Top.Keys() {
		v, _ := view.Top.Get(k)
		c.UI.Println(fmt.Sprintf("  %s = %s", k, jsonString(v)))
	}
	for _, k := range view.Env.Keys() {
		v, _ := view.Env.GetString(k)
		c.UI.Println(fmt.Sprintf("  env.%s = %s", k, jsonString(maskEnvValue(k, v))))
	}
	c.UI.Println("")
	c.UI.Println(c.UI.C("Effective (managed > local > project > user):", "bold"))
	if len(eff.TopKeys)+len(eff.EnvKeys) == 0 {
		c.UI.Println("  (none)")
	}
	for _, k := range eff.TopKeys {
		c.UI.Println(fmt.Sprintf("  %s = %s  [%s]", k, jsonString(eff.Top[k].Value), eff.Top[k].From))
	}
	for _, k := range eff.EnvKeys {
		v := fmt.Sprint(eff.Env[k].Value)
		c.UI.Println(fmt.Sprintf("  env.%s = %s  [%s]", k, jsonString(maskEnvValue(k, v)), eff.Env[k].From))
	}
	c.UI.Println("")
	if len(issues) == 0 {
		c.UI.Println(c.UI.C("No conflicts.", "green"))
		return nil
	}
	c.UI.Println(c.UI.C("Conflicts and warnings:", "bold"))
	for _, i := range issues {
		tag := c.UI.C("WARN", "yellow")
		if i.Level == "fail" {
			tag = c.UI.C("FAIL", "red")
		}
		c.UI.Println(fmt.Sprintf("  %s %s\n       fix: %s", tag, i.Message, i.Fix))
	}
	return nil
}

// --- models ----------------------------------------------------------------------

// activeOrNamed resolves an optional profile argument.
func activeOrNamed(c *Ctx, pos []string) (*Profile, error) {
	if len(pos) == 1 {
		return GetProfile(pos[0])
	}
	known, err := LoadProfiles()
	if err != nil {
		return nil, err
	}
	states := LoadScopes(c.ProjectDir)
	for i := len(states) - 1; i >= 0; i-- { // highest precedence first
		if states[i].Data != nil {
			if n := DetectActive(states[i].Data, states[i].Scope, known); n != "" {
				return known[n], nil
			}
		}
	}
	return nil, usageErr("pass a profile name", "no profile given and none is active")
}

type modelCache struct {
	Base      string       `json:"base"`
	FetchedAt int64        `json:"fetched_at"`
	Data      []ModelEntry `json:"data"`
}

func cachePath(name string) string {
	return filepath.Join(ConfigDir(), "cache", "models-"+name+".json")
}

func fetchModels(p *Profile, refresh bool) ([]ModelEntry, int64, bool, error) {
	path := cachePath(p.Name)
	if !refresh {
		if raw, err := os.ReadFile(path); err == nil {
			var mc modelCache
			if json.Unmarshal(raw, &mc) == nil && mc.Base == p.GatewayRoot() {
				return mc.Data, mc.FetchedAt, true, nil
			}
		}
	}
	key, err := RequireKey(p.Name, p.KeyBackend)
	if err != nil {
		return nil, 0, false, err
	}
	d, err := Discover(p.GatewayRoot(), key, p.HeaderMap(), 15*time.Second)
	if err != nil {
		return nil, 0, false, ioErr("check network / proxy settings", "cannot reach %s: %v", p.GatewayRoot(), err)
	}
	if d.Error != "" {
		return nil, 0, false, ioErr("run `tether doctor "+p.Name+"`", "GET /v1/models failed (%d): %s", d.Status, d.Error)
	}
	now := time.Now().Unix()
	b, _ := json.MarshalIndent(modelCache{p.GatewayRoot(), now, d.Entries}, "", "  ")
	if err := AtomicWrite(path, append(b, '\n'), false); err != nil {
		return nil, 0, false, err
	}
	return d.Entries, now, false, nil
}

func cmdModels(c *Ctx, args []string) error {
	fs := newFlagSet(c, "models", "models [<name>] [--refresh] [--json]")
	refresh := fs.Bool("refresh", false, "ignore the cache and refetch")
	asJSON := fs.Bool("json", false, "machine-readable output")
	pos, err := parseArgs(c, fs, args, 0, 1)
	if err != nil {
		return err
	}
	p, err := activeOrNamed(c, pos)
	if err != nil {
		return err
	}
	if p.Type != "quilr" {
		return usageErr("", "model listing needs a gateway profile; %q is %s", p.Name, p.Type)
	}
	entries, fetchedAt, cached, err := fetchModels(p, *refresh)
	if err != nil {
		return err
	}
	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	kept, dropped := ClaudeFilter(ids)
	pinnedAs := map[string]string{}
	for t, id := range p.Pins {
		pinnedAs[id] = t
	}
	overrideFor := map[string]string{}
	for a, g := range p.ModelOverrides {
		overrideFor[g] = a
	}
	rows := []any{}
	for _, e := range entries {
		allowed := len(p.AvailableModels) == 0 || contains(p.AvailableModels, e.ID)
		r := ojson.NewObject()
		r.Set("id", e.ID)
		r.Set("display_name", e.DisplayName)
		r.Set("in_picker", contains(kept, e.ID) && allowed)
		r.Set("dropped_by_filter", contains(dropped, e.ID))
		if len(p.AvailableModels) > 0 {
			r.Set("allowed", contains(p.AvailableModels, e.ID))
		} else {
			r.Set("allowed", nil)
		}
		r.Set("pinned_as", nilIfEmpty(pinnedAs[e.ID]))
		r.Set("override_for", nilIfEmpty(overrideFor[e.ID]))
		rows = append(rows, r)
	}
	var missing []string
	for _, id := range append(mapValues(p.Pins), mapValues(p.ModelOverrides)...) {
		if !contains(ids, id) {
			missing = append(missing, id)
		}
	}
	if *asJSON {
		o := ojson.NewObject()
		o.Set("profile", p.Name)
		o.Set("base_url", p.GatewayRoot())
		o.Set("cached", cached)
		o.Set("fetched_at", fetchedAt)
		o.Set("models", rows)
		c.UI.JSON(o)
		return nil
	}
	age := "live"
	if cached {
		age = fmt.Sprintf("cached %ds ago", time.Now().Unix()-fetchedAt)
	}
	c.UI.Println(fmt.Sprintf("%d models from %s (%s; --refresh to refetch)", len(ids), p.GatewayRoot(), age))
	for _, raw := range rows {
		r := raw.(*ojson.Object)
		id, _ := r.GetString("id")
		var tags []string
		if t := pinnedAs[id]; t != "" {
			tags = append(tags, c.UI.C("pinned:"+t, "green"))
		}
		if v, _ := r.Get("allowed"); v == true {
			tags = append(tags, c.UI.C("allowed", "cyan"))
		}
		if a := overrideFor[id]; a != "" {
			tags = append(tags, "override:"+a)
		}
		inPicker, _ := r.Get("in_picker")
		if contains(dropped, id) {
			tags = append(tags, c.UI.C("dropped (no 'claude'/'anthropic' in id)", "yellow"))
		} else if inPicker != true {
			tags = append(tags, c.UI.C("hidden by availableModels", "dim"))
		}
		mark := "-"
		if inPicker == true {
			mark = "+"
		}
		c.UI.Printf(" %s %-44s %s\n", mark, id, strings.Join(tags, " "))
	}
	for _, m := range missing {
		c.UI.Warn("%s is pinned/overridden but the gateway does not list it for this key", m)
	}
	return nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, k := range sortedKeys(m) {
		out = append(out, m[k])
	}
	return out
}

// --- pin / allow / override ---------------------------------------------------------

func editable(name string) (*Profile, error) {
	p, err := GetProfile(name)
	if err != nil {
		return nil, err
	}
	if p.Type == "anthropic" {
		return nil, usageErr("", "%q is an anthropic profile; it writes no model settings", name)
	}
	return p, nil
}

func saved(c *Ctx, p *Profile) error {
	if len(p.Pins) == 0 {
		p.Pins = nil
	}
	if len(p.ModelOverrides) == 0 {
		p.ModelOverrides = nil
	}
	if err := PutProfile(p); err != nil {
		return err
	}
	c.UI.Println(c.UI.C(fmt.Sprintf("Updated profile %q.", p.Name), "green") + " Apply with: tether use " + p.Name)
	return nil
}

func cmdPin(c *Ctx, args []string) error {
	fs := newFlagSet(c, "pin", "pin <name> [--opus ID] [--sonnet ID] [--haiku ID] [--fable ID] [--clear]")
	vals := map[string]*string{}
	set := map[string]bool{}
	for _, t := range Tiers {
		vals[t] = fs.String(t, "", "model id ('' unpins)")
	}
	clear := fs.Bool("clear", false, "remove all pins first")
	pos, err := parseArgs(c, fs, args, 1, 1)
	if err != nil {
		return err
	}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	p, err := editable(pos[0])
	if err != nil {
		return err
	}
	changed := false
	if *clear {
		p.Pins, changed = nil, true
	}
	if p.Pins == nil {
		p.Pins = map[string]string{}
	}
	for _, t := range Tiers {
		if !set[t] {
			continue
		}
		changed = true
		if *vals[t] == "" {
			delete(p.Pins, t)
			continue
		}
		if p.Pins[t], err = validateModelID(*vals[t]); err != nil {
			return err
		}
	}
	if !changed {
		return usageErr("pass --opus/--sonnet/--haiku/--fable ID or --clear", "nothing to pin")
	}
	return saved(c, p)
}

func cmdAllow(c *Ctx, args []string) error {
	fs := newFlagSet(c, "allow", "allow <name> --models a,b,c [--enforce] | --clear")
	models := fs.String("models", "", "comma-separated model ids or aliases")
	enforce := fs.Bool("enforce", false, "also set enforceAvailableModels (managed only)")
	clear := fs.Bool("clear", false, "remove the allowlist")
	pos, err := parseArgs(c, fs, args, 1, 1)
	if err != nil {
		return err
	}
	p, err := editable(pos[0])
	if err != nil {
		return err
	}
	switch {
	case *clear:
		p.AvailableModels, p.EnforceAvailable = nil, false
	case *models == "":
		return usageErr("", "pass --models a,b,c or --clear")
	default:
		p.AvailableModels = nil
		for _, m := range strings.Split(*models, ",") {
			if m = strings.TrimSpace(m); m != "" {
				id, err := validateModelID(m)
				if err != nil {
					return err
				}
				p.AvailableModels = append(p.AvailableModels, id)
			}
		}
		p.EnforceAvailable = *enforce
		if *enforce {
			c.UI.Println("Note: enforceAvailableModels only takes effect from managed settings " +
				"(`tether use --scope managed` or `tether export-managed`).")
		}
	}
	return saved(c, p)
}

func cmdOverride(c *Ctx, args []string) error {
	fs := newFlagSet(c, "override", "override <name> <anthropic-id>=<gateway-id> ... [--remove ID] [--clear]")
	var remove stringList
	fs.Var(&remove, "remove", "remove the override for this Anthropic id (repeatable)")
	clear := fs.Bool("clear", false, "remove all overrides first")
	pos, err := parseArgs(c, fs, args, 1, -1)
	if err != nil {
		return err
	}
	p, err := editable(pos[0])
	if err != nil {
		return err
	}
	pairs := pos[1:]
	if len(pairs) == 0 && len(remove) == 0 && !*clear {
		return usageErr("pass <anthropic-id>=<gateway-id>, --remove ID or --clear", "nothing to change")
	}
	if *clear || p.ModelOverrides == nil {
		p.ModelOverrides = map[string]string{}
	}
	for _, r := range remove {
		delete(p.ModelOverrides, r)
	}
	for _, pair := range pairs {
		src, dst, ok := strings.Cut(pair, "=")
		if !ok || src == "" || dst == "" {
			return usageErr("use <anthropic-id>=<gateway-id>", "bad override %q", pair)
		}
		if _, err := validateModelID(src); err != nil {
			return err
		}
		if _, err := validateModelID(dst); err != nil {
			return err
		}
		p.ModelOverrides[src] = dst
	}
	return saved(c, p)
}
