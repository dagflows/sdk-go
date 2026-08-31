package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
)

var (
	anyType   = reflect.TypeFor[any]()
	bytesType = reflect.TypeFor[[]byte]()
)

// None is the input of a node with no parents.
type None struct{}

// Decimal is an arbitrary precision decimal carried as its string form, the
// profile's spelling for a number JSON cannot hold exactly.
type Decimal string

// convert decodes a JSON value already parsed into any (with json.Number for
// numbers) into T, by way of the compact encoding. Numbers stay exact: any
// fields inside T keep json.Number.
func convert[T any](raw any, where string) (T, error) {
	var out T
	if err := convertInto(raw, &out, where); err != nil {
		return out, err
	}
	return out, nil
}

// convertInto decodes raw into the value target points at.
func convertInto(raw any, target any, where string) error {
	data, err := compact(raw)
	if err != nil {
		return fmt.Errorf("'%s': %w", where, err)
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(target); err != nil {
		return fmt.Errorf("'%s': %w", where, err)
	}
	return nil
}

// isRowsType reports whether t is Rows[E] for some E: an iter.Seq2[E, error].
func isRowsType(t reflect.Type) bool {
	if t.Kind() != reflect.Func || t.NumIn() != 1 || t.NumOut() != 0 || t.IsVariadic() {
		return false
	}
	yield := t.In(0)
	return yield.Kind() == reflect.Func &&
		yield.NumIn() == 2 && yield.In(1) == errorType &&
		yield.NumOut() == 1 && yield.Out(0).Kind() == reflect.Bool
}

// rowsElem is E for a Rows[E] type.
func rowsElem(t reflect.Type) reflect.Type {
	return t.In(0).In(0)
}

// typedRows adapts a stream of decoded records into a Rows[E] of type t,
// decoding each record into E as it is asked for.
func typedRows(source rows, t reflect.Type, where string) reflect.Value {
	yieldType := t.In(0)
	elem := rowsElem(t)

	return reflect.MakeFunc(t, func(args []reflect.Value) []reflect.Value {
		yield := args[0]
		zero := reflect.Zero(elem)

		for row, err := range source {
			value := zero
			if err == nil {
				ptr := reflect.New(elem)
				if err = convertInto(row, ptr.Interface(), where); err == nil {
					value = ptr.Elem()
				}
			}

			errValue := reflect.Zero(errorType)
			if err != nil {
				errValue = reflect.ValueOf(&err).Elem()
			}

			if !yield.Call([]reflect.Value{value, errValue})[0].Bool() {
				return nil
			}
		}

		_ = yieldType
		return nil
	})
}
