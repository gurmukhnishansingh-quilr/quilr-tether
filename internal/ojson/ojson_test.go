package ojson

import (
	"errors"
	"strings"
	"testing"
)

func TestRoundTripPreservesOrder(t *testing.T) {
	in := `{"z": 1, "a": {"y": [1, 2.50, "x"], "b": null}, "m": true, "u": "<&>é\n"}`
	obj, err := ParseObject([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := Marshal(obj, "")
	want := `{"z":1,"a":{"y":[1,2.50,"x"],"b":null},"m":true,"u":"<&>é\n"}`
	if string(out) != want {
		t.Fatalf("got %s\nwant %s", out, want)
	}
}

func TestSetKeepsPositionDeleteRemoves(t *testing.T) {
	o := NewObject()
	o.Set("a", "1")
	o.Set("b", "2")
	o.Set("a", "3")
	o.Delete("b")
	o.Set("c", "4")
	if got := strings.Join(o.Keys(), ","); got != "a,c" {
		t.Fatalf("keys = %s", got)
	}
}

func TestEqualIgnoresOrder(t *testing.T) {
	a, _ := ParseObject([]byte(`{"a":1,"b":[1,{"x":"y","z":2}]}`))
	b, _ := ParseObject([]byte(`{"b":[1,{"z":2,"x":"y"}],"a":1}`))
	if !Equal(a, b) {
		t.Fatal("expected equal")
	}
	c, _ := ParseObject([]byte(`{"a":1,"b":[{"x":"y","z":2},1]}`))
	if Equal(a, c) {
		t.Fatal("array order must matter")
	}
	if !Equal([]string{"a"}, []any{"a"}) {
		t.Fatal("[]string vs []any")
	}
}

func TestSyntaxErrorPosition(t *testing.T) {
	_, err := ParseObject([]byte("{\n  \"a\": 1,\n  oops\n}"))
	var se *SyntaxError
	if !errors.As(err, &se) || se.Line != 3 {
		t.Fatalf("got %v", err)
	}
	if _, err := ParseObject([]byte(`[1]`)); err == nil {
		t.Fatal("array must be rejected")
	}
	if _, err := ParseObject([]byte(`{} {}`)); err == nil {
		t.Fatal("trailing data must be rejected")
	}
	if _, err := ParseObject([]byte("\xef\xbb\xbf{\"a\":1}")); err != nil {
		t.Fatalf("BOM: %v", err)
	}
}

func TestDetectIndent(t *testing.T) {
	cases := map[string]string{"{\n    \"a\": 1\n}": "    ", "{\n\t\"a\": 1\n}": "\t", "{}": "  "}
	for in, want := range cases {
		if got := DetectIndent([]byte(in)); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}
