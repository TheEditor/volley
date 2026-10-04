package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Validate checks embedded draft 2020-12 schemas. Resource loading is restricted
// to the embedded schema set; validation never fetches a network resource.
var schemaMu sync.Mutex
var schemas map[string]*jsonschema.Schema

func compiledSchema(name string) (*jsonschema.Schema, error) {
	schemaMu.Lock()
	defer schemaMu.Unlock()
	if schema := schemas[name]; schema != nil {
		return schema, nil
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(schemaLoader{})
	entries, err := assets.ReadDir("schemas")
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		b, err := assets.ReadFile("schemas/" + entry.Name())
		if err != nil {
			return nil, err
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		id, ok := doc.(map[string]any)["$id"].(string)
		if !ok {
			return nil, fmt.Errorf("Embedded schema lacks an ID")
		}
		if err := compiler.AddResource(id, doc); err != nil {
			return nil, err
		}
		if err := compiler.AddResource("https://volley.invalid/schema/1/"+entry.Name(), doc); err != nil {
			return nil, err
		}
	}
	schema, err := compiler.Compile("https://volley.invalid/schema/1/" + name)
	if err != nil {
		return nil, err
	}
	if schemas == nil {
		schemas = make(map[string]*jsonschema.Schema)
	}
	schemas[name] = schema
	return schema, nil
}

// Compilation is lazy and guarded; no fallible package initializer runs before
// entry recovery. Compiled schemas are immutable during concurrent validation.
func Validate(name string, value any) error {
	schema, err := compiledSchema(name)
	if err != nil {
		return err
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return err
	}
	return schema.Validate(doc)
}

type schemaLoader struct{}

func (schemaLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("Unembedded schema resource %q", url)
}
