package schema

import (
	"context"
	"reflect"
	"regexp"
	"testing"

	"github.com/sagernet/sing/common/json/badoption"
)

func TestGenerateBadOptionRegexp(t *testing.T) {
	type options struct {
		Pattern *badoption.Regexp `json:"pattern,omitempty"`
	}
	content, err := Generate(context.Background(), reflect.TypeFor[options]())
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`"pattern"\s*:\s*\{[^}]*"type"\s*:\s*"string"`).Match(content) {
		t.Fatalf("regexp schema is not a string: %s", content)
	}
}
