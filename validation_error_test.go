package jsonschema_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pb33f/jsonschema/v6"
	"github.com/pb33f/jsonschema/v6/kind"
)

func compileRegressionSchema(t testing.TB, source string) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(source))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.AssertContent()
	if err := c.AddResource("https://example.com/schema.json", doc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile("https://example.com/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

var locationCases = []struct {
	name    string
	schema  string
	valid   any
	invalid any
}{
	{
		name:    "propertyNames",
		schema:  `{"propertyNames":{"pattern":"^[a-z]+$"}}`,
		valid:   map[string]any{"good": nil},
		invalid: map[string]any{"BAD": nil},
	},
	{
		name:    "contentSchema",
		schema:  `{"contentMediaType":"application/json","contentSchema":{"type":"integer"}}`,
		valid:   `1`,
		invalid: `"bad"`,
	},
}

func nestedArraySchema(item string) string {
	for i := 0; i < 4; i++ {
		item = `{"items":` + item + `}`
	}
	return item
}

func nestedArray(items ...any) any {
	return []any{[]any{[]any{items}}}
}

func wrapperKeyword(k jsonschema.ErrorKind) string {
	switch k.(type) {
	case *kind.PropertyNames:
		return "propertyNames"
	case *kind.ContentSchema:
		return "contentSchema"
	}
	return ""
}

func TestValidationErrorInstanceLocations(t *testing.T) {
	for _, tt := range locationCases {
		t.Run(tt.name, func(t *testing.T) {
			sch := compileRegressionSchema(t, nestedArraySchema(tt.schema))
			if err := sch.Validate(nestedArray(tt.valid, tt.valid, tt.valid)); err != nil {
				t.Fatalf("valid siblings: %v", err)
			}
			// At this depth the location slice has spare capacity. Visiting the
			// next array element must not change an earlier error's location.
			err := sch.Validate(nestedArray(tt.invalid, tt.valid, tt.invalid))
			verr, ok := err.(*jsonschema.ValidationError)
			if !ok {
				t.Fatalf("got %T, want *ValidationError", err)
			}
			want := []string{"/0/0/0/0", "/0/0/0/2"}
			var got []string
			var visit func(*jsonschema.ValidationError)
			visit = func(e *jsonschema.ValidationError) {
				if wrapperKeyword(e.ErrorKind) == tt.name {
					got = append(got, "/"+strings.Join(e.InstanceLocation, "/"))
				}
				for _, cause := range e.Causes {
					visit(cause)
				}
			}
			visit(verr)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("stored locations = %v, want %v", got, want)
			}
			for _, output := range []struct {
				name string
				unit *jsonschema.OutputUnit
			}{
				{"basic", verr.BasicOutput()},
				{"detailed", verr.DetailedOutput()},
			} {
				var locations []string
				var walk func(*jsonschema.OutputUnit)
				walk = func(unit *jsonschema.OutputUnit) {
					if strings.HasSuffix(unit.KeywordLocation, "/"+tt.name) {
						locations = append(locations, unit.InstanceLocation)
					}
					for i := range unit.Errors {
						walk(&unit.Errors[i])
					}
				}
				walk(output.unit)
				if !reflect.DeepEqual(locations, want) {
					t.Errorf("%s locations = %v, want %v", output.name, locations, want)
				}
			}
		})
	}
}

func TestBasicOutputReferencedPropertyNames(t *testing.T) {
	for _, count := range []int{1, 2} {
		for _, references := range []int{0, 1, 2} {
			t.Run(fmt.Sprintf("properties=%d/references=%d", count, references), func(t *testing.T) {
				item := `{"propertyNames":{"pattern":"^[a-z]+$"}}`
				defs := `"names":` + item
				if references > 0 {
					item = `{"$ref":"#/$defs/names"}`
				}
				if references > 1 {
					defs += `,"alias":` + item
					item = `{"$ref":"#/$defs/alias"}`
				}
				sch := compileRegressionSchema(t, `{"$defs":{`+defs+`},"items":`+item+`}`)
				obj := make(map[string]any, count)
				for i := 0; i < count; i++ {
					obj[fmt.Sprintf("BAD%d", i)] = nil
				}
				err := sch.Validate([]any{obj})
				verr, ok := err.(*jsonschema.ValidationError)
				if !ok {
					t.Fatalf("got %T, want *ValidationError", err)
				}
				expectedKinds := make(map[jsonschema.ErrorKind]bool)
				var collectErrors func(*jsonschema.ValidationError)
				collectErrors = func(e *jsonschema.ValidationError) {
					expectedKinds[e.ErrorKind] = true
					for _, cause := range e.Causes {
						collectErrors(cause)
					}
				}
				collectErrors(verr)
				out := verr.BasicOutput()
				names, patterns := 0, 0
				for _, unit := range out.Errors {
					if len(unit.Errors) != 0 || unit.Error == nil {
						t.Fatalf("basic output must contain flat error units: %+v", unit)
					}
					if !expectedKinds[unit.Error.Kind] {
						t.Errorf("output kind %T does not retain its validation error's identity", unit.Error.Kind)
					}
					switch unit.Error.Kind.(type) {
					case *kind.PropertyNames:
						names++
						if unit.InstanceLocation != "/0" {
							t.Errorf("propertyNames location = %q, want /0", unit.InstanceLocation)
						}
					case *kind.Pattern:
						patterns++
					}
					if references > 0 && unit.AbsoluteKeywordLocation == "" {
						t.Errorf("referenced error has no absolute keyword location: %+v", unit)
					}
				}
				if names != count || patterns != count {
					t.Errorf("error kinds: PropertyNames=%d, Pattern=%d; want %d each", names, patterns, count)
				}
				// Detailed output retains the constraint hierarchy and its leaves.
				detailed := verr.DetailedOutput()
				leaves := 0
				var walk func(*jsonschema.OutputUnit)
				walk = func(unit *jsonschema.OutputUnit) {
					if unit.Error != nil {

						if _, ok := unit.Error.Kind.(*kind.Pattern); ok {
							leaves++
						}
					}
					for i := range unit.Errors {
						walk(&unit.Errors[i])
					}
				}
				walk(detailed)
				if leaves != count {
					t.Errorf("detailed Pattern leaves = %d, want %d", leaves, count)
				}
			})
		}
	}
}

func BenchmarkValidationErrorInstanceLocations(b *testing.B) {
	for _, tt := range locationCases {
		b.Run(tt.name, func(b *testing.B) {
			sch := compileRegressionSchema(b, nestedArraySchema(tt.schema))
			for _, valid := range []bool{true, false} {
				b.Run(fmt.Sprintf("valid=%t", valid), func(b *testing.B) {
					value := nestedArray(tt.valid, tt.valid, tt.valid)
					if !valid {
						value = nestedArray(tt.invalid, tt.valid, tt.invalid)
					}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						if err := sch.Validate(value); (err == nil) != valid {
							b.Fatalf("unexpected validation result: %v", err)
						}
					}
				})
			}
		})
	}
}
