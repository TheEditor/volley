package contract

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Validate checks embedded draft 2020-12 schemas. Resource loading is restricted
// to the embedded schema set; validation never fetches a network resource.
func Validate(name string, value any) error {
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(schemaLoader{})
	entries, err := assets.ReadDir("schemas")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		b, err := assets.ReadFile("schemas/" + entry.Name())
		if err != nil {
			return err
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			return err
		}
		id, ok := doc.(map[string]any)["$id"].(string)
		if !ok {
			return fmt.Errorf("Embedded schema lacks an ID")
		}
		if err := compiler.AddResource(id, doc); err != nil {
			return err
		}
	}
	schema, err := compiler.Compile("https://volley.invalid/schema/1/" + name)
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
