package app

import (
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/gurmukhnishansingh-quilr/quilr-tether/internal/ojson"
)

var (
	knownMu      sync.Mutex
	knownSecrets = map[string]bool{}

	maskPrefix   = regexp.MustCompile(`^((?:[A-Za-z0-9]+-){1,2})`)
	secretName   = regexp.MustCompile(`(?i)(API_KEY|AUTH_TOKEN|SECRET|PASSWORD|SESSION_TOKEN)`)
	secretHeader = regexp.MustCompile(`(?i)^(authorization|x-api-key|proxy-authorization|api-key)$`)
	// Bare key-shaped strings that slipped through, e.g. inside an error body.
	keyShaped = regexp.MustCompile(`\bsk-[A-Za-z0-9]+-[A-Za-z0-9_\-]{12,}`)
)

// Mask turns sk-quilr-0123456789abcdef into sk-quilr-…cdef. Short values reveal nothing.
func Mask(v string) string {
	if v == "" {
		return ""
	}
	prefix := ""
	if m := maskPrefix.FindStringSubmatch(v); m != nil && len(m[1]) <= 12 {
		prefix = m[1]
	}
	rest := len(v) - len(prefix)
	if rest < 12 {
		if rest > 8 {
			return prefix + "…"
		}
		return "…"
	}
	return prefix + "…" + v[len(v)-4:]
}

// RegisterSecret remembers a secret so later output containing it is masked.
func RegisterSecret(s string) {
	if len(s) < 6 {
		return
	}
	knownMu.Lock()
	knownSecrets[s] = true
	knownMu.Unlock()
}

func resetSecrets() {
	knownMu.Lock()
	knownSecrets = map[string]bool{}
	knownMu.Unlock()
}

// Redact masks every registered secret and anything key-shaped.
func Redact(text string) string {
	knownMu.Lock()
	secrets := make([]string, 0, len(knownSecrets))
	for s := range knownSecrets {
		secrets = append(secrets, s)
	}
	knownMu.Unlock()
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, s := range secrets {
		text = strings.ReplaceAll(text, s, Mask(s))
	}
	return keyShaped.ReplaceAllStringFunc(text, Mask)
}

func isSecretName(name string) bool { return secretName.MatchString(name) }

func maskHeaders(raw string) string {
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		name, value, ok := strings.Cut(line, ":")
		if ok && secretHeader.MatchString(strings.TrimSpace(name)) {
			lines[i] = name + ": " + Mask(strings.TrimSpace(value))
		}
	}
	return strings.Join(lines, "\n")
}

// MaskSettings returns a copy of a settings object that is safe to display.
func MaskSettings(s *ojson.Object) *ojson.Object {
	out := s.Clone()
	env, ok := out.GetObject("env")
	if !ok {
		return out
	}
	for _, k := range env.Keys() {
		v, ok := env.GetString(k)
		if !ok {
			continue
		}
		if isSecretName(k) {
			env.Set(k, Mask(v))
		} else if k == "ANTHROPIC_CUSTOM_HEADERS" {
			env.Set(k, maskHeaders(v))
		}
	}
	return out
}

// MaskMCPServers returns a copy of an mcpServers object with secret header
// and env values masked.
func MaskMCPServers(servers *ojson.Object) *ojson.Object {
	out := servers.Clone()
	for _, name := range out.Keys() {
		srv, ok := out.GetObject(name)
		if !ok {
			continue
		}
		if h, ok := srv.GetObject("headers"); ok {
			for _, k := range h.Keys() {
				if v, ok := h.GetString(k); ok && (secretHeader.MatchString(k) || isSecretName(strings.ReplaceAll(k, "-", "_"))) {
					h.Set(k, Mask(v))
				}
			}
		}
		if env, ok := srv.GetObject("env"); ok {
			for _, k := range env.Keys() {
				if v, ok := env.GetString(k); ok && isSecretName(k) {
					env.Set(k, Mask(v))
				}
			}
		}
	}
	return out
}

// maskEnvValue masks a single env value for display.
func maskEnvValue(k, v string) string {
	if isSecretName(k) {
		return Mask(v)
	}
	if k == "ANTHROPIC_CUSTOM_HEADERS" {
		return maskHeaders(v)
	}
	return v
}
