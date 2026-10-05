package app

import (
	"bytes"
	"fmt"
	"strings"
	"time"
)

// Doctor checks for quilr-bedrock profiles: Claude Code in Bedrock mode
// (CLAUDE_CODE_USE_BEDROCK) talking to Quilr's /bedrock-runtime route with
// SigV4 signed by the Quilr key.

func (d *doctorRun) bedrockHeaders() map[string]string {
	h := d.p.HeaderMap()
	h["X-Conversation-Id"] = d.tag
	return h
}

// bedrockModel is the model doctor pings: the pinned Sonnet, else the
// default model, any other pin, or Claude Code's own small/fast default.
func (d *doctorRun) bedrockModel() string {
	for _, m := range []string{d.p.Pins["sonnet"], d.p.Model, d.p.Pins["haiku"], d.p.Pins["opus"], d.p.Pins["fable"]} {
		if m != "" {
			return m
		}
	}
	return defaultBedrockModel
}

// bedrockFix turns a Bedrock-route error into a one-line fix.
func (d *doctorRun) bedrockFix(status int, kind, msg string) string {
	switch {
	case strings.Contains(strings.ToLower(msg), "streaming is not enabled"):
		return "Claude Code streams every response, so it can't run on this route until Quilr enables Bedrock streaming " +
			"on the gateway; ask Quilr to enable it, or use a key for the anthropic_messages route (--type quilr)"
	case strings.Contains(msg, "not enabled"):
		return fmt.Sprintf("pin a Bedrock model ID that is enabled on your Quilr key: tether pin %s --sonnet ID --haiku ID", d.p.Name)
	case kind == "SignatureDoesNotMatch":
		return "the gateway rejected the signature; check the key with `tether profile set-key " + d.p.Name + "`"
	case status == 401 || status == 403:
		return "replace the key with `tether profile set-key " + d.p.Name + "` (it must be a key for Quilr's 'bedrock' provider type)"
	case status == 404:
		return "wrong region or base URL; Quilr's Bedrock route ends in /bedrock-runtime"
	}
	return "see the gateway logs"
}

func (d *doctorRun) bedrockChecks() []Check {
	model := d.bedrockModel()
	region := d.p.SigningRegion()
	base := d.p.GatewayRoot()
	var checks []Check

	// 3. inference
	inference := Check{ID: 3, Name: "inference"}
	resp, err := bedrockCall("POST", bedrockModelURL(base, model, "invoke"), bedrockBody(16, "ping"), d.key, region, d.bedrockHeaders(), d.timeout)
	switch {
	case err != nil:
		inference.Status, inference.Detail = "fail", "POST /model/.../invoke: "+err.Error()
		inference.Fix = "check DNS, proxy (HTTPS_PROXY) and the region"
	case resp.Status == 200:
		inference.Status = "pass"
		inference.Detail = fmt.Sprintf("%s: 200 in %s (SigV4, %s)", model, ms(resp.Elapsed), region)
		inference.Data = map[string]any{"status": 200, "latency_ms": resp.Elapsed.Milliseconds(), "model": model}
	default:
		kind, msg := bedrockErrorMessage(resp)
		inference.Status = "fail"
		inference.Detail = fmt.Sprintf("%s: HTTP %d: %s", model, resp.Status, msg)
		inference.Fix = d.bedrockFix(resp.Status, kind, msg)
		inference.Data = map[string]any{"status": resp.Status, "error_type": kind, "model": model}
	}
	checks = append(checks, inference)

	// 4. streaming: Claude Code uses InvokeModelWithResponseStream.
	checks = append(checks, d.bedrockStreamCheck(base, model, region))

	checks = append(checks,
		Check{ID: 5, Name: "beta passthrough", Status: "skip", Detail: "not used on the Bedrock route (Claude Code sends only Bedrock-supported features)"},
		Check{ID: 6, Name: "token counting", Status: "skip", Detail: "not checked on the Bedrock route; Claude Code estimates when unavailable"},
	)

	// 7. model discovery: Claude Code lists inference profiles at startup.
	disc, err := bedrockCall("GET", base+"/inference-profiles?type=SYSTEM_DEFINED", nil, d.key, region, d.bedrockHeaders(), d.timeout)
	pinned := len(d.p.Pins) > 0 || d.p.Model != ""
	switch {
	case err == nil && disc.Status == 200:
		checks = append(checks, Check{ID: 7, Name: "model discovery", Status: "pass", Detail: "inference profiles are listed by the gateway"})
	case pinned:
		checks = append(checks, Check{ID: 7, Name: "model discovery", Status: "pass",
			Detail: "the gateway does not list inference profiles; not needed because models are pinned"})
	default:
		checks = append(checks, Check{ID: 7, Name: "model discovery", Status: "warn",
			Detail: "the gateway does not list inference profiles and no models are pinned, so Claude Code falls back to its built-in Bedrock IDs",
			Fix:    fmt.Sprintf("pin models enabled on your key: tether pin %s --sonnet ID --haiku ID", d.p.Name)})
	}

	// 8. every pinned model is enabled on the key.
	checks = append(checks, d.bedrockPinsCheck(model, inference))
	checks = append(checks, Check{ID: 9, Name: "log tag", Status: "pass", Detail: "requests tagged X-Conversation-Id: " + d.tag,
		Fix: "search the Quilr logs for this id"})
	return checks
}

func (d *doctorRun) bedrockStreamCheck(base, model, region string) Check {
	c := Check{ID: 4, Name: "streaming"}
	res, raw, err := bedrockStream(bedrockModelURL(base, model, "invoke-with-response-stream"),
		bedrockBody(64, streamPrompt), d.key, region, d.bedrockHeaders(), d.timeout)
	switch {
	case err != nil:
		c.Status, c.Detail, c.Fix = "fail", "stream request failed: "+err.Error(), "check proxy and gateway streaming support"
	case res.Status != 200:
		c.Status, c.Detail, c.Fix = "fail", fmt.Sprintf("HTTP %d: %s", res.Status, res.Error), d.bedrockFix(res.Status, "", res.Error)
	case strings.Contains(res.ContentType, "text/event-stream"):
		c.Status = "fail"
		c.Detail = "the gateway returns text/event-stream; Claude Code expects Bedrock's application/vnd.amazon.eventstream"
		c.Fix = "relay the Bedrock event stream unchanged, or use the anthropic_messages route (--type quilr)"
	case bytes.Contains(raw, []byte(":exception-type")):
		c.Status, c.Detail, c.Fix = "fail", "the stream carried an error event: "+truncate(printable(raw), 160), "see the gateway logs"
	case len(raw) == 0:
		c.Status, c.Detail, c.Fix = "fail", "empty response stream", "see the gateway logs"
	default:
		span := res.EventTimes[len(res.EventTimes)-1] - res.FirstEvent
		c.Status = "pass"
		c.Detail = fmt.Sprintf("%s, first bytes after %s, %d chunks over %s", res.ContentType, ms(res.FirstEvent), len(res.EventTimes), ms(span))
		if res.ContentType == "" {
			c.Status = "warn"
			c.Fix = "the gateway drops Content-Type; Claude Code assumes the Bedrock event stream, which works only if the body is unmodified"
		} else if len(res.EventTimes) >= 3 && res.Total > 500*time.Millisecond && span < 50*time.Millisecond && res.FirstEvent > res.Total*8/10 {
			c.Status = "warn"
			c.Fix = "all chunks arrived at once, so the gateway appears to buffer; disable response buffering"
		}
		c.Data = map[string]any{"content_type": res.ContentType, "chunks": len(res.EventTimes), "first_byte_ms": res.FirstEvent.Milliseconds()}
	}
	return c
}

func (d *doctorRun) bedrockPinsCheck(pinged string, inference Check) Check {
	seen := map[string]bool{}
	var ids []string
	for _, m := range append([]string{d.p.Model}, mapValuesTiered(d.p.Pins)...) {
		if m != "" && !seen[m] {
			seen[m] = true
			ids = append(ids, m)
		}
	}
	if len(ids) == 0 {
		return Check{ID: 8, Name: "pinned models", Status: "warn", Detail: "no Bedrock models pinned",
			Fix: fmt.Sprintf("tether pin %s --sonnet ID --haiku ID", d.p.Name)}
	}
	var missing []string
	for _, m := range ids {
		if m == pinged {
			if inference.Status != "pass" {
				missing = append(missing, m)
			}
			continue
		}
		resp, err := bedrockCall("POST", bedrockModelURL(d.p.GatewayRoot(), m, "invoke"), bedrockBody(1, "ping"),
			d.key, d.p.SigningRegion(), d.bedrockHeaders(), d.timeout)
		if err != nil || resp.Status != 200 {
			missing = append(missing, m)
		}
	}
	if len(missing) > 0 {
		return Check{ID: 8, Name: "pinned models", Status: "warn", Detail: "not usable with this key: " + strings.Join(missing, ", "),
			Fix: "enable them on the key in Quilr, or change the pins with `tether pin`", Data: map[string]any{"missing": missing}}
	}
	return Check{ID: 8, Name: "pinned models", Status: "pass", Detail: fmt.Sprintf("all %d pinned models answer", len(ids))}
}

func mapValuesTiered(pins map[string]string) []string {
	var out []string
	for _, t := range Tiers {
		if m, ok := pins[t]; ok {
			out = append(out, m)
		}
	}
	return out
}

func printable(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		if c >= 32 && c < 127 {
			sb.WriteByte(c)
		} else {
			sb.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(sb.String()), " ")
}
