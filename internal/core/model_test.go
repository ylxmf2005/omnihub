package core

import (
	"errors"
	"testing"
)

func TestOperationValidation(t *testing.T) {
	query := "agent search"
	target := "https://example.com/post"
	base := Operation{
		SchemaVersion:      SchemaVersion,
		Operation:          OperationSearch,
		Query:              &query,
		Scope:              Scope{Sources: []string{"github"}},
		RoutePolicy:        RoutePolicy{Mode: RouteAuto, AllowFallback: true},
		Limit:              20,
		IdentityDedupe:     IdentityExact,
		SimilarityGrouping: SimilarityOff,
		DeadlineMS:         30000,
	}

	cases := []struct {
		name      string
		operation Operation
		wantError bool
	}{
		{name: "search", operation: base},
		{name: "search without query", operation: replace(base, func(value *Operation) { value.Query = nil }), wantError: true},
		{name: "latest", operation: replace(base, func(value *Operation) { value.Operation = OperationLatest; value.Query = nil })},
		{name: "latest with query", operation: replace(base, func(value *Operation) { value.Operation = OperationLatest }), wantError: true},
		{name: "fetch", operation: replace(base, func(value *Operation) { value.Operation = OperationFetch; value.Query = nil; value.Target = &target })},
		{name: "fetch without target", operation: replace(base, func(value *Operation) { value.Operation = OperationFetch; value.Query = nil }), wantError: true},
		{name: "missing scope", operation: replace(base, func(value *Operation) { value.Scope = Scope{} }), wantError: true},
		{name: "untyped selector", operation: replace(base, func(value *Operation) { value.RoutePolicy.Prefer = []RouteSelector{{ID: "github"}} }), wantError: true},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := test.operation.Validate()
			if test.wantError && !errors.Is(err, ErrInvalidOperation) {
				t.Fatalf("Validate() error = %v, want ErrInvalidOperation", err)
			}
			if !test.wantError && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func replace(operation Operation, mutate func(*Operation)) Operation {
	mutate(&operation)
	return operation
}
