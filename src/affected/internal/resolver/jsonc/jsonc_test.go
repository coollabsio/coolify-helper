package jsonc

import (
	"reflect"
	"testing"
)

func TestUnmarshal(t *testing.T) {
	var v map[string]any
	src := "\xef\xbb\xbf{\n  // line comment\n  \"a\": \"http://x//y\", /* block */\n  \"b\": [1, 2,],\n  \"c\": \"q\\\"//\",\n}\n"
	if err := Unmarshal([]byte(src), &v); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"a": "http://x//y", "b": []any{1.0, 2.0}, "c": `q"//`}
	if !reflect.DeepEqual(v, want) {
		t.Errorf("got %v", v)
	}
	for _, bad := range []string{`{"a": "x`, `{/* x`, `{"a":}`} {
		if err := Unmarshal([]byte(bad), &v); err == nil {
			t.Errorf("Unmarshal(%q): want an error", bad)
		}
	}
}
