package runtime

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
)

// encodeRows serializes every record in one batch, according to the declared
// content type: NDJSON lines, CSV, or a single JSON array.
//
// It writes through the shared compact encoder, so the bytes it uploads measure
// the same as the inline check that decided to upload them.
func encodeRows(records rows, contentType ContentType) ([]byte, error) {
	collected := []any{}

	for row, err := range records {
		if err != nil {
			return nil, err
		}

		collected = append(collected, row)
	}

	switch contentType {
	case NDJSON:
		var out bytes.Buffer

		enc := newCompactEncoder()
		for _, row := range collected {
			encoded, err := enc.encode(row)
			if err != nil {
				return nil, err
			}

			out.Write(encoded)
			out.WriteByte('\n')
		}

		return out.Bytes(), nil

	case CSV:
		return encodeCSV(collected)
	}

	return compact(collected)
}

// encodeCSV formats collected rows as CSV with sorted headers from the first record.
func encodeCSV(collected []any) ([]byte, error) {
	var out bytes.Buffer
	var header []string

	for _, row := range collected {
		record, err := asMapping(row)
		if err != nil {
			return nil, err
		}

		if header == nil {
			header = slices.Sorted(maps.Keys(record))
			writeCSVRecord(&out, header)
		}

		if extra := extraColumns(record, header); len(extra) > 0 {
			return nil, fmt.Errorf("a csv row does not match the header %v: it has fields not in the header: %v", header, extra)
		}

		cells := make([]string, len(header))
		for i, column := range header {
			cells[i] = cell(record[column])
		}

		writeCSVRecord(&out, cells)
	}

	return out.Bytes(), nil
}

// writeCSVRecord writes a single CRLF-terminated CSV record.
func writeCSVRecord(out *bytes.Buffer, fields []string) {
	var line bytes.Buffer

	w := csv.NewWriter(&line)
	w.Write(fields)
	w.Flush()

	out.Write(bytes.TrimSuffix(line.Bytes(), []byte("\n")))
	out.WriteString("\r\n")
}

// encodeValue serializes a single value according to the declared content type,
// passing raw bytes through unchanged and writing everything else with the
// shared compact encoder.
func encodeValue(value any, contentType ContentType) ([]byte, error) {
	if contentType == TEXT {
		return []byte(text(value)), nil
	}

	if raw, ok := value.([]byte); ok {
		return raw, nil
	}

	return compact(value)
}

// text converts a value into a plain string.
func text(value any) string {
	switch v := value.(type) {
	case string:
		return v

	case []byte:
		return string(v)

	case nil:
		return ""

	case fmt.Stringer:
		return v.String()

	default:
		return fmt.Sprint(v)
	}
}

// RowEncoder serializes records one at a time for a streaming upload, holding
// the framing each content type needs between rows.
type RowEncoder struct {
	contentType ContentType
	json        *compactEncoder
	started     bool
	header      []string
	csv         *csv.Writer
	buf         bytes.Buffer
}

// NewRowEncoder returns a streaming row encoder for the given content type.
func NewRowEncoder(contentType ContentType) *RowEncoder {
	return &RowEncoder{
		contentType: contentType,
		json:        newCompactEncoder(),
	}
}

// Encode serializes a single row to bytes, valid until the next call.
func (e *RowEncoder) Encode(row any) ([]byte, error) {
	switch e.contentType {
	case NDJSON:
		encoded, err := e.json.encode(row)
		if err != nil {
			return nil, err
		}

		return append(encoded, '\n'), nil

	case CSV:
		return e.csvRow(row)
	}

	// For JSON arrays, prepend '[' on the first row and ',' on subsequent rows.
	prefix := byte(',')
	if !e.started {
		prefix = '['
		e.started = true
	}

	encoded, err := e.json.encode(row)
	if err != nil {
		return nil, err
	}

	return append([]byte{prefix}, encoded...), nil
}

// Finish returns any closing bytes needed for the document.
func (e *RowEncoder) Finish() []byte {
	if isRows(e.contentType) {
		return nil
	}

	if e.started {
		return []byte{']'}
	}

	return []byte("[]")
}

// csvRow serializes one record as a CSV row, writing the header on the first call.
func (e *RowEncoder) csvRow(row any) ([]byte, error) {
	record, err := asMapping(row)
	if err != nil {
		return nil, err
	}

	e.buf.Reset()
	if e.csv == nil {
		// Header uses sorted keys from the first row.
		e.header = slices.Sorted(maps.Keys(record))
		e.csv = csv.NewWriter(&e.buf)

		if err := e.writeRecord(e.header); err != nil {
			return nil, err
		}
	}

	// Reject rows with unexpected fields to prevent misalignment.
	if extra := extraColumns(record, e.header); len(extra) > 0 {
		return nil, fmt.Errorf("a csv row does not match the header %v: it has fields not in the header: %v", e.header, extra)
	}

	cells := make([]string, len(e.header))
	for i, column := range e.header {
		cells[i] = cell(record[column])
	}

	if err := e.writeRecord(cells); err != nil {
		return nil, err
	}

	return e.buf.Bytes(), nil
}

// writeRecord formats and writes a CRLF-terminated CSV row.
func (e *RowEncoder) writeRecord(fields []string) error {
	if err := e.csv.Write(fields); err != nil {
		return err
	}

	e.csv.Flush()
	if err := e.csv.Error(); err != nil {
		return err
	}

	// Convert standard newline to CRLF.
	e.buf.Truncate(e.buf.Len() - 1)
	e.buf.WriteString("\r\n")

	return nil
}

// extraColumns returns any record keys not present in the header, sorted.
func extraColumns(record map[string]any, header []string) []string {
	var extra []string

	for key := range record {
		if !slices.Contains(header, key) {
			extra = append(extra, key)
		}
	}

	slices.Sort(extra)

	return extra
}

// asMapping converts a row into key-value pairs, decoding structs through JSON.
func asMapping(row any) (map[string]any, error) {
	if record, ok := row.(map[string]any); ok {
		return record, nil
	}

	raw, err := json.Marshal(row)
	if err != nil {
		return nil, fmt.Errorf("a csv row must be a mapping so it has column names, got %s: %w", typeName(row), err)
	}

	if len(raw) == 0 || raw[0] != '{' {
		return nil, fmt.Errorf("a csv row must be a mapping so it has column names, got %s", typeName(row))
	}

	value, err := DecodeJSON(raw)
	if err != nil {
		return nil, err
	}

	return value.(map[string]any), nil
}

// cell formats a single CSV field value. Nested values are encoded as JSON.
func cell(value any) string {
	switch v := value.(type) {
	case nil:
		return ""

	case string:
		return v

	case json.Number:
		return v.String()

	case bool:
		return strconv.FormatBool(v)

	case int:
		return strconv.Itoa(v)

	case int64:
		return strconv.FormatInt(v, 10)

	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)

	case []byte:
		return string(v)

	case map[string]any, []any:
		if out, err := compact(v); err == nil {
			return string(out)
		}
	}

	return fmt.Sprint(value)
}
