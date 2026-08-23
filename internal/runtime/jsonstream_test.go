package runtime

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// chunkReader yields the source data in discrete byte chunks to test split boundaries.
type chunkReader struct {
	chunks [][]byte
	pulled int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(c.chunks) == 0 {
		return 0, io.EOF
	}
	c.pulled++

	n := copy(p, c.chunks[0])

	if n < len(c.chunks[0]) {
		c.chunks[0] = c.chunks[0][n:]
	} else {
		c.chunks = c.chunks[1:]
	}

	return n, nil
}

func chunks(text string, size int) *chunkReader {
	raw := []byte(text)
	var out [][]byte

	for start := 0; start < len(raw); start += size {
		out = append(out, raw[start:min(start+size, len(raw))])
	}

	return &chunkReader{chunks: out}
}

// read collects all streamed array elements, returning the first error encountered.
func read(text string, size int) ([]any, error) {
	out := []any{}
	for element, err := range iterJSONArray(chunks(text, size)) {
		if err != nil {
			return nil, err
		}
		out = append(out, element)
	}

	return out, nil
}

// parsed decodes a full JSON document into a slice with exact number representations.
func parsed(t *testing.T, text string) []any {
	t.Helper()

	value, err := DecodeJSON([]byte(text))
	require.NoError(t, err)

	if value == nil {
		return nil
	}

	return value.([]any)
}

func TestEverySplitOfADocumentReadsTheSame(t *testing.T) {
	document := `[{"a": 1}, {"b": [2, 3]}, "x", 4, null, true]`
	for _, size := range []int{1, 2, 3, 5, 7, 16, 4096} {
		got, err := read(document, size)
		require.NoError(t, err, "size %d", size)
		require.Equal(t, parsed(t, document), got, "size %d", size)
	}
}

func TestAnEscapeSequenceSplitAcrossAChunkBoundary(t *testing.T) {
	document := `["a\"b", "c\\", "dée", "line\nbreak"]`
	for _, size := range []int{1, 2, 3, 4, 8} {
		got, err := read(document, size)
		require.NoError(t, err)
		require.Equal(t, []any{`a"b`, `c\`, "dée", "line\nbreak"}, got, "size %d", size)
	}
}

func TestAMultibyteCharacterSplitAcrossAChunkBoundary(t *testing.T) {
	document := `["日本語", "😀", "café"]`
	for _, size := range []int{1, 2, 3, 5} {
		got, err := read(document, size)
		require.NoError(t, err)
		require.Equal(t, []any{"日本語", "😀", "café"}, got, "size %d", size)
	}
}

func TestOneElementSpanningSeveralChunks(t *testing.T) {
	element := map[string]any{"pad": strings.Repeat("x", 500), "n": 7}
	raw, err := json.Marshal([]any{element, map[string]any{"tail": true}})
	require.NoError(t, err)
	for _, size := range []int{1, 4, 64} {
		got, err := read(string(raw), size)
		require.NoError(t, err)
		require.Equal(t, parsed(t, string(raw)), got, "size %d", size)
	}
}

func TestAnElementLargerThanTheWholeReadSize(t *testing.T) {
	element := map[string]any{"blob": strings.Repeat("y", 100_000)}
	raw, err := json.Marshal([]any{element})

	require.NoError(t, err)

	got, err := read(string(raw), 512)

	require.NoError(t, err)
	require.Equal(t, parsed(t, string(raw)), got)
}

func TestANumberAtAChunkBoundaryIsNotTruncated(t *testing.T) {
	document := "[125, 3.5, 1e3, -42]"

	for _, size := range []int{1, 2, 8} {
		got, err := read(document, size)
		require.NoError(t, err)
		require.Equal(t, []any{json.Number("125"), json.Number("3.5"), json.Number("1e3"), json.Number("-42")}, got, "size %d", size)
	}
}

func TestAnEmptyArrayYieldsNothing(t *testing.T) {
	for _, size := range []int{1, 3, 64} {
		got, err := read("[]", size)

		require.NoError(t, err)
		require.Empty(t, got)

		got, err = read("  [  ]  ", size)

		require.NoError(t, err)
		require.Empty(t, got)
	}
}

func TestNestedArrays(t *testing.T) {
	document := "[[1, [2, 3]], [], [[[4]]]]"

	for _, size := range []int{1, 2, 16} {
		got, err := read(document, size)
		require.NoError(t, err)
		require.Equal(t, parsed(t, document), got, "size %d", size)
	}
}

func TestAJSONObjectIsRefusedByName(t *testing.T) {
	_, err := read(`{"a": 1}`, 4)
	require.ErrorContains(t, err, "cannot be streamed as rows")
	require.ErrorContains(t, err, `found '{'`)
}

func TestAScalarDocumentIsRefusedByName(t *testing.T) {
	_, err := read(`42`, 4)
	require.ErrorContains(t, err, `expected a JSON array, found '4'`)
	_, err = read(`"text"`, 4)
	require.ErrorContains(t, err, `found '"'`)
}

func TestAnEmptyReferenceIsRefused(t *testing.T) {
	_, err := read("", 4)
	require.ErrorContains(t, err, "empty")
	_, err = read("   ", 4)
	require.ErrorContains(t, err, "empty")
}

func TestATruncatedDocumentIsDetectedAtEOF(t *testing.T) {
	for _, size := range []int{1, 4, 64} {
		_, err := read(`[{"a": 1}, {"b": `, size)
		require.ErrorContains(t, err, "not valid JSON", "size %d", size)
	}
}

func TestAnUnclosedArrayIsDetectedAtEOF(t *testing.T) {
	for _, size := range []int{1, 4, 64} {
		_, err := read(`[{"a": 1}, `, size)
		require.ErrorContains(t, err, "never closed", "size %d", size)

		_, err = read(`[{"a": 1}`, size)
		require.ErrorContains(t, err, "never closed", "size %d", size)

		_, err = read(`[`, size)
		require.ErrorContains(t, err, "never closed", "size %d", size)
	}
}

func TestCorruptionIsReportedRatherThanReadPast(t *testing.T) {
	_, err := read(`[{"a": 1} {"b": 2}]`, 4)
	require.Error(t, err)

	_, err = read(`[1,,2]`, 4)
	require.Error(t, err)
}

func TestElementsBeforeTheCorruptionAreStillDelivered(t *testing.T) {
	var got []any
	var failure error

	for element, err := range iterJSONArray(chunks(`[1, 2, {"x": }]`, 3)) {
		if err != nil {
			failure = err
			break
		}
		got = append(got, element)
	}
	require.Equal(t, []any{json.Number("1"), json.Number("2")}, got)
	require.Error(t, failure)
}

func TestReadingIsLazy(t *testing.T) {
	var out [][]byte

	for element := range 500 {
		prefix := ","
		if element == 0 {
			prefix = "["
		}

		out = append(out, fmt.Appendf(nil, `%s{"i": %d}`, prefix, element))
	}
	out = append(out, []byte("]"))
	source := &chunkReader{chunks: out}

	for element, err := range iterJSONArray(source) {
		require.NoError(t, err)
		require.Equal(t, map[string]any{"i": json.Number("0")}, element)
		break
	}

	require.Less(t, source.pulled, 10, "pulled %d chunks to yield the first element", source.pulled)
}

func TestItAgreesWithTheWholeDocumentDecoderOnRandomDocuments(t *testing.T) {
	rng := rand.New(rand.NewPCG(20260819, 20260819))
	alphabet := []rune("ab\"\\n\t日😀é/ ")

	var value func(depth int) any
	value = func(depth int) any {
		kinds := 9
		if depth >= 2 {
			kinds = 6
		}
		switch rng.IntN(kinds) {
		case 0:
			return rng.IntN(2_000_001) - 1_000_000
		case 1:
			return []any{0, -0.5, 1e3, 3.25, 2.5e-4}[rng.IntN(5)]
		case 2:
			return []any{true, false, nil}[rng.IntN(3)]
		case 3:
			var b strings.Builder
			for range rng.IntN(12) {
				b.WriteRune(alphabet[rng.IntN(len(alphabet))])
			}
			return b.String()
		case 4:
			return ""
		case 5:
			return []any{[]any{}, map[string]any{}}[rng.IntN(2)]
		case 6:
			items := make([]any, rng.IntN(4))
			for i := range items {
				items[i] = value(depth + 1)
			}
			return items
		case 7:
			record := map[string]any{}
			for i := range rng.IntN(4) {
				record[fmt.Sprintf("k%d", i)] = value(depth + 1)
			}
			return record
		default:
			return map[string]any{"nested": []any{value(depth + 1)}}
		}
	}

	for range 400 {
		document := make([]any, rng.IntN(6))
		for i := range document {
			document[i] = value(0)
		}
		var text []byte
		var err error

		if rng.IntN(2) == 0 {
			text, err = json.MarshalIndent(document, "", "  ")
		} else {
			text, err = json.Marshal(document)
		}

		require.NoError(t, err)
		size := []int{1, 2, 3, 5, 13, 64, 4096}[rng.IntN(7)]

		got, err := read(string(text), size)
		require.NoError(t, err, "split %d failed on %s", size, text)
		require.Equal(t, parsed(t, string(text)), got, "split %d changed %s", size, text)
	}
}
