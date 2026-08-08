package conductor

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestNativeToolSchemaTranslatesArrayShorthand is the defect this file exists
// for. gitCommit and gitDiff both declare a "string[]" argument; passing that
// spelling through produced "type":"string[]", which is not a JSON Schema type,
// and the provider rejected the entire request — so no tool definition reached
// it and every growth silently fell back to the prose protocol.
//
// Mutation target: restore `prop["type"] = arg.Type` for the array case → the
// literal reappears and this test fails.
func TestNativeToolSchemaTranslatesArrayShorthand(t *testing.T) {
	prop := jsonSchemaProperty(ToolArgument{Name: "paths", Type: "string[]", Description: "Paths to stage."})

	if got := prop["type"]; got != "array" {
		t.Fatalf(`type = %v, want "array" (a JSON Schema list is not spelled "string[]")`, got)
	}
	items, ok := prop["items"].(map[string]any)
	if !ok {
		t.Fatalf("items = %#v, want a schema object; an array without items does not describe its element type", prop["items"])
	}
	if got := items["type"]; got != "string" {
		t.Errorf(`items.type = %v, want "string"`, got)
	}
	if prop["description"] != "Paths to stage." {
		t.Errorf("description was lost during translation: %#v", prop["description"])
	}
}

// TestNativeToolSchemaKeepsScalarTypes pins that translation did not disturb the
// types that were already valid.
//
// Mutation target: drop a case from the switch → that type loses its constraint
// and this test fails.
func TestNativeToolSchemaKeepsScalarTypes(t *testing.T) {
	for _, scalar := range []string{"string", "number", "integer", "boolean", "object"} {
		prop := jsonSchemaProperty(ToolArgument{Name: "a", Type: scalar})
		if got := prop["type"]; got != scalar {
			t.Errorf("type for %q = %v, want %q", scalar, got, scalar)
		}
		if _, hasItems := prop["items"]; hasItems {
			t.Errorf("scalar %q gained an items key", scalar)
		}
	}
}

// TestNativeToolSchemaOmitsUnknownTypeRatherThanEmittingIt pins the property
// that keeps a future unknown spelling from repeating this outage. A property
// with no type is valid and permissive; a property with an invented type
// invalidates the whole request and disables tool calling for every tool.
//
// Mutation target: add a default arm setting prop["type"] = arg.Type → the
// unknown spelling is emitted again and this test fails.
func TestNativeToolSchemaOmitsUnknownTypeRatherThanEmittingIt(t *testing.T) {
	prop := jsonSchemaProperty(ToolArgument{Name: "x", Type: "tuple<int,int>", Description: "kept"})

	if got, present := prop["type"]; present {
		t.Fatalf("type = %v for an unrecognised spelling; emitting it invalidates every tool in the request", got)
	}
	if prop["description"] != "kept" {
		t.Errorf("description dropped alongside the unknown type: %#v", prop["description"])
	}
}

// TestNativeToolSchemaProducesValidSchemaForEveryTool walks the mapper rather
// than the property helper, because the provider validates the assembled
// document. Any type it cannot name is a rejected request.
//
// Mutation target: reinstate the verbatim copy in mapToolsToNative → gitCommit
// carries "string[]" and this test fails.
func TestNativeToolSchemaProducesValidSchemaForEveryTool(t *testing.T) {
	valid := map[string]bool{
		"null": true, "boolean": true, "object": true, "array": true,
		"number": true, "string": true, "integer": true,
	}

	tools := []ToolDefinition{
		{Name: "gitCommit", Arguments: []ToolArgument{
			{Name: "message", Type: "string", Required: true},
			{Name: "paths", Type: "string[]"},
		}},
		{Name: "listAvailableTools"},
	}

	for _, mapped := range mapToolsToNative(tools) {
		parameters, ok := mapped.Function.Parameters.(map[string]any)
		if !ok {
			t.Fatalf("%s: parameters are %T, want a schema object", mapped.Function.Name, mapped.Function.Parameters)
		}
		properties, ok := parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s: properties are %T, want an object (null properties is invalid draft 2020-12)", mapped.Function.Name, parameters["properties"])
		}
		for name, raw := range properties {
			prop := raw.(map[string]any)
			declared, present := prop["type"]
			if !present {
				continue // no constraint is valid, and is the unknown-type path
			}
			if !valid[declared.(string)] {
				t.Errorf("%s.%s declares type %q, which is not a JSON Schema type", mapped.Function.Name, name, declared)
			}
		}
	}
}

// TestNativeToolSchemaCoversSproutVocabulary reads the reference sprout runtime
// and asserts every argument type it declares is one the mapper translates. A
// per-type unit test cannot notice a new spelling added to the runtime later,
// which is exactly how "string[]" arrived and stayed.
//
// The floor on matches matters as much as the assertion: if the catalogue is
// restructured so this pattern stops matching, the test must fail rather than
// quietly examine nothing.
func TestNativeToolSchemaCoversSproutVocabulary(t *testing.T) {
	const referenceRuntime = "../../../../sprouts/go/main.go"

	source, err := os.ReadFile(referenceRuntime)
	if err != nil {
		t.Fatalf("read reference sprout runtime: %v", err)
	}
	catalogue := source
	if start := strings.Index(string(source), "func availableTools()"); start >= 0 {
		catalogue = source[start:]
	} else {
		t.Fatal("availableTools() not found; the catalogue moved and this test is scanning the wrong thing")
	}

	declared := regexp.MustCompile(`Type:\s*"([^"]+)"`).FindAllStringSubmatch(string(catalogue), -1)
	if len(declared) < 10 {
		t.Fatalf("found only %d declared argument types; expected the full catalogue, so the pattern has stopped matching", len(declared))
	}

	for _, match := range declared {
		spelling := match[1]
		prop := jsonSchemaProperty(ToolArgument{Name: "a", Type: spelling})
		if _, present := prop["type"]; !present {
			t.Errorf("the sprout runtime declares argument type %q, which the mapper does not translate; it would be sent with no type constraint", spelling)
		}
	}
}
