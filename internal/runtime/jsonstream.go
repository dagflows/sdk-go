package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// source records whether the underlying stream ran out. What the decoder calls
// a document that simply stops is not stable across Go versions: 1.26 reported
// io.EOF, 1.27 reports a *json.SyntaxError reading "unexpected end of JSON
// input", and 1.27 also keeps More() true at the end of a truncated array where
// 1.26 turned it false. Asking the reader whether it reached the end is the same
// question with an answer that does not move.
type source struct {
	r       io.Reader
	drained bool
}

func (s *source) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if errors.Is(err, io.EOF) {
		s.drained = true
	}

	return n, err
}

func (s *source) endedEarly(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	var syntax *json.SyntaxError

	return s.drained && errors.As(err, &syntax)
}

// iterJSONArray streams elements from a top-level JSON array in r.
func iterJSONArray(r io.Reader) rows {
	return func(yield func(any, error) bool) {
		src := &source{
			r: r,
		}
		dec := json.NewDecoder(src)
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
				yield(nil, elementError(dec, src, err))
				return
			}

			if !yield(element, nil) {
				return
			}
		}

		// Read the closing array bracket or detect unexpected EOF.
		if _, err := dec.Token(); err != nil {
			if src.endedEarly(err) {
				yield(nil, errors.New("the array is never closed, the reference ended mid document"))
				return
			}

			yield(nil, notJSON(err))
		}
	}
}

// elementError distinguishes unclosed arrays from truncated element values at EOF.
func elementError(dec *json.Decoder, src *source, err error) error {
	if src.endedEarly(err) {
		pending, _ := io.ReadAll(dec.Buffered())
		if strings.Trim(string(pending), " \t\r\n,") == "" {
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
