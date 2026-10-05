package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Requests to Quilr's /bedrock-runtime route, which speaks the Amazon Bedrock
// Runtime InvokeModel API and verifies SigV4 signatures made with the Quilr
// key as both access key ID and secret.

const (
	bedrockAnthropicVersion = "bedrock-2023-05-31"
	// Claude Code's small/fast default on Bedrock in us-* regions; used by doctor
	// when the profile pins nothing.
	defaultBedrockModel = "us.anthropic.claude-sonnet-4-5-20250929-v1:0"
)

// bedrockModelURL builds {base}/model/{id}/{action}, escaping ":" in the model
// ID as the AWS SDKs do.
func bedrockModelURL(base, model, action string) string {
	return base + "/model/" + strings.ReplaceAll(url.PathEscape(model), ":", "%3A") + "/" + action
}

func bedrockBody(maxTokens int, text string) []byte {
	b, _ := json.Marshal(map[string]any{
		"anthropic_version": bedrockAnthropicVersion,
		"max_tokens":        maxTokens,
		"messages":          []any{map[string]any{"role": "user", "content": text}},
	})
	return b
}

func newSignedRequest(method, rawURL string, body []byte, key, region string, extra map[string]string) (*http.Request, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, rawURL, rdr)
	if err != nil {
		return nil, &GatewayError{err}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "tether/"+Version)
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	signV4(req, body, key, key, region, "bedrock", time.Now())
	return req, nil
}

func bedrockCall(method, rawURL string, body []byte, key, region string, extra map[string]string, timeout time.Duration) (*Response, error) {
	req, err := newSignedRequest(method, rawURL, body, key, region, extra)
	if err != nil {
		return nil, err
	}
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

// bedrockStream calls invoke-with-response-stream and timestamps each chunk
// of the binary event stream as it arrives.
func bedrockStream(rawURL string, body []byte, key, region string, extra map[string]string, timeout time.Duration) (*StreamResult, []byte, error) {
	req, err := newSignedRequest("POST", rawURL, body, key, region, extra)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/vnd.amazon.eventstream")
	start := time.Now()
	resp, err := newHTTPClient(timeout).Do(req)
	if err != nil {
		return nil, nil, &GatewayError{unwrapURLError(err)}
	}
	defer resp.Body.Close()
	res := &StreamResult{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), FirstEvent: -1}
	var all bytes.Buffer
	buf := make([]byte, 32*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			now := time.Since(start)
			if res.FirstEvent < 0 {
				res.FirstEvent = now
			}
			res.EventTimes = append(res.EventTimes, now)
			all.Write(buf[:n])
		}
		if rerr != nil {
			if rerr != io.EOF {
				res.Error = rerr.Error()
			}
			break
		}
	}
	res.Total = time.Since(start)
	if resp.StatusCode != 200 {
		r := Response{Status: resp.StatusCode, Body: all.Bytes()}
		res.Error = r.ErrorMessage()
	}
	return res, all.Bytes(), nil
}

// bedrockErrorMessage reads Bedrock-style errors: {"__type": ..., "message": ...}.
func bedrockErrorMessage(r *Response) (kind, msg string) {
	var e struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
	}
	if json.Unmarshal(r.Body, &e) == nil && (e.Type != "" || e.Message != "") {
		return e.Type, truncate(e.Message, 200)
	}
	return "", r.ErrorMessage()
}
