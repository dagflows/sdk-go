package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// iterJSONArray streams elements from a top-level JSON array in r.
func iterJSONArray(r io.Reader) rows {
	return func(yield func(any, error) bool) {
		dec := json.NewDecoder(r)
		dec.UseNumber()

		open, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				yield(nil, errors.New("the reference is empty, expected a JSON array"))
				return
			}

			yield(nil, notJSON(err))
			return
		}

		if delim, ok := open.(json.Delim); !ok || delim != '[' {
			yield(nil,
				fmt.Errorf(
					"expected a JSON array, found %q, a JSON object cannot be streamed as rows, so send ndjson or read it with .value()",
					firstByte(open),
				),
			)
			return
		}

		for dec.More() {
			var element any

			if err := dec.Decode(&element); err != nil {
				yield(nil, elementError(dec, err))
				return
			}

			if !yield(element, nil) {
				return
			}
		}

		// Read the closing array bracket or detect unexpected EOF.
		if _, err := dec.Token(); err != nil {
			if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) {
				yield(nil, errors.New("the array is never closed, the reference ended mid document"))
				return
			}

			yield(nil, notJSON(err))
		}
	}
}

// elementError distinguishes unclosed arrays from truncated element values at EOF.
func elementError(dec *json.Decoder, err error) error {
	if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) {
		pending, _ := io.ReadAll(dec.Buffered())
		if strings.TrimSpace(string(pending)) == "" {
			return errors.New("the array is never closed, the reference ended mid document")
		}

		return errors.New("the reference is not valid JSON, the document ends mid value")
	}

	return notJSON(err)
}

func notJSON(err error) error {
	return fmt.Errorf("the reference is not valid JSON: %w", err)
}

// firstByte returns a character representation of the token that replaced the expected '[' bracket.
func firstByte(token json.Token) rune {
	switch v := token.(type) {
	case json.Delim:
		return rune(v)

	case string:
		return '"'

	case json.Number:
		return rune(v[0])

	case bool:
		if v {
			return 't'
		}

		return 'f'

	case nil:
		return 'n'
	}

	return unicode.ReplacementChar
}
