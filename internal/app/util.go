package app

import "github.com/gurmukhnishansingh-quilr/quilr-tether/internal/ojson"

// NewOrderedStrings turns a map into an ojson object with sorted keys.
func NewOrderedStrings(m map[string]string) *ojson.Object {
	o := ojson.NewObject()
	for _, k := range sortedKeys(m) {
		o.Set(k, m[k])
	}
	return o
}

func jsonString(v any) string {
	b, err := ojson.Marshal(v, "")
	if err != nil {
		return "?"
	}
	return string(b)
}
