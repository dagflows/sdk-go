package authoring

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/dagflows/sdk-go/runtime"
	"github.com/stretchr/testify/require"
)

// Tests for type reflection against the cross-SDK schema fixtures.
//
// The fixtures in sdk-contract/fixtures/schema were generated from the Python
// types, so the equivalent Go types must reflect to the same JSON, except where
// CONTRACT.md's table records that Go has no such type.

const schemaFixtures = "../../sdk-contract/fixtures/schema"

// The fixture types, as Go declares them.
type (
	Order struct {
		ID     int64  `json:"id"`
		Amount int64  `json:"amount"`
		Note   string `json:"note,omitempty"`
	}
	Orders struct {
		Orders []Order    `json:"orders"`
		Placed *time.Time `json:"placed,omitempty"`
	}
	colour string
	level  string
	// everything is Python's Everything without the fields Go cannot
	// declare: day (no date type) and either (no union).
	everything struct {
		Flag     bool               `json:"flag"`
		Count    int64              `json:"count"`
		Ratio    float64            `json:"ratio"`
		Name     string             `json:"name"`
		Blob     []byte             `json:"blob"`
		When     time.Time          `json:"when"`
		Price    runtime.Decimal    `json:"price"`
		Colour   colour             `json:"colour"`
		Level    level              `json:"level"`
		Tags     []string           `json:"tags"`
		Scores   map[string]float64 `json:"scores"`
		Anything any                `json:"anything"`
		Maybe    *string            `json:"maybe"`
		Orders   []Order            `json:"orders"`
		Bag      map[string]any     `json:"bag"`
		Items    []any              `json:"items"`
	}
)

// A named string type declares its values; Python's Enum carries its name
// as the title, a Literal does not.
func (colour) DagflowsSchema() Schema { return Enum("Colour", "red", "blue") }
func (level) DagflowsSchema() Schema {
	return Schema{{"type", "string"}, {"enum", []any{"low", "high"}}}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(schemaFixtures, name+".json"))
	if err != nil {
		t.Skipf("contract fixture %s not on disk", name)
	}

	return raw
}

func compactJSON(t *testing.T, raw []byte) string {
	t.Helper()

	var out bytes.Buffer
	require.NoError(t, json.Compact(&out, raw))

	return out.String()
}

func marshalled(t *testing.T, value any) string {
	t.Helper()

	raw, err := json.Marshal(value)
	require.NoError(t, err)

	return string(raw)
}

// Tests that the reflected schemas match the contract's fixtures byte for byte,
// key order included. Each fixture is compacted and compared as text, so a
// reordered property is a failure rather than a passing map comparison.
func TestTheGoTypesReflectToTheContractsSchemas(t *testing.T) {
	cases := map[string]func() (any, error){
		"order":  func() (any, error) { return SchemaOf(reflect.TypeFor[Order]()) },
		"orders": func() (any, error) { return SchemaOf(reflect.TypeFor[Orders]()) },
		"output-value-orders": func() (any, error) {
			return OutputSpec(reflect.TypeFor[Orders]())
		},
		"output-rows-order": func() (any, error) {
			return OutputSpec(reflect.TypeFor[runtime.Rows[Order]]())
		},
		"output-bytes": func() (any, error) { return OutputSpec(reflect.TypeFor[[]byte]()) },
		// Result[T] unwraps to T before the spec is taken (NodeResult).
		"output-result-orders": func() (any, error) {
			return OutputSpec(reflect.TypeFor[Orders]())
		},
		"output-any":         func() (any, error) { return OutputSpec(nil) },
		"input-value-orders": func() (any, error) { return InputSpec(reflect.TypeFor[Orders]()) },
		"input-handle-orders": func() (any, error) {
			return InputSpec(reflect.TypeFor[*runtime.Input[Orders]]())
		},
		"input-rows-order": func() (any, error) {
			return InputSpec(reflect.TypeFor[runtime.Rows[Order]]())
		},
		"input-bytes": func() (any, error) { return InputSpec(reflect.TypeFor[[]byte]()) },
	}

	for name, produce := range cases {
		t.Run(name, func(t *testing.T) {
			expected := compactJSON(t, fixture(t, name))

			value, err := produce()
			require.NoError(t, err)
			require.Equal(t, expected, marshalled(t, value))
		})
	}
}

// Tests that the whole profile reflects as Python's Everything does, minus the
// fields Go cannot express: a date, a union, and a field that is required yet
// nullable (a Go pointer is optional and nullable in one).
func TestEverythingReflectsAsPythonsExceptWhereGoHasNoSuchType(t *testing.T) {
	var expected map[string]any
	require.NoError(t, json.Unmarshal(fixture(t, "everything"), &expected))

	properties := expected["properties"].(map[string]any)
	delete(properties, "day")
	delete(properties, "either")

	var required []any
	for _, name := range expected["required"].([]any) {
		if name != "day" && name != "either" && name != "maybe" {
			required = append(required, name)
		}
	}
	expected["required"] = required
	expected["title"] = "everything"

	schema, err := SchemaOf(reflect.TypeFor[everything]())
	require.NoError(t, err)

	var actual map[string]any
	require.NoError(t, json.Unmarshal([]byte(marshalled(t, schema)), &actual))
	require.Equal(t, expected, actual)

	// Declaration order survives, which a map compare cannot see.
	want := propertyOrder(t, fixture(t, "everything"))
	want = slices.DeleteFunc(want, func(name string) bool { return name == "day" || name == "either" })
	require.Equal(t, want, propertyOrder(t, []byte(marshalled(t, schema))))
}

// propertyOrder returns the top-level property names in the order they were written.
func propertyOrder(t *testing.T, raw []byte) []string {
	t.Helper()

	decoder := json.NewDecoder(bytes.NewReader(raw))
	depth := 0
	inProperties := false

	var names []string

	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}

		switch v := token.(type) {
		case json.Delim:
			switch v {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if inProperties && depth == 1 {
					return names
				}
			}
		case string:
			if depth == 1 && v == "properties" && !inProperties {
				inProperties = true
				continue
			}
			if inProperties && depth == 2 {
				names = append(names, v)
				// Skip the value.
				var skipped any
				require.NoError(t, decoder.Decode(&skipped))
			}
		}
	}

	return names
}

func TestATypeMayDescribeItselfThroughAPointerReceiver(t *testing.T) {
	schema, err := SchemaOf(reflect.TypeFor[pointerSchemer]())
	require.NoError(t, err)
	require.Equal(t, `{"type":"string","format":"uuid"}`, marshalled(t, schema))

	schema, err = SchemaOf(reflect.TypeFor[*pointerSchemer]())
	require.NoError(t, err)
	require.Equal(t, `{"type":["string","null"],"format":"uuid"}`, marshalled(t, schema))
}

type pointerSchemer struct{ id string }

func (*pointerSchemer) DagflowsSchema() Schema {
	return Schema{{"type", "string"}, {"format", "uuid"}}
}

func TestEmbeddedStructsFlattenAsEncodingJSONDoes(t *testing.T) {
	type base struct {
		ID int64 `json:"id"`
	}
	type named struct {
		base
		Name   string `json:"name"`
		hidden int
		Skip   string `json:"-"`
		Alias  string
	}

	schema, err := SchemaOf(reflect.TypeFor[named]())
	require.NoError(t, err)
	require.Equal(t,
		`{"type":"object","title":"named","properties":{"id":{"type":"integer","format":"int64"},"name":{"type":"string"},"Alias":{"type":"string"}},"required":["id","name","Alias"]}`,
		marshalled(t, schema))
}

func TestTheSmallerIntegersAreInt32AndTheRestInt64(t *testing.T) {
	type sizes struct {
		A int8   `json:"a"`
		B int16  `json:"b"`
		C int32  `json:"c"`
		D int    `json:"d"`
		E uint8  `json:"e"`
		F uint64 `json:"f"`
		G uint   `json:"g"`
	}

	schema, err := SchemaOf(reflect.TypeFor[sizes]())
	require.NoError(t, err)

	raw := marshalled(t, schema)
	for _, small := range []string{"a", "b", "c", "e"} {
		require.Contains(t, raw, `"`+small+`":{"type":"integer","format":"int32"}`)
	}
	for _, wide := range []string{"d", "f", "g"} {
		require.Contains(t, raw, `"`+wide+`":{"type":"integer","format":"int64"}`)
	}
}

func TestJSONNumberAndRawMessageHaveTheirOwnShapes(t *testing.T) {
	number, err := SchemaOf(reflect.TypeFor[json.Number]())
	require.NoError(t, err)
	require.Equal(t, `{"type":"number"}`, marshalled(t, number))

	raw, err := SchemaOf(reflect.TypeFor[json.RawMessage]())
	require.NoError(t, err)
	require.Equal(t, `{}`, marshalled(t, raw))
}
