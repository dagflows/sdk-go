package authoring

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/dagflows/sdk-go/runtime"
)

// Type reflection and schema generation for manifest `io` definitions.
//
// Derives JSON Schema 2020-12 definitions using the Dagflows Data Model (DDM)
// profile directly from the Go types on a handler. The profile emits a fixed
// subset of the vocabulary: type, properties, required, items, enum, oneOf,
// additionalProperties, format for the types JSON lacks, and title for the name
// a type had in the code. Generated schemas are inlined to keep manifests
// self-contained, and a recursive type is refused rather than emitted half.

// Schema is a JSON Schema document with its keys in the order they were
// written, so a manifest is byte-stable and reads like the type it came from.
type Schema []SchemaField

// SchemaField is one key and its value in a Schema.
type SchemaField struct {
	Key   string
	Value any
}

// MarshalJSON encodes the schema as a JSON object, keeping the fields in the
// order they were written.
func (s Schema) MarshalJSON() ([]byte, error) {
	var out bytes.Buffer

	out.WriteByte('{')

	for i, f := range s {
		if i > 0 {
			out.WriteByte(',')
		}

		key, err := json.Marshal(f.Key)
		if err != nil {
			return nil, err
		}

		value, err := marshalCompact(f.Value)
		if err != nil {
			return nil, err
		}

		out.Write(key)
		out.WriteByte(':')
		out.Write(value)
	}

	out.WriteByte('}')

	return out.Bytes(), nil
}

// marshalCompact encodes a value as JSON on a single line, leaving HTML
// characters unescaped.
func marshalCompact(value any) ([]byte, error) {
	var out bytes.Buffer

	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(value); err != nil {
		return nil, err
	}

	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

// Schemer is implemented by a type that describes its own schema, which is
// how a named string type declares itself an enum, or a custom type says what
// its JSON form is.
type Schemer interface {
	DagflowsSchema() Schema
}

var (
	schemerType = reflect.TypeFor[Schemer]()
	timeType    = reflect.TypeFor[time.Time]()
	numberType  = reflect.TypeFor[json.Number]()
	rawType     = reflect.TypeFor[json.RawMessage]()
	decimalType = reflect.TypeFor[runtime.Decimal]()
	bytesType   = reflect.TypeFor[[]byte]()
)

// Port shapes a node can declare for an input or an output.
const (
	ShapeValue = "value"
	ShapeRows  = "rows"
	ShapeBytes = "bytes"
)

// PortManifest describes one side of an edge in the manifest: its shape,
// content type and schema.
type PortManifest struct {
	Shape       string              `json:"shape"`
	ContentType runtime.ContentType `json:"content_type,omitempty"`
	Schema      Schema              `json:"schema"`
}

// SchemaOf derives the profile schema for a Go type. An empty schema is "any".
func SchemaOf(t reflect.Type) (Schema, error) {
	return schemaOf(t, nil)
}

// schemaOf derives the schema for t, carrying the enclosing struct types in
// stack so a cycle can be detected.
func schemaOf(t reflect.Type, stack []reflect.Type) (Schema, error) {
	if t == nil {
		return Schema{}, nil
	}

	// A pointer is its element, nullable: decided before the element gets to
	// describe itself, so a self-describing type behind a pointer is still
	// allowed to be null.
	if t.Kind() == reflect.Pointer {
		inner, err := schemaOf(t.Elem(), stack)
		if err != nil {
			return nil, err
		}
		return nullable(inner), nil
	}

	// A type that describes itself wins, so enums and custom encodings are
	// stated once, next to the type.
	if t.Implements(schemerType) {
		return reflect.Zero(t).Interface().(Schemer).DagflowsSchema(), nil
	}
	if t.Kind() != reflect.Pointer && reflect.PointerTo(t).Implements(schemerType) {
		return reflect.New(t).Interface().(Schemer).DagflowsSchema(), nil
	}

	switch t {
	case timeType:
		return Schema{{"type", "string"}, {"format", "date-time"}}, nil
	case numberType:
		return Schema{{"type", "number"}}, nil
	case rawType:
		return Schema{}, nil
	case decimalType:
		return Schema{{"type", "string"}, {"format", "decimal"}}, nil
	case bytesType:
		return Schema{{"type", "string"}, {"format", "byte"}}, nil
	}

	switch t.Kind() {
	case reflect.Bool:
		return Schema{{"type", "boolean"}}, nil

	case reflect.Int, reflect.Int64, reflect.Uint, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return Schema{{"type", "integer"}, {"format", "int64"}}, nil

	case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Uint8, reflect.Uint16:
		return Schema{{"type", "integer"}, {"format", "int32"}}, nil

	case reflect.Float32, reflect.Float64:
		return Schema{{"type", "number"}, {"format", "double"}}, nil

	case reflect.String:
		return Schema{{"type", "string"}}, nil

	case reflect.Interface:
		return Schema{}, nil

	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return Schema{{"type", "string"}, {"format", "byte"}}, nil
		}
		items, err := schemaOf(t.Elem(), stack)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			return Schema{{"type", "array"}}, nil
		}
		return Schema{{"type", "array"}, {"items", items}}, nil

	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("%s has non-string keys, which JSON cannot carry", t)
		}
		values, err := schemaOf(t.Elem(), stack)
		if err != nil {
			return nil, err
		}
		if len(values) == 0 {
			return Schema{{"type", "object"}}, nil
		}
		return Schema{{"type", "object"}, {"additionalProperties", values}}, nil

	case reflect.Struct:
		return structSchema(t, stack)

	case reflect.Func:
		// A Rows[T] in a field position has no JSON form.
		return nil, fmt.Errorf("%s is a function, which has no JSON form", t)
	}

	return Schema{}, nil
}

// nullable widens a schema to admit null, adding "null" to a stated type or
// wrapping the schema in oneOf when the type is not a single name. An empty
// schema already admits any value and is returned as such.
func nullable(inner Schema) Schema {
	if len(inner) == 0 {
		return Schema{}
	}

	out := make(Schema, 0, len(inner))
	typed := false

	for _, f := range inner {
		if f.Key == "type" {
			if kind, ok := f.Value.(string); ok {
				out = append(out, SchemaField{"type", []string{kind, "null"}})
				typed = true
				continue
			}
		}
		out = append(out, f)
	}

	if !typed {
		return Schema{{"oneOf", []any{inner, Schema{{"type", "null"}}}}}
	}

	return out
}

// property is one struct field's contribution to an object schema.
type property struct {
	name     string
	schema   Schema
	required bool
}

// structSchema derives the object schema for a struct type, titled with the
// type's name, and refuses a type that refers to itself.
func structSchema(t reflect.Type, stack []reflect.Type) (Schema, error) {
	for _, seen := range stack {
		if seen == t {
			return nil, fmt.Errorf("%s refers to itself, and a recursive type cannot be inlined in the manifest yet; break the cycle or declare the field as any", t.Name())
		}
	}
	stack = append(stack, t)

	properties, err := structProperties(t, stack)
	if err != nil {
		return nil, err
	}

	out := Schema{{"type", "object"}}
	if t.Name() != "" {
		out = append(out, SchemaField{"title", t.Name()})
	}

	props := make(Schema, 0, len(properties))
	var required []string
	for _, p := range properties {
		props = append(props, SchemaField{p.name, p.schema})
		if p.required {
			required = append(required, p.name)
		}
	}

	out = append(out, SchemaField{"properties", props})
	if len(required) > 0 {
		out = append(out, SchemaField{"required", required})
	}

	return out, nil
}

// structProperties lists a struct's JSON properties in field order, flattening
// embedded structs the way encoding/json does.
func structProperties(t reflect.Type, stack []reflect.Type) ([]property, error) {
	var out []property

	for i := range t.NumField() {
		field := t.Field(i)
		tag := field.Tag.Get("json")

		if tag == "-" {
			continue
		}

		name, options, _ := strings.Cut(tag, ",")
		optional := strings.Contains(","+options+",", ",omitempty,") ||
			strings.Contains(","+options+",", ",omitzero,")

		if field.Anonymous && name == "" {
			inner := field.Type
			if inner.Kind() == reflect.Pointer {
				inner = inner.Elem()
			}
			if inner.Kind() == reflect.Struct {
				nested, err := structProperties(inner, stack)
				if err != nil {
					return nil, err
				}
				out = append(out, nested...)
				continue
			}
		}

		if !field.IsExported() {
			continue
		}

		if name == "" {
			name = field.Name
		}

		schema, err := schemaOf(field.Type, stack)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", t.Name(), field.Name, err)
		}

		out = append(out, property{
			name:     name,
			schema:   schema,
			required: !optional && field.Type.Kind() != reflect.Pointer,
		})
	}

	return out, nil
}

// Enum is a ready-made schema for a named string type: return it from
// DagflowsSchema to declare the values the type may take.
func Enum(title string, values ...string) Schema {
	list := make([]any, len(values))
	for i, v := range values {
		list[i] = v
	}
	return Schema{{"type", "string"}, {"enum", list}, {"title", title}}
}

var (
	writtenType    = reflect.TypeFor[runtime.Written]()
	writtenPtrType = reflect.TypeFor[*runtime.Written]()
)

// OutputSpec derives the io.output block for a handler's Out type.
func OutputSpec(t reflect.Type) (PortManifest, error) {
	// A Written is whatever the handler streamed through ctx.Out, whose
	// content type it chose at run time: the declaration says "anything".
	if t == nil || t == writtenType || t == writtenPtrType {
		return PortManifest{Shape: ShapeValue, ContentType: runtime.JSON, Schema: Schema{}}, nil
	}

	switch {
	case isRows(t):
		schema, err := SchemaOf(rowsElem(t))
		if err != nil {
			return PortManifest{}, err
		}
		return PortManifest{Shape: ShapeRows, ContentType: runtime.NDJSON, Schema: schema}, nil

	case t == bytesType:
		return PortManifest{Shape: ShapeBytes, ContentType: runtime.BYTES, Schema: Schema{{"type", "string"}, {"format", "byte"}}}, nil
	}

	schema, err := SchemaOf(t)
	if err != nil {
		return PortManifest{}, err
	}

	return PortManifest{Shape: ShapeValue, ContentType: runtime.JSON, Schema: schema}, nil
}

// InputSpec derives the io.inputs port for what a handler expects of its one
// parent, or nil when the type states nothing (any, the bag, None, an untyped
// handle).
func InputSpec(t reflect.Type) (*PortManifest, error) {
	if t == nil || t.Kind() == reflect.Interface {
		return nil, nil
	}

	if t == reflect.TypeFor[*runtime.Inputs]() || t == reflect.TypeFor[runtime.None]() {
		return nil, nil
	}

	// *Input[E]: the expectation is E.
	if t.Kind() == reflect.Pointer && t.Elem().Kind() == reflect.Struct && strings.HasPrefix(t.Elem().Name(), "Input[") {
		method := reflect.New(t.Elem()).MethodByName("ValueType")
		if method.IsValid() {
			elem := method.Call(nil)[0].Interface().(reflect.Type)
			return InputSpec(elem)
		}
	}

	switch {
	case isRows(t):
		schema, err := SchemaOf(rowsElem(t))
		if err != nil {
			return nil, err
		}
		return &PortManifest{Shape: ShapeRows, Schema: schema}, nil

	case t == bytesType:
		return &PortManifest{Shape: ShapeBytes, Schema: Schema{{"type", "string"}, {"format", "byte"}}}, nil
	}

	schema, err := SchemaOf(t)
	if err != nil {
		return nil, err
	}

	return &PortManifest{Shape: ShapeValue, Schema: schema}, nil
}

var errorType = reflect.TypeFor[error]()

// isRows reports whether t is runtime.Rows[E] for some E.
func isRows(t reflect.Type) bool {
	if t.Kind() != reflect.Func || t.NumIn() != 1 || t.NumOut() != 0 || t.IsVariadic() {
		return false
	}
	yield := t.In(0)
	return yield.Kind() == reflect.Func &&
		yield.NumIn() == 2 && yield.In(1) == errorType &&
		yield.NumOut() == 1 && yield.Out(0).Kind() == reflect.Bool
}

// rowsElem returns E for a runtime.Rows[E] type.
func rowsElem(t reflect.Type) reflect.Type {
	return t.In(0).In(0)
}
