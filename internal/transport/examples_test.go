package transport

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func TestContractJSONExamplesMatchGeneratedSchemas(t *testing.T) {
	artifacts, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	contract, err := os.ReadFile("../../shape/contract.md")
	if err != nil {
		t.Fatal(err)
	}
	blocks := regexp.MustCompile("(?s)```json\\n(.*?)```").FindAllSubmatch(contract, -1)
	if len(blocks) < 9 {
		t.Fatalf("contract JSON blocks = %d, want at least 9", len(blocks))
	}

	checks := []struct {
		name   string
		block  int
		schema json.RawMessage
	}{
		{name: "operation", block: 0, schema: artifacts.Schemas.Operation},
		{name: "envelope", block: 1, schema: artifacts.Schemas.Envelope},
		{name: "route template", block: 2, schema: artifacts.Schemas.RouteTemplate},
		{name: "channel", block: 3, schema: artifacts.Schemas.Channel},
		{name: "item", block: 5, schema: artifacts.Schemas.Item},
		{name: "observation", block: 6, schema: artifacts.Schemas.Observation},
		{name: "coverage", block: 7, schema: artifacts.Schemas.Coverage},
		{name: "error", block: 8, schema: artifacts.Schemas.Error},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			var schema jsonschema.Schema
			if err := json.Unmarshal(check.schema, &schema); err != nil {
				t.Fatal(err)
			}
			resolved, err := schema.Resolve(nil)
			if err != nil {
				t.Fatal(err)
			}
			var example any
			if err := json.Unmarshal(blocks[check.block][1], &example); err != nil {
				t.Fatal(err)
			}
			if err := resolved.Validate(&example); err != nil {
				t.Fatalf("contract example does not match generated schema: %v", err)
			}
		})
	}
}
