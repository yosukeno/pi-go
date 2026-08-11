package tools

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestGenerateSchemaEquivalentHandwritten verifies that the reflection-based
// schema generator produces equivalent schemas to the hand-written ones.
func TestGenerateSchemaEquivalentHandwritten(t *testing.T) {
	tests := []struct {
		name string
		tool Tool
	}{
		{"ls", &Ls{}},
		{"bash", &Bash{}},
		{"read", &Read{}},
		{"write", &Write{}},
		{"find", &Find{}},
		{"grep", &Grep{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Get the hand-written schema
			handwritten := tt.tool.InputSchema()

			// Get the reflection-based schema
			var reflection map[string]any
			if sp, ok := tt.tool.(SchemaProvider); ok {
				reflection = sp.Schema()
			} else {
				t.Fatalf("tool %s does not implement SchemaProvider", tt.name)
			}

			// Compare schemas
			if !schemasEqual(handwritten, reflection) {
				// Marshal both for nice diff output
				h, _ := json.MarshalIndent(handwritten, "", "  ")
				r, _ := json.MarshalIndent(reflection, "", "  ")
				t.Errorf("schemas differ:\nhandwritten:\n%s\n\nreflection:\n%s", h, r)
			}
		})
	}
}

// schemasEqual compares two JSON schemas for semantic equality.
// This handles cases where order doesn't matter (like required arrays).
func schemasEqual(a, b map[string]any) bool {
	// Check type
	if a["type"] != b["type"] {
		return false
	}

	// Check required fields - they should be the same regardless of order
	_, aHasReq := a["required"]
	_, bHasReq := b["required"]
	if aHasReq != bHasReq {
		return false
	}
	if aHasReq {
		if !stringSlicesEqual(requiredNames(a), requiredNames(b)) {
			return false
		}
	}

	// Check properties
	aProps, aOk := schemaProps(a)
	bProps, bOk := schemaProps(b)
	if aOk != bOk || len(aProps) != len(bProps) {
		return false
	}

	for key, aProp := range aProps {
		bProp, ok := bProps[key]
		if !ok {
			return false
		}

		aPropMap, aOk := aProp.(map[string]any)
		bPropMap, bOk := bProp.(map[string]any)

		if !aOk || !bOk {
			// Not maps, compare directly
			if aProp != bProp {
				return false
			}
			continue
		}

		// Compare property maps, but ignore description differences
		// (reflection adds descriptions from tags, hand-written may differ slightly)
		if aPropMap["type"] != bPropMap["type"] {
			return false
		}

		// If both have items, compare those
		aItems, aHasItems := aPropMap["items"]
		bItems, bHasItems := bPropMap["items"]
		if aHasItems != bHasItems {
			return false
		}
		if aHasItems {
			aItemsMap, aOk := aItems.(map[string]any)
			bItemsMap, bOk := bItems.(map[string]any)
			if !aOk || !bOk {
				return false
			}

			// Recursively compare items schemas
			if !schemasEqual(aItemsMap, bItemsMap) {
				return false
			}
		}
	}

	return true
}

// schemaProps normalizes the properties entry of a schema: hand-written
// schemas for the content-bearing tools use orderedProps (wire order matters,
// see there), while everything else uses plain maps. Semantic comparison
// ignores order, so both become a map here.
func schemaProps(s map[string]any) (map[string]any, bool) {
	switch p := s["properties"].(type) {
	case map[string]any:
		return p, true
	case orderedProps:
		m := make(map[string]any, len(p))
		for _, pair := range p {
			m[pair.name] = pair.spec
		}
		return m, true
	}
	return nil, false
}

// stringSlicesEqual compares two string slices regardless of order.
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aMap := make(map[string]bool)
	for _, s := range a {
		aMap[s] = true
	}
	for _, s := range b {
		if !aMap[s] {
			return false
		}
	}
	return true
}

// TestGenerateSchemaRequiredFields verifies that the reflection generator
// correctly identifies required fields.
func TestGenerateSchemaRequiredFields(t *testing.T) {
	type testStruct struct {
		RequiredField string `json:"requiredField" required:"true"`
		OptionalField string `json:"optionalField,omitempty"`
		ExplicitFalse string `json:"explicitFalse" required:"false"`
		NoTag         string `json:"noTag"` // required by default
	}

	schema := GenerateSchema(reflect.TypeOf(testStruct{}))
	required := requiredNames(schema)

	expectedRequired := []string{"requiredField", "noTag"}
	if !stringSlicesEqual(required, expectedRequired) {
		t.Errorf("got required fields %v, want %v", required, expectedRequired)
	}

	// Verify optional fields are not in required
	for _, name := range []string{"optionalField", "explicitFalse"} {
		for _, req := range required {
			if req == name {
				t.Errorf("field %s should not be required", name)
			}
		}
	}
}

// TestGenerateSchemaRequiredNeverNil verifies that GenerateSchema never returns a
// nil required array, which marshals to JSON null and makes moonshot/kimi reject
// every request rather than just the offending tool.
func TestGenerateSchemaRequiredNeverNil(t *testing.T) {
	type allOptional struct {
		A string `json:"a,omitempty"`
		B string `json:"b,omitempty"`
	}

	schema := GenerateSchema(reflect.TypeOf(allOptional{}))

	// Marshal to JSON and verify required is an array, not null
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}

	var parsed struct {
		Required json.RawMessage `json:"required"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}

	// required should be "[]", not "null"
	if string(parsed.Required) == "null" {
		t.Error("required is null, should be an empty array")
	}

	// Verify it's a valid empty array
	var required []string
	if err := json.Unmarshal(parsed.Required, &required); err != nil {
		t.Errorf("required is not a valid array: %v", err)
	}
	if len(required) != 0 {
		t.Errorf("expected empty array, got %d items", len(required))
	}
}

// TestGenerateSchemaHandlesComplexTypes verifies type mapping.
func TestGenerateSchemaHandlesComplexTypes(t *testing.T) {
	type complexStruct struct {
		StringField string   `json:"stringField"`
		IntField    int      `json:"intField"`
		FloatField  float64  `json:"floatField"`
		BoolField   bool     `json:"boolField"`
		SliceField  []string `json:"sliceField"`
	}

	schema := GenerateSchema(reflect.TypeOf(complexStruct{}))
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("properties missing or not a map")
	}

	tests := []struct {
		field string
		want  string
	}{
		{"stringField", "string"},
		{"intField", "number"},
		{"floatField", "number"},
		{"boolField", "boolean"},
		{"sliceField", "array"},
	}

	for _, tt := range tests {
		prop, ok := props[tt.field].(map[string]any)
		if !ok {
			t.Errorf("field %s missing or not a map", tt.field)
			continue
		}
		got, _ := prop["type"].(string)
		if got != tt.want {
			t.Errorf("field %s: got type %q, want %q", tt.field, got, tt.want)
		}
	}
}

// TestGenerateSchemaHandlesDescriptions verifies that description tags
// are included in the generated schema.
func TestGenerateSchemaHandlesDescriptions(t *testing.T) {
	type describedStruct struct {
		Field string `json:"field" description:"This is a description"`
	}

	schema := GenerateSchema(reflect.TypeOf(describedStruct{}))
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("properties missing or not a map")
	}

	prop, ok := props["field"].(map[string]any)
	if !ok {
		t.Fatal("field property missing or not a map")
	}

	desc, ok := prop["description"].(string)
	if !ok {
		t.Error("description missing or not a string")
	} else if desc != "This is a description" {
		t.Errorf("got description %q, want %q", desc, "This is a description")
	}
}
