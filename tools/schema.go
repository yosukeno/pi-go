package tools

import (
	"fmt"
	"reflect"
	"strings"
)

// GenerateSchema builds a JSON Schema from a Go struct type using reflection.
// This eliminates hand-written schema errors of the kind object guards against,
// where a nil required list marshals to JSON null instead of an empty array and
// moonshot/kimi rejects the whole request.
func GenerateSchema(typ reflect.Type) map[string]any {
	if typ.Kind() != reflect.Struct {
		panic(fmt.Sprintf("GenerateSchema requires a struct type, got %s", typ.Kind()))
	}

	props := make(map[string]any)
	required := make([]string, 0)

	for i := range typ.NumField() {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}

		// Get the JSON tag to determine the field name
		jsonTag := field.Tag.Get("json")
		if jsonTag == "" || jsonTag == "-" {
			continue
		}

		// Parse the JSON tag (e.g. "path,omitempty" -> "path", ["omitempty"])
		parts := strings.Split(jsonTag, ",")
		name := parts[0]
		if name == "" {
			continue
		}

		// Build the property schema
		propSchema := map[string]any{
			"type": jsonTypeFromGo(field.Type),
		}

		// An array says nothing about what it holds without items, and a model
		// filling an under-specified array has to guess. Emitted here so the
		// reflected schema stays comparable to the hand-written one — see
		// TestGenerateSchemaEquivalentHandwritten, which treats a mismatch in
		// items as a difference.
		if k := field.Type.Kind(); k == reflect.Slice || k == reflect.Array {
			propSchema["items"] = itemSchema(field.Type.Elem())
		}

		// Add description if available
		if desc := field.Tag.Get("description"); desc != "" {
			propSchema["description"] = desc
		}

		// A closed set of allowed values. Worth expressing in the schema rather
		// than only in prose: a model that picks an invalid value is told so by the
		// provider before the call is made, instead of by us a turn later.
		if enum := field.Tag.Get("enum"); enum != "" {
			propSchema["enum"] = strings.Split(enum, ",")
		}

		props[name] = propSchema

		// Check if this field is required
		// A field is required if:
		// 1. It doesn't have omitempty tag
		// 2. It doesn't have required:"false" tag
		// 3. For pointer types, they're optional by default unless required:"true"
		isRequired := true
		for _, tag := range parts[1:] {
			if tag == "omitempty" {
				isRequired = false
				break
			}
		}

		// Check explicit required tag
		reqTag := field.Tag.Get("required")
		if reqTag == "false" {
			isRequired = false
		} else if reqTag == "true" {
			isRequired = true
		}

		// Pointer types are optional by default
		if field.Type.Kind() == reflect.Ptr && reqTag != "true" {
			isRequired = false
		}

		if isRequired {
			required = append(required, name)
		}
	}

	// Ensure required is never nil; see object for what a JSON null does here.
	if required == nil {
		required = []string{}
	}

	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

// itemSchema describes an array's element. A struct element recurses into the full
// object schema; anything else is named by its JSON type.
func itemSchema(elem reflect.Type) map[string]any {
	if elem.Kind() == reflect.Ptr {
		elem = elem.Elem()
	}
	if elem.Kind() == reflect.Struct {
		return GenerateSchema(elem)
	}
	return map[string]any{"type": jsonTypeFromGo(elem)}
}

// jsonTypeFromGo maps Go types to JSON Schema types.
func jsonTypeFromGo(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Bool:
		return "boolean"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Map, reflect.Struct, reflect.Interface:
		return "object"
	case reflect.Ptr:
		return jsonTypeFromGo(t.Elem())
	default:
		return "string" // fallback
	}
}

// GenerateArraySchema builds a JSON Schema for an array type with items schema.
func GenerateArraySchema(itemType reflect.Type, itemDesc string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": itemDesc,
		"items":       GenerateSchema(itemType),
	}
}

// SchemaFromArgs is a helper for tools to implement Schema() method.
// It takes the args struct type and returns the JSON Schema.
func SchemaFromArgs(args any) map[string]any {
	return GenerateSchema(reflect.TypeOf(args))
}
