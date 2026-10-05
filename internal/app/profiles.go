package app

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

var (
	ProfileTypes = []string{"quilr", "quilr-bedrock", "anthropic", "bedrock"}
	Tiers        = []string{"opus", "sonnet", "haiku", "fable"}

	QuilrRegions = map[string]string{
		"auto":    "https://guardrails.quilr.ai",
		"usa-1":   "https://guardrails-usa-1.quilr.ai",
		"usa-2":   "https://guardrails-usa-2.quilr.ai",
		"india-1": "https://guardrails-india-1.quilr.ai",
		"jp-1":    "https://guardrails-jp-1.quilr.ai",
	}
	QuilrRegionNames = []string{"auto", "usa-1", "usa-2", "india-1", "jp-1"}

	nameRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	modelIDRE = regexp.MustCompile(`^\S+$`)
)

const (
	quilrPath        = "/anthropic_messages"
	quilrBedrockPath = "/bedrock-runtime"
	defaultAWSRegion = "us-east-1"
)

// Profile is one entry in profiles.toml. Keys are never stored here.
type Profile struct {
	Name string `toml:"-"`
	Type string `toml:"type"`
	// quilr and quilr-bedrock
	Region             string `toml:"region,omitempty"`
	BaseURL            string `toml:"base_url,omitempty"` // overrides region, e.g. staging
	Email              string `toml:"email,omitempty"`
	ProviderLabel      string `toml:"provider_label,omitempty"`
	Discovery          *bool  `toml:"discovery,omitempty"` // nil = on
	DiscoveryTimeoutMS int    `toml:"discovery_timeout_ms,omitempty"`
	BedrockBacked      bool   `toml:"bedrock_backed,omitempty"`
	KeyBackend         string `toml:"key_backend,omitempty"`
	// bedrock (aws_region is also the SigV4 signing region for quilr-bedrock)
	AWSRegion  string `toml:"aws_region,omitempty"`
	AWSProfile string `toml:"aws_profile,omitempty"`
	SSORefresh bool   `toml:"sso_refresh,omitempty"`
	// models
	Model            string            `toml:"model,omitempty"`
	Pins             map[string]string `toml:"pins,omitempty"`
	AvailableModels  []string          `toml:"available_models,omitempty"`
	EnforceAvailable bool              `toml:"enforce_available,omitempty"`
	ModelOverrides   map[string]string `toml:"model_overrides,omitempty"`
}

func validateName(name string) error {
	if !nameRE.MatchString(name) {
		return usageErr("use letters, digits, '-' and '_'", "invalid profile name %q", name)
	}
	return nil
}

func validateModelID(id string) (string, error) {
	if id == "" || !modelIDRE.MatchString(id) {
		return "", usageErr("", "invalid model id %q", id)
	}
	return id, nil
}

func (p *Profile) Validate() error {
	if err := validateName(p.Name); err != nil {
		return err
	}
	if !contains(ProfileTypes, p.Type) {
		return usageErr("", "unknown profile type %q; expected quilr, quilr-bedrock, anthropic or bedrock", p.Type)
	}
	if p.IsQuilr() && p.BaseURL == "" {
		if _, ok := QuilrRegions[p.Region]; !ok {
			return usageErr("", "unknown Quilr region %q; expected %s", p.Region, strings.Join(QuilrRegionNames, ", "))
		}
	}
	if p.Type == "bedrock" && (p.AWSRegion == "" || p.AWSProfile == "") {
		return usageErr("", "bedrock profiles need --aws-region and --aws-profile")
	}
	for t := range p.Pins {
		if !contains(Tiers, t) {
			return usageErr("", "unknown model tier %q", t)
		}
	}
	return nil
}

// IsQuilr is true for both Quilr routes: quilr (/anthropic_messages, the
// Anthropic Messages format) and quilr-bedrock (/bedrock-runtime, the Bedrock
// Runtime format signed with the Quilr key).
func (p *Profile) IsQuilr() bool { return p.Type == "quilr" || p.Type == "quilr-bedrock" }

func (p *Profile) NeedsKey() bool { return p.IsQuilr() }

// SigningRegion is the AWS region quilr-bedrock signs requests for.
func (p *Profile) SigningRegion() string {
	if p.AWSRegion != "" {
		return p.AWSRegion
	}
	return defaultAWSRegion
}

func (p *Profile) DiscoveryOn() bool { return p.Discovery == nil || *p.Discovery }

// GatewayRoot is the gateway URL Claude Code is pointed at, never with a
// trailing slash: ANTHROPIC_BASE_URL for quilr, ANTHROPIC_BEDROCK_BASE_URL for
// quilr-bedrock.
func (p *Profile) GatewayRoot() string {
	if p.BaseURL != "" {
		return strings.TrimRight(p.BaseURL, "/")
	}
	region := p.Region
	if region == "" {
		region = "auto"
	}
	if p.Type == "quilr-bedrock" {
		return QuilrRegions[region] + quilrBedrockPath
	}
	return QuilrRegions[region] + quilrPath
}

func (p *Profile) CustomHeaders(includeEmail bool) string {
	var h []string
	if p.Email != "" && includeEmail {
		h = append(h, "X-User-Email: "+p.Email)
	}
	if p.ProviderLabel != "" {
		h = append(h, "X-Provider-Label: "+p.ProviderLabel)
	}
	return strings.Join(h, "\n")
}

func (p *Profile) HeaderMap() map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(p.CustomHeaders(true), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

func (p *Profile) Target() string {
	switch p.Type {
	case "quilr":
		return p.GatewayRoot()
	case "quilr-bedrock":
		return p.GatewayRoot() + " (Bedrock format, " + p.SigningRegion() + ")"
	case "bedrock":
		return "bedrock " + p.AWSRegion + " (AWS profile " + p.AWSProfile + ")"
	}
	return "claude.ai login / Console key"
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// FragmentOpts selects how the key is delivered.
type FragmentOpts struct {
	Scope         string
	HelperCommand string // apiKeyHelper (quilr) or awsCredentialExport (quilr-bedrock) value
	PlaintextKey  string // env.ANTHROPIC_API_KEY value
	OmitEmail     bool   // managed export: X-User-Email is per-user
}

// BuildFragment returns the owned keys `tether use` writes for this profile.
// With neither HelperCommand nor PlaintextKey, the fragment carries no
// credential (managed export).
func BuildFragment(p *Profile, o FragmentOpts) Fragment {
	f := NewFragment()
	if p.Type == "anthropic" {
		return f
	}
	if p.Type == "quilr" {
		f.Env.Set("ANTHROPIC_BASE_URL", p.GatewayRoot())
		if o.PlaintextKey != "" {
			f.Env.Set("ANTHROPIC_API_KEY", o.PlaintextKey)
		} else if o.HelperCommand != "" {
			f.Top.Set("apiKeyHelper", o.HelperCommand)
		}
		if h := p.CustomHeaders(!o.OmitEmail); h != "" {
			f.Env.Set("ANTHROPIC_CUSTOM_HEADERS", h)
		}
		if p.DiscoveryOn() {
			f.Env.Set("CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY", "1")
			if p.DiscoveryTimeoutMS > 0 {
				f.Env.Set("CLAUDE_CODE_GATEWAY_MODEL_DISCOVERY_TIMEOUT_MS", strconv.Itoa(p.DiscoveryTimeoutMS))
			}
		}
		if p.BedrockBacked {
			f.Env.Set("CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS", "1")
		}
	}
	if p.Type == "quilr-bedrock" {
		f.Env.Set("CLAUDE_CODE_USE_BEDROCK", "1")
		f.Env.Set("ANTHROPIC_BEDROCK_BASE_URL", p.GatewayRoot())
		f.Env.Set("AWS_REGION", p.SigningRegion())
		if o.HelperCommand != "" {
			f.Top.Set("awsCredentialExport", o.HelperCommand)
		}
		if h := p.CustomHeaders(!o.OmitEmail); h != "" {
			f.Env.Set("ANTHROPIC_CUSTOM_HEADERS", h)
		}
	}
	if p.Type == "bedrock" {
		f.Env.Set("CLAUDE_CODE_USE_BEDROCK", "1")
		f.Env.Set("AWS_REGION", p.AWSRegion)
		f.Env.Set("AWS_PROFILE", p.AWSProfile)
		if p.SSORefresh {
			f.Top.Set("awsAuthRefresh", "aws sso login --profile "+p.AWSProfile)
		}
	}
	for _, t := range Tiers {
		if id, ok := p.Pins[t]; ok {
			f.Env.Set("ANTHROPIC_DEFAULT_"+strings.ToUpper(t)+"_MODEL", id)
		}
	}
	if p.Model != "" {
		f.Top.Set("model", p.Model)
	}
	if len(p.AvailableModels) > 0 {
		arr := make([]any, len(p.AvailableModels))
		for i, m := range p.AvailableModels {
			arr[i] = m
		}
		f.Top.Set("availableModels", arr)
		// Claude Code reads enforceAvailableModels from managed settings only.
		if p.EnforceAvailable && o.Scope == "managed" {
			f.Top.Set("enforceAvailableModels", true)
		}
	}
	if len(p.ModelOverrides) > 0 {
		ov := NewOrderedStrings(p.ModelOverrides)
		f.Top.Set("modelOverrides", ov)
	}
	return f
}

// --- storage ---------------------------------------------------------------

type profilesFile struct {
	Profiles map[string]*Profile `toml:"profiles"`
}

func ProfilesPath() string { return filepath.Join(ConfigDir(), "profiles.toml") }

func LoadProfiles() (map[string]*Profile, error) {
	path := ProfilesPath()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]*Profile{}, nil
	}
	if err != nil {
		return nil, wrapIO("read", path, err)
	}
	var pf profilesFile
	md, err := toml.Decode(string(data), &pf)
	if err != nil {
		return nil, ioErr("", "%s is not valid TOML: %v", path, err)
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		return nil, ioErr("", "%s has unknown field(s): %v", path, undec)
	}
	out := map[string]*Profile{}
	for name, p := range pf.Profiles {
		p.Name = name
		if err := p.Validate(); err != nil {
			return nil, ioErr("", "profile %q in %s: %v", name, path, err)
		}
		out[name] = p
	}
	return out, nil
}

func SaveProfiles(all map[string]*Profile) error {
	var buf bytes.Buffer
	buf.WriteString("# tether profiles. Managed by `tether profile ...`; keys are never stored here.\n\n")
	if err := toml.NewEncoder(&buf).Encode(profilesFile{Profiles: all}); err != nil {
		return ioErr("", "cannot encode profiles: %v", err)
	}
	return AtomicWrite(ProfilesPath(), buf.Bytes(), false)
}

func GetProfile(name string) (*Profile, error) {
	all, err := LoadProfiles()
	if err != nil {
		return nil, err
	}
	p, ok := all[name]
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
		return nil, usageErr("create one with `tether profile add`", "no profile named %q (known: %s)", name, known)
	}
	return p, nil
}

func PutProfile(p *Profile) error {
	all, err := LoadProfiles()
	if err != nil {
		return err
	}
	all[p.Name] = p
	return SaveProfiles(all)
}
