package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func collect(t *testing.T, records rows) []any {
	t.Helper()
	out := []any{}

	for row, err := range records {
		require.NoError(t, err)
		out = append(out, row)
	}

	return out
}

func TestLinesAreSplitOnNewlineWithCRStripped(t *testing.T) {
	var got []string

	for line, err := range iterLines(strings.NewReader("a\r\nb\n\nc")) {
		require.NoError(t, err)
		got = append(got, string(line))
	}

	require.Equal(t, []string{"a", "b", "", "c"}, got)
}

func TestALineLongerThanTheBufferArrivesWhole(t *testing.T) {
	long := strings.Repeat("x", 3*chunkBytes)
	var got []string

	for line, err := range iterLines(strings.NewReader(long + "\ny\n")) {
		require.NoError(t, err)
		got = append(got, string(line))
	}

	require.Equal(t, []string{long, "y"}, got)
}

func TestNDJSONYieldsOneValuePerLineAndSkipsBlankLines(t *testing.T) {
	body := "{\"id\": 1, \"name\": \"ana\"}\n\n  \n{\"id\": 2, \"name\": \"bo\"}\n"
	require.Equal(t, []any{
		map[string]any{"id": json.Number("1"), "name": "ana"},
		map[string]any{"id": json.Number("2"), "name": "bo"},
	}, collect(t, iterNDJSON(strings.NewReader(body))))
}

func TestAnInvalidNDJSONLineNamesItsNumber(t *testing.T) {
	var failure error

	for _, err := range iterNDJSON(strings.NewReader("{\"a\": 1}\n\n{oops}\n")) {
		if err != nil {
			failure = err
		}
	}

	require.ErrorContains(t, failure, "line 3 is not valid JSON")
}

func TestCSVYieldsOneMapPerRowWithQuotedNewlinesPreserved(t *testing.T) {
	body := "id,name\r\n1,ana\r\n2,\"bo\nbi\"\r\n"
	require.Equal(t, []any{
		map[string]any{"id": "1", "name": "ana"},
		map[string]any{"id": "2", "name": "bo\nbi"},
	}, collect(t, iterCSV(strings.NewReader(body))))
}

func TestCSVRowsShorterOrLongerThanTheHeaderStillRead(t *testing.T) {
	body := "a,b\n1\n1,2,3\n"
	require.Equal(t, []any{
		map[string]any{"a": "1", "b": nil},
		map[string]any{"a": "1", "b": "2", csvExtraKey: []any{"3"}},
	}, collect(t, iterCSV(strings.NewReader(body))))
}

func TestAnEmptyCSVHasNoRows(t *testing.T) {
	require.Empty(t, collect(t, iterCSV(strings.NewReader(""))))
	require.Empty(t, collect(t, iterCSV(strings.NewReader("a,b\n"))))
}

func TestCSVWrittenByTheEncoderReadsBack(t *testing.T) {
	encoded, err := encodeRows(seq(
		map[string]any{"id": 1, "text": "a,b \"q\"\nline"},
		map[string]any{"id": 2, "text": ""},
	), CSV)

	require.NoError(t, err)

	require.Equal(t, []any{
		map[string]any{"id": "1", "text": "a,b \"q\"\nline"},
		map[string]any{"id": "2", "text": ""},
	}, collect(t, iterCSV(strings.NewReader(string(encoded)))))
}
