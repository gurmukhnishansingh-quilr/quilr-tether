package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const anthropicVersion = "2023-06-01"

// Version is set by main (and -ldflags at build time).
var Version = "dev"

// newHTTPClient never follows redirects: Claude Code treats a redirect on
// discovery as failure so the credential can't leak to the redirect target.
func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// GatewayError is a network-level failure: DNS, TLS, refused, timeout.
type GatewayError struct{ Err error }

func (e *GatewayError) Error() string { return e.Err.Error() }

type Response struct {
	Status   int
	Header   http.Header
	Body     []byte
	Elapsed  time.Duration
	Location string
}

func (r *Response) ErrorMessage() string {
	var data map[string]any
	if err := json.Unmarshal(r.Body, &data); err == nil {
		if e, ok := data["error"].(map[string]any); ok {
			if m, ok := e["message"].(string); ok {
				return truncate(m, 200)
			}
		}
		if m, ok := data["message"].(string); ok {
			return truncate(m, 200)
		}
	}
	return truncate(strings.TrimSpace(string(r.Body)), 200)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func buildHeaders(key string, extra map[string]string, bearer bool) http.Header {
	h := http.Header{}
	h.Set("anthropic-version", anthropicVersion)
	h.Set("content-type", "application/json")
	h.Set("user-agent", "tether/"+Version)
	if key != "" {
		h.Set("x-api-key", key)
		if bearer {
			h.Set("authorization", "Bearer "+key)
		}
	}
	for k, v := range extra {
		h.Set(k, v)
	}
	return h
}

func httpCall(method, url string, headers http.Header, body any, timeout time.Duration) (*Response, error) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return nil, &GatewayError{err}
	}
	req.Header = headers
	start := time.Now()
	resp, err := newHTTPClient(timeout).Do(req)
	if err != nil {
		return nil, &GatewayError{unwrapURLError(err)}
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &GatewayError{err}
	}
	return &Response{resp.StatusCode, resp.Header, payload, time.Since(start), resp.Header.Get("Location")}, nil
}

func unwrapURLError(err error) error {
	var ue interface{ Unwrap() error }
	if errors.As(err, &ue) {
		if inner := ue.Unwrap(); inner != nil {
			return inner
		}
	}
	return err
}

// StreamResult timestamps every SSE event relative to the request start.
type StreamResult struct {
	Status      int
	ContentType string
	FirstEvent  time.Duration // -1 when none arrived
	Total       time.Duration
	EventTimes  []time.Duration
	EventNames  []string
	Error       string
}

func httpStream(url string, headers http.Header, body map[string]any, timeout time.Duration) (*StreamResult, error) {
	body["stream"] = true
	b, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", url, bytes.NewReader(b))
	if err != nil {
		return nil, &GatewayError{err}
	}
	req.Header = headers
	start := time.Now()
	resp, err := newHTTPClient(timeout).Do(req)
	if err != nil {
		return nil, &GatewayError{unwrapURLError(err)}
	}
	defer resp.Body.Close()
	res := &StreamResult{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), FirstEvent: -1}
	if resp.StatusCode != 200 {
		payload, _ := io.ReadAll(resp.Body)
		r := Response{Status: resp.StatusCode, Body: payload}
		res.Error = r.ErrorMessage()
		res.Total = time.Since(start)
		return res, nil
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event:") {
			now := time.Since(start)
			if res.FirstEvent < 0 {
				res.FirstEvent = now
			}
			res.EventTimes = append(res.EventTimes, now)
			res.EventNames = append(res.EventNames, strings.TrimSpace(line[6:]))
		}
	}
	res.Total = time.Since(start)
	if err := sc.Err(); err != nil {
		res.Error = err.Error()
	}
	return res, nil
}

// --- model discovery -------------------------------------------------------

type ModelEntry struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
}

type Discovery struct {
	Status   int
	Elapsed  time.Duration
	Entries  []ModelEntry
	Error    string
	Location string
}

func (d *Discovery) IDs() []string {
	ids := make([]string, len(d.Entries))
	for i, e := range d.Entries {
		ids[i] = e.ID
	}
	return ids
}

// ClaudeFilter mirrors Claude Code (v2.1.223+): keep an id when it contains
// "claude" or "anthropic" anywhere, case-insensitively.
func ClaudeFilter(ids []string) (kept, dropped []string) {
	for _, id := range ids {
		l := strings.ToLower(id)
		if strings.Contains(l, "claude") || strings.Contains(l, "anthropic") {
			kept = append(kept, id)
		} else {
			dropped = append(dropped, id)
		}
	}
	return kept, dropped
}

// Discover runs GET {base}/v1/models?limit=1000 the way Claude Code does:
// both credential headers, no redirects.
func Discover(base, key string, extra map[string]string, timeout time.Duration) (*Discovery, error) {
	h := buildHeaders(key, extra, true)
	h.Del("content-type")
	resp, err := httpCall("GET", base+"/v1/models?limit=1000", h, nil, timeout)
	if err != nil {
		return nil, err
	}
	d := &Discovery{Status: resp.Status, Elapsed: resp.Elapsed, Location: resp.Location}
	if resp.Status >= 300 && resp.Status < 400 {
		d.Error = fmt.Sprintf("redirected to %s", resp.Location)
		return d, nil
	}
	if resp.Status != 200 {
		d.Error = resp.ErrorMessage()
		return d, nil
	}
	var raw struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &raw); err != nil || raw.Data == nil {
		d.Error = `unexpected shape; expected {"data":[{"id": ...}]}`
		return d, nil
	}
	for _, item := range raw.Data {
		var e ModelEntry
		var probe map[string]any
		if json.Unmarshal(item, &probe) != nil {
			d.Error = `unexpected shape; expected {"data":[{"id": ...}]}`
			d.Entries = nil
			return d, nil
		}
		if _, ok := probe["id"].(string); !ok {
			d.Error = `an entry in "data" has no string "id"`
			d.Entries = nil
			return d, nil
		}
		_ = json.Unmarshal(item, &e)
		d.Entries = append(d.Entries, e)
	}
	return d, nil
}
