// Package schematest validates documents against the JSON Schemas in schema/,
// for tests: every response the tests produce must match the published contract.
package schematest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// BaseURL is the $id prefix of every schema.
const BaseURL = "https://coolify.io/schemas/coolify-affected/v1/"

var (
	once     sync.Once
	compiled map[string]*jsonschema.Schema
	loadErr  error
)

// Dir returns the schema directory of the module.
func Dir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "schema")
}

func load() {
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	files, err := filepath.Glob(filepath.Join(Dir(), "*.json"))
	if err != nil || len(files) == 0 {
		loadErr = fmt.Errorf("no schemas in %s", Dir())
		return
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			loadErr = err
			return
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			loadErr = fmt.Errorf("%s: %w", f, err)
			return
		}
		if err := c.AddResource(BaseURL+filepath.Base(f), doc); err != nil {
			loadErr = err
			return
		}
	}
	compiled = map[string]*jsonschema.Schema{}
	for _, f := range files {
		s, err := c.Compile(BaseURL + filepath.Base(f))
		if err != nil {
			loadErr = fmt.Errorf("%s: %w", f, err)
			return
		}
		compiled[filepath.Base(f)] = s
	}
}

// Validate validates JSON against a schema file name, such as "evaluate-response.json".
func Validate(name string, data []byte) error {
	once.Do(load)
	if loadErr != nil {
		return loadErr
	}
	s, ok := compiled[name]
	if !ok {
		return fmt.Errorf("no schema %s", name)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	return s.Validate(doc)
}

// Check fails the test when v (a value or JSON bytes) does not match the schema.
func Check(t testing.TB, name string, v any) {
	t.Helper()
	b, ok := v.([]byte)
	if !ok {
		var err error
		if b, err = json.Marshal(v); err != nil {
			t.Fatal(err)
		}
	}
	if err := Validate(name, b); err != nil {
		doc := string(b)
		if len(doc) > 400 {
			doc = doc[:400] + "…"
		}
		t.Fatalf("%s does not match %s: %v", doc, name, err)
	}
}
