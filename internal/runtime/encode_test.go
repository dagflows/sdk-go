package runtime

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// seq converts in-memory records into a streaming rows iterator.
func seq(items ...any) rows {
	return func(yield func(any, error) bool) {
		for _, item := range items {
			if !yield(item, nil) {
				return
			}
		}
	}
}

var testRows = []any{
	map[string]any{"id": 1, "name": "ana"},
	map[string]any{"id": 2, "name": "bo"},
}

func TestStreamedAndBufferedEncodingsAreIdentical(t *testing.T) {
	for _, contentType := range []ContentType{NDJSON, CSV, JSON} {
		t.Run(contentType, func(t *testing.T) {
			encoder := NewRowEncoder(contentType)
			var streamed bytes.Buffer
			for _, row := range testRows {
				encoded, err := encoder.Encode(row)
				require.NoError(t, err)
				streamed.Write(encoded)
			}
			streamed.Write(encoder.Finish())

			batch, err := encodeRows(seq(testRows...), contentType)
			require.NoError(t, err)
			require.Equal(t, string(batch), streamed.String())
		})
	}
}

func TestAnEmptyStreamStillEncodesADocument(t *testing.T) {
	require.Equal(t, "[]", string(NewRowEncoder(JSON).Finish()))
	require.Empty(t, NewRowEncoder(NDJSON).Finish())
	require.Empty(t, NewRowEncoder(CSV).Finish())
}

func TestNDJSONIsOneCompactRecordPerLine(t *testing.T) {
	out, err := encodeRows(seq(testRows...), NDJSON)
	require.NoError(t, err)
	require.Equal(t, "{\"id\":1,\"name\":\"ana\"}\n{\"id\":2,\"name\":\"bo\"}\n", string(out))
}

func TestAJSONArrayIsCommaJoinedCompactValues(t *testing.T) {
	out, err := encodeRows(seq(1, "x", map[string]any{"a": []any{2}}), JSON)
	require.NoError(t, err)
	require.Equal(t, `[1,"x",{"a":[2]}]`, string(out))
}

func TestCSVTakesItsHeaderFromTheFirstRowAndEndsLinesWithCRLF(t *testing.T) {
	out, err := encodeRows(seq(testRows...), CSV)
	require.NoError(t, err)
	require.Equal(t, "id,name\r\n1,ana\r\n2,bo\r\n", string(out))
}

func TestACSVRowThatDoesNotMatchTheHeaderNamesTheHeader(t *testing.T) {
	encoder := NewRowEncoder(CSV)
	_, err := encoder.Encode(map[string]any{"id": 1, "name": "ana"})
	require.NoError(t, err)

	_, err = encoder.Encode(map[string]any{"id": 2, "surname": "bo"})
	require.ErrorContains(t, err, "does not match the header [id name]")
	require.ErrorContains(t, err, "surname")

	// Missing columns render empty rather than failing.
	encoded, err := encoder.Encode(map[string]any{"id": 3})
	require.NoError(t, err)
	require.Equal(t, "3,\r\n", string(encoded))
}

func TestACSVRowMustBeAMapping(t *testing.T) {
	_, err := NewRowEncoder(CSV).Encode(42)
	require.ErrorContains(t, err, "a csv row must be a mapping so it has column names, got int")

	_, err = encodeRows(seq([]any{1, 2}), CSV)
	require.ErrorContains(t, err, "must be a mapping")
}

func TestAStructIsACSVRowThroughItsJSONForm(t *testing.T) {
	type order struct {
		ID     int     `json:"id"`
		Amount float64 `json:"amount"`
		Note   *string `json:"note"`
	}
	out, err := encodeRows(seq(order{ID: 1, Amount: 2.5}), CSV)
	require.NoError(t, err)
	require.Equal(t, "amount,id,note\r\n2.5,1,\r\n", string(out))
}

func TestCSVCellsQuoteWhatNeedsQuotingAndKeepNestedValuesParseable(t *testing.T) {
	out, err := encodeRows(seq(map[string]any{
		"text":   "a,b \"q\"\nline",
		"flag":   true,
		"nested": map[string]any{"k": []any{1}},
		"none":   nil,
	}), CSV)
	require.NoError(t, err)
	require.Equal(t, "flag,nested,none,text\r\ntrue,\"{\"\"k\"\":[1]}\",,\"a,b \"\"q\"\"\nline\"\r\n", string(out))
}

func TestEncodingNeverEscapesHTMLAndMeasuresBytesNotCharacters(t *testing.T) {
	out, err := compact(map[string]any{"html": "<a href='x'>&</a>", "text": "日本語"})
	require.NoError(t, err)
	require.Equal(t, `{"html":"<a href='x'>&</a>","text":"日本語"}`, string(out))

	n, err := encodedLen(newCompactEncoder(), "日本語")
	require.NoError(t, err)
	require.Equal(t, int64(len(`"日本語"`)), n)
}

func TestEncodeValueRendersTextBytesAndJSON(t *testing.T) {
	out, err := encodeValue(42, TEXT)
	require.NoError(t, err)
	require.Equal(t, "42", string(out))

	out, err = encodeValue([]byte{0, 1}, BYTES)
	require.NoError(t, err)
	require.Equal(t, []byte{0, 1}, out)

	out, err = encodeValue(map[string]any{"a": json.Number("1.50")}, JSON)
	require.NoError(t, err)
	require.Equal(t, `{"a":1.50}`, string(out))
}

func TestDecodeJSONKeepsNumbersExactAndRefusesTrailingData(t *testing.T) {
	value, err := DecodeJSON([]byte(`{"id": 12345678901234567890, "f": 1.10}`))
	require.NoError(t, err)

	record := value.(map[string]any)
	require.Equal(t, json.Number("12345678901234567890"), record["id"])
	require.Equal(t, json.Number("1.10"), record["f"])

	_, err = DecodeJSON([]byte(`{"a": 1} {"b": 2}`))
	require.ErrorContains(t, err, "extra data")
}
