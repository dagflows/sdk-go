package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

var inputRows = []any{
	map[string]any{"id": json.Number("1"), "name": "ana"},
	map[string]any{"id": json.Number("2"), "name": "bo"},
}

var bodies = map[string][]byte{
	"/rows.ndjson": []byte("{\"id\": 1, \"name\": \"ana\"}\n{\"id\": 2, \"name\": \"bo\"}\n"),
	"/rows.csv":    []byte("id,name\r\n1,ana\r\n2,bo\r\n"),
	"/rows.json":   []byte(`[{"id": 1, "name": "ana"}, {"id": 2, "name": "bo"}]`),
	"/object.json": []byte(`{"id": 1}`),
	"/text.txt":    []byte("hello\nworld"),
	"/blob.bin":    bytes.Repeat(blob(), 4),
	"/big.ndjson":  append([]byte("{\"i\": 0}\n"), bytes.Repeat([]byte("{\"i\": 1, \"pad\": \""+strings.Repeat("x", 100)+"\"}\n"), 550_000)...),
}

func blob() []byte {
	out := make([]byte, 256)

	for i := range out {
		out[i] = byte(i)
	}

	return out
}

type served struct {
	gets  atomic.Int64
	bytes atomic.Int64
}

func newInputServer(t *testing.T) (string, *served) {
	t.Helper()

	stats := &served{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}

		stats.gets.Add(1)
		w.Header().Set("Content-Length", itoa(len(body)))

		flusher := w.(http.Flusher)

		for start := 0; start < len(body); start += 1024 {
			piece := body[start:min(start+1024, len(body))]
			if _, err := w.Write(piece); err != nil {
				return
			}

			flusher.Flush()
			stats.bytes.Add(int64(len(piece)))
		}
	}))

	t.Cleanup(server.Close)

	return server.URL, stats
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

func inline(data any, contentType ContentType) *Input {
	return &Input{
		key: "x",
		entry: map[string]any{
			"type":         INLINE,
			"data":         data,
			"content_type": contentType,
		},
	}
}

func reference(base, path string, contentType ContentType, size int64, memoryLimitMB int) *Input {
	return &Input{
		key: "x",
		entry: map[string]any{
			"type":         REFERENCE,
			"url":          base + path,
			"content_type": contentType,
			"size":         size,
		},
		memoryLimitMB: memoryLimitMB,
	}
}

func asText(rows []any) []map[string]string {
	out := make([]map[string]string, 0, len(rows))

	for _, row := range rows {
		record := map[string]string{}

		for key, value := range row.(map[string]any) {
			record[key] = Str(value)
		}

		out = append(out, record)
	}

	return out
}

func TestTheSameNodeCodeReadsTheSameEitherSideOfTheThreshold(t *testing.T) {
	base, _ := newInputServer(t)

	for _, tc := range []struct {
		contentType ContentType
		path        string
	}{
		{NDJSON, "/rows.ndjson"},
		{CSV, "/rows.csv"},
	} {
		small := collect(t, inline(inputRows, tc.contentType).Iter())
		large := collect(t, reference(base, tc.path, tc.contentType, 1, 0).Iter())

		require.Equal(t, inputRows, small)
		require.Equal(t, asText(small), asText(large), tc.contentType)
	}
}

func TestAnInlineValueIteratesOnceRatherThanOverItsKeys(t *testing.T) {
	require.Equal(t, []any{map[string]any{"count": 12}}, collect(t, inline(map[string]any{"count": 12}, JSON).Iter()))
}

func TestAReferenceJSONArrayStreams(t *testing.T) {
	base, _ := newInputServer(t)
	require.Equal(t, inputRows, collect(t, reference(base, "/rows.json", JSON, 1, 0).Iter()))
}

func TestAReferenceJSONObjectIsRefusedByName(t *testing.T) {
	base, _ := newInputServer(t)

	var failure error

	for _, err := range reference(base, "/object.json", JSON, 1, 0).Iter() {
		failure = err
	}

	require.ErrorContains(t, failure, "cannot be streamed as rows")
}

func TestBinaryHasNoRecordsAndSaysSo(t *testing.T) {
	base, stats := newInputServer(t)

	var failure error

	for _, err := range reference(base, "/blob.bin", BYTES, 1, 0).Iter() {
		failure = err
	}

	var unavailable *InputUnavailable

	require.ErrorAs(t, failure, &unavailable)
	require.Contains(t, failure.Error(), ".bytes()")
	require.Zero(t, stats.gets.Load(), "the refusal must not cost a download")

	_, err := reference(base, "/blob.bin", BYTES, 1, 0).Value()
	require.ErrorAs(t, err, &unavailable)
	require.Contains(t, err.Error(), "not JSON; read it with .bytes()")
}

func readBytes(t *testing.T, handle *Input) []byte {
	t.Helper()

	body, err := handle.Bytes()
	require.NoError(t, err)

	defer body.Close()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)

	return raw
}

func TestBytesIsTotal(t *testing.T) {
	base, _ := newInputServer(t)

	require.Equal(t, bodies["/blob.bin"], readBytes(t, reference(base, "/blob.bin", BYTES, 1, 0)))
	require.JSONEq(t, `{"a": 1}`, string(readBytes(t, inline(map[string]any{"a": 1}, JSON))))
	require.Equal(t, "hello", string(readBytes(t, inline("hello", TEXT))))
	require.Equal(t, `"hello"`, string(readBytes(t, inline("hello", JSON))))
}

func TestASmallReferenceStillMaterialises(t *testing.T) {
	base, _ := newInputServer(t)

	value, err := reference(base, "/rows.ndjson", NDJSON, 1, 0).Value()
	require.NoError(t, err)
	require.Equal(t, inputRows, value)

	value, err = reference(base, "/rows.json", JSON, 1, 512).Value()
	require.NoError(t, err)
	require.Equal(t, inputRows, value)

	value, err = reference(base, "/text.txt", TEXT, 1, 512).Value()
	require.NoError(t, err)
	require.Equal(t, "hello\nworld", value)

	value, err = reference(base, "/rows.csv", CSV, 1, 512).Value()
	require.NoError(t, err)
	require.Equal(t, []any{
		map[string]any{"id": "1", "name": "ana"},
		map[string]any{"id": "2", "name": "bo"},
	}, value)
}

func TestAValueThatWouldNotFitIsRefusedBeforeItIsFetched(t *testing.T) {
	base, stats := newInputServer(t)
	handle := reference(base, "/rows.ndjson", NDJSON, 100*1024*1024, 512)

	_, err := handle.Value()
	require.True(t, errors.Is(err, &InputTooLarge{}))
	require.Zero(t, stats.gets.Load(), "the refusal must not cost a download")

	// Iterating provides memory-safe row access without full buffering.
	require.Equal(t, inputRows, collect(t, handle.Iter()))
}

func TestIterationIsReEntrantRatherThanASpentGenerator(t *testing.T) {
	base, stats := newInputServer(t)
	handle := reference(base, "/rows.ndjson", NDJSON, 1, 0)

	first := collect(t, handle.Iter())
	second := collect(t, handle.Iter())

	require.Equal(t, inputRows, first)
	require.Equal(t, inputRows, second)
	require.Equal(t, int64(2), stats.gets.Load())
}

func TestStoppingEarlyDoesNotPayForTheRest(t *testing.T) {
	base, stats := newInputServer(t)
	handle := reference(base, "/big.ndjson", NDJSON, 1, 0)

	for row, err := range handle.Iter() {
		require.NoError(t, err)
		require.Equal(t, map[string]any{"i": json.Number("0")}, row)
		break
	}

	total := int64(len(bodies["/big.ndjson"]))
	require.Less(t, stats.bytes.Load(), total/2, "read %d of %d bytes to take one row", stats.bytes.Load(), total)
}

func TestAReferenceWithARowTypeIsAListWhenAskedForAValue(t *testing.T) {
	base, _ := newInputServer(t)

	value, err := reference(base, "/rows.ndjson", NDJSON, 1, 0).Value()
	require.NoError(t, err)
	require.Len(t, value, 2)
}

func TestMaterialisingTwiceCostsOneDownload(t *testing.T) {
	base, stats := newInputServer(t)
	handle := reference(base, "/rows.ndjson", NDJSON, 1, 0)

	value, err := handle.Value()
	require.NoError(t, err)
	require.Equal(t, inputRows, value)

	again, err := handle.Value()
	require.NoError(t, err)
	require.Equal(t, inputRows, again)
	require.Equal(t, inputRows, collect(t, handle.Iter()), "iteration reads the cached value")
	require.Equal(t, int64(1), stats.gets.Load())
}

func TestACachedJSONObjectStillRefusesToIterate(t *testing.T) {
	base, _ := newInputServer(t)
	handle := reference(base, "/object.json", JSON, 1, 0)

	_, err := handle.Value()
	require.NoError(t, err)

	var failure error

	for _, err := range handle.Iter() {
		failure = err
	}

	require.ErrorContains(t, failure, "cannot be streamed as rows")
}

func TestAMissingReferenceIsAnInfrastructureFailure(t *testing.T) {
	base, _ := newInputServer(t)

	_, err := reference(base, "/absent.ndjson", NDJSON, 1, 0).Value()
	fail, ok := errors.AsType[*Fail](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, INFRASTRUCTURE, fail.Category)
	require.Contains(t, fail.Message, "HTTP 404")
}
