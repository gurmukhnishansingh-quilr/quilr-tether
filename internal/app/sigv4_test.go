package app

import (
	"net/http"
	"testing"
	"time"
)

// AWS SigV4 test suite, "get-vanilla" and "get-vanilla-query-order-key-case".
func TestSignV4AWSTestVectors(t *testing.T) {
	now := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	cases := map[string]string{
		"https://example.amazonaws.com/":                             "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31",
		"https://example.amazonaws.com/?Param2=value2&Param1=value1": "b97d918cfa904a5beff61c982a1b6f458b799221646efd99d3219ec94cdf2500",
	}
	for u, want := range cases {
		req, _ := http.NewRequest("GET", u, nil)
		signV4(req, nil, "AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "us-east-1", "service", now)
		got := req.Header.Get("Authorization")
		wantAuth := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=" + want
		if got != wantAuth {
			t.Errorf("%s\n got %s\nwant %s", u, got, wantAuth)
		}
	}
}

func TestCanonicalURIDoubleEncodesModelIDs(t *testing.T) {
	req, _ := http.NewRequest("POST", bedrockModelURL("https://gw.example/bedrock-runtime", "us.anthropic.claude-x-v1:0", "invoke"), nil)
	if got := req.URL.EscapedPath(); got != "/bedrock-runtime/model/us.anthropic.claude-x-v1%3A0/invoke" {
		t.Fatalf("wire path %s", got)
	}
	if got := canonicalURI(req.URL); got != "/bedrock-runtime/model/us.anthropic.claude-x-v1%253A0/invoke" {
		t.Fatalf("canonical %s", got)
	}
}
