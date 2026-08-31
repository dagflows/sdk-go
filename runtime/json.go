package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// compactEncoder writes the JSON profile every dagflows SDK shares.
//
// It uses compact separators with no extra whitespace, leaves HTML characters
// unescaped so the bytes are the raw UTF-8 a reader in another language would
// measure, rejects values JSON cannot represent such as NaN and infinity, and
// strips the trailing newline encoding/json appends. One encoder is reused
// across records to minimize allocations.
type compactEncoder struct {
	buf bytes.Buffer
	enc *json.Encoder
}

// newCompactEncoder returns an encoder writing the shared JSON profile.
func newCompactEncoder() *compactEncoder {
	c := &compactEncoder{}
	c.enc = json.NewEncoder(&c.buf)
	c.enc.SetEscapeHTML(false)

	return c
}

// encode converts a value to compact JSON bytes, valid until the next call.
func (c *compactEncoder) encode(value any) ([]byte, error) {
	c.buf.Reset()

	if err := c.enc.Encode(value); err != nil {
		return nil, err
	}

	// Strip trailing newline added by Encoder.
	return bytes.TrimSuffix(c.buf.Bytes(), []byte{'\n'}), nil
}

// compact encodes a single value to compact JSON, returning bytes the caller
// owns rather than a view of a reused buffer.
func compact(value any) ([]byte, error) {
	out, err := newCompactEncoder().encode(value)
	if err != nil {
		return nil, err
	}

	return bytes.Clone(out), nil
}

// encodedLen returns the byte length of the value when encoded as compact JSON.
//
// Every size decision goes through here, so a running total and the final
// inline check measure the same bytes.
func encodedLen(enc *compactEncoder, value any) (int64, error) {
	out, err := enc.encode(value)
	if err != nil {
		return 0, err
	}

	return int64(len(out)), nil
}

// DecodeJSON parses a JSON document, using json.Number so large integers and floats don't lose precision.
func DecodeJSON(data []byte) (any, error) {
	return DecodeJSONFrom(bytes.NewReader(data))
}

// DecodeJSONFrom parses a JSON document from r and errors if there is any trailing data.
func DecodeJSONFrom(r io.Reader) (any, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()

	var value any

	if err := dec.Decode(&value); err != nil {
		return nil, err
	}

	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return nil, errors.New("extra data after the JSON document")
		}

		return nil, err
	}

	return value, nil
}

// typeName formats a value type name for error messages.
func typeName(value any) string {
	if value == nil {
		return "nil"
	}

	return fmt.Sprintf("%T", value)
}
