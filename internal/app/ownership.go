package app

import (
	"fmt"
	"regexp"

	"github.com/gurmukhnishansingh-quilr/quilr-tether/internal/ojson"
)

// OwnedTop are the top-level settings keys tether manages. enforceAvailableModels
// is not in the original list, but `allow --enforce` writes it, so it must be
// cleared on switch like availableModels.
var OwnedTop = []string{"model", "availableModels", "enforceAvailableModels", "modelOverrides", "apiKeyHelper", "awsAuthRefresh", "awsCredentialExport"}

var ownedEnvExact = map[string]bool{
	"ANTHROPIC_BASE_URL":                             true,
	"ANTHROPIC_API_KEY":                              true,
	"ANTHROPIC_AUTH_TOKEN":                           true,
	"ANTHROPIC_CUSTOM_HEADERS":                       true,
	"ANTHROPIC_MODEL":                                true,
	"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY":     true,
	"CLAUDE_CODE_GATEWAY_MODEL_DISCOVERY_TIMEOUT_MS": true,
	"CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS":         true,
	"CLAUDE_CODE_USE_BEDROCK":                        true,
	"AWS_REGION":                                     true,
	"AWS_PROFILE":                                    true,
	"ANTHROPIC_BEDROCK_BASE_URL":                     true,
}

var ownedEnvPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^ANTHROPIC_DEFAULT_[A-Z0-9]+_MODEL(_NAME|_DESCRIPTION|_SUPPORTED_CAPABILITIES)?$`),
	regexp.MustCompile(`^ANTHROPIC_CUSTOM_MODEL_OPTION(_[A-Z_]+)?$`),
}

// ForeignProviderVars are never written by tether but switch the provider, so
// status and doctor report them.
var ForeignProviderVars = []string{
	"CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_USE_MANTLE",
	"ANTHROPIC_VERTEX_BASE_URL",
}

func IsOwnedEnv(name string) bool {
	if ownedEnvExact[name] {
		return true
	}
	for _, p := range ownedEnvPatterns {
		if p.MatchString(name) {
			return true
		}
	}
	return false
}

func isOwnedTop(k string) bool { return contains(OwnedTop, k) }

func isForeign(k string) bool { return contains(ForeignProviderVars, k) }

// Fragment is the owned part of a settings file that a profile produces.
type Fragment struct {
	Top *ojson.Object // owned top-level keys, in write order
	Env *ojson.Object // owned env keys, in write order
}

func NewFragment() Fragment { return Fragment{Top: ojson.NewObject(), Env: ojson.NewObject()} }

func (f Fragment) IsEmpty() bool { return f.Top.Len() == 0 && f.Env.Len() == 0 }

func (f Fragment) validate() error {
	for _, k := range f.Top.Keys() {
		if !isOwnedTop(k) {
			return fmt.Errorf("fragment contains top-level key tether does not own: %s", k)
		}
	}
	for _, k := range f.Env.Keys() {
		if !IsOwnedEnv(k) {
			return fmt.Errorf("fragment contains env key tether does not own: %s", k)
		}
	}
	return nil
}

// OwnedView extracts the owned keys from a settings object.
func OwnedView(s *ojson.Object) Fragment {
	f := NewFragment()
	for _, k := range s.Keys() {
		if isOwnedTop(k) {
			v, _ := s.Get(k)
			f.Top.Set(k, ojson.Clone(v))
		}
	}
	if env, ok := s.GetObject("env"); ok {
		for _, k := range env.Keys() {
			if IsOwnedEnv(k) {
				v, _ := env.Get(k)
				f.Env.Set(k, v)
			}
		}
	}
	return f
}

// mergeOrdered keeps non-owned keys untouched and in place. Owned keys present
// in next keep their position with the new value; owned keys absent from next
// are dropped; brand-new keys are appended.
func mergeOrdered(old, next *ojson.Object, owned func(string) bool) *ojson.Object {
	out := ojson.NewObject()
	for _, k := range old.Keys() {
		v, _ := old.Get(k)
		if !owned(k) {
			out.Set(k, v)
		} else if nv, ok := next.Get(k); ok {
			out.Set(k, ojson.Clone(nv))
		}
	}
	for _, k := range next.Keys() {
		if !out.Has(k) {
			v, _ := next.Get(k)
			out.Set(k, ojson.Clone(v))
		}
	}
	return out
}

// ApplyFragment clears every owned key, then applies f. It never mutates settings.
func ApplyFragment(settings *ojson.Object, f Fragment) (*ojson.Object, error) {
	if err := f.validate(); err != nil {
		return nil, err
	}
	var oldEnv *ojson.Object
	if raw, ok := settings.Get("env"); ok {
		e, isObj := raw.(*ojson.Object)
		if !isObj {
			return nil, ioErr("", "settings \"env\" is not an object; refusing to touch it")
		}
		oldEnv = e
	}
	var newEnv *ojson.Object
	if oldEnv != nil || f.Env.Len() > 0 {
		base := oldEnv
		if base == nil {
			base = ojson.NewObject()
		}
		newEnv = mergeOrdered(base, f.Env, IsOwnedEnv)
		// Drop the env block only if we are the ones who emptied it.
		if newEnv.Len() == 0 && oldEnv != nil && oldEnv.Len() > 0 {
			newEnv = nil
		}
	}
	next := f.Top.Clone()
	if newEnv != nil {
		next.Set("env", newEnv)
	}
	return mergeOrdered(settings, next, func(k string) bool { return isOwnedTop(k) || k == "env" }), nil
}
