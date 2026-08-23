package runtime

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
)

// iterLines yields lines from r with trailing newlines (including CR) removed.
// Lines longer than the chunk buffer are accumulated and returned in full.
func iterLines(r io.Reader) iter2[[]byte] {
	return func(yield func([]byte, error) bool) {
		reader := bufio.NewReaderSize(r, chunkBytes)

		var long []byte

		for {
			line, err := reader.ReadSlice('\n')
			if errors.Is(err, bufio.ErrBufferFull) {
				long = append(long, line...)
				continue
			}

			if len(long) > 0 {
				line = append(long, line...)
				long = long[:0]
			}

			if len(line) > 0 || err == nil {
				line = bytes.TrimSuffix(line, []byte("\n"))
				line = bytes.TrimSuffix(line, []byte("\r"))

				if !yield(line, nil) {
					return
				}
			}

			if err != nil {
				if err != io.EOF {
					yield(nil, err)
				}

				return
			}
		}
	}
}

// iterNDJSON yields one decoded JSON value per non-empty line.
func iterNDJSON(r io.Reader) rows {
	return func(yield func(any, error) bool) {
		number := 0

		for line, err := range iterLines(r) {
			if err != nil {
				yield(nil, err)
				return
			}

			number++

			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}

			value, err := DecodeJSON(line)
			if err != nil {
				yield(nil, fmt.Errorf("line %d is not valid JSON: %w", number, err))
				return
			}

			if !yield(value, nil) {
				return
			}
		}
	}
}

// csvExtraKey is the map key for cells exceeding the header column count.
const csvExtraKey = ""

// iterCSV yields each row as a map keyed by the header columns.
// Missing cells are nil; extra cells are stored under csvExtraKey.
func iterCSV(r io.Reader) rows {
	return func(yield func(any, error) bool) {
		reader := csv.NewReader(bufio.NewReaderSize(r, chunkBytes))
		reader.FieldsPerRecord = -1
		reader.LazyQuotes = true
		reader.ReuseRecord = true

		header, err := reader.Read()
		if err != nil {
			if err != io.EOF {
				yield(nil, err)
			}

			return
		}

		header = append([]string(nil), header...)

		for {
			record, err := reader.Read()
			if err != nil {
				if err != io.EOF {
					yield(nil, err)
				}

				return
			}

			row := make(map[string]any, len(header))
			for i, column := range header {
				if i < len(record) {
					row[column] = record[i]
				} else {
					row[column] = nil
				}
			}

			if len(record) > len(header) {
				extra := make([]any, 0, len(record)-len(header))
				for _, cell := range record[len(header):] {
					extra = append(extra, cell)
				}

				row[csvExtraKey] = extra
			}

			if !yield(row, nil) {
				return
			}
		}
	}
}
