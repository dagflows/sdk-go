package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

var bigRows = func() []any {
	out := make([]any, 0, 50)

	for i := range 50 {
		out = append(out, map[string]any{
			"id":   i,
			"name": fmt.Sprintf("n%d", i),
			"pad":  strings.Repeat("x", 100),
		})
	}

	return out
}()

// objectStore serves back whatever was PUT, keyed by path.
func objectStore(t *testing.T) (string, *sync.Map) {
	t.Helper()

	var stored sync.Map

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			stored.Store(r.URL.Path, body)

		case http.MethodGet:
			body, ok := stored.Load(r.URL.Path)
			if !ok {
				http.NotFound(w, r)
				return
			}

			w.Write(body.([]byte))
		}
	}))

	t.Cleanup(server.Close)

	return server.URL, &stored
}

func produce(t *testing.T, base string, rows []any, contentType ContentType, inlineMaxBytes int) *Block {
	t.Helper()

	var output any = seqOf(rows)
	if contentType == JSON {
		output = rows
	}

	env, err := ToEnvelope(
		Result{
			Output:      output,
			ContentType: contentType,
		},
		inlineMaxBytes,
		&Upload{
			URL: base + "/obj",
			Key: "node-outputs/run/producer/0",
		},
		512,
		nil,
	)
	require.NoError(t, err)

	return env.Output
}

func consume(base string, block *Block) *Input {
	var entry map[string]any

	if block.Type == INLINE {
		entry = map[string]any{
			"type":         INLINE,
			"data":         block.Data,
			"content_type": block.ContentType,
		}
	} else {
		entry = map[string]any{
			"type":         REFERENCE,
			"url":          base + "/obj",
			"size":         block.Size,
			"content_type": block.ContentType,
		}
	}

	return &Input{
		key:           "parent",
		entry:         entry,
		memoryLimitMB: 512,
	}
}

// wireRows reflects JSON round-trip behavior preserving exact numbers.
func wireRows(t *testing.T, rows []any) []any {
	t.Helper()

	raw, err := compact(rows)
	require.NoError(t, err)

	parsed, err := DecodeJSON(raw)
	require.NoError(t, err)

	return parsed.([]any)
}

func TestRowsSurviveTheTripWhenTheyHaveToBeUploaded(t *testing.T) {
	for _, contentType := range []ContentType{NDJSON, JSON} {
		base, _ := objectStore(t)
		block := produce(t, base, bigRows, contentType, 64)
		require.Equal(t, REFERENCE, block.Type, "the fixture must be big enough to offload")
		require.Equal(t, wireRows(t, bigRows), collect(t, consume(base, block).Iter()), contentType)
	}
}

func TestCSVRowsSurviveTheTrip(t *testing.T) {
	base, _ := objectStore(t)
	block := produce(t, base, bigRows, CSV, 64)
	require.Equal(t, REFERENCE, block.Type)
	require.Equal(t, asText(bigRows), asText(collect(t, consume(base, block).Iter())))
}

func TestTheSameRowsReadTheSameWhicheverSideOfTheThreshold(t *testing.T) {
	for _, contentType := range []ContentType{NDJSON, CSV} {
		base, _ := objectStore(t)
		small := consume(base, produce(t, base, testRows, contentType, 1<<20))
		large := consume(base, produce(t, base, testRows, contentType, 8))

		require.Equal(t, INLINE, small.Type())
		require.Equal(t, REFERENCE, large.Type())
		require.Equal(t, asText(testRows), asText(collect(t, small.Iter())), contentType)
		require.Equal(t, asText(testRows), asText(collect(t, large.Iter())), contentType)
	}
}

func TestAnUploadedObjectIsThePayloadNotTheEnvelope(t *testing.T) {
	base, stored := objectStore(t)
	produce(t, base, bigRows, NDJSON, 64)
	body, _ := stored.Load("/obj")
	raw := body.([]byte)
	require.False(t, bytes.HasPrefix(bytes.TrimSpace(raw), []byte(`{"status"`)), "the envelope must not be uploaded")
	require.Equal(t, len(bigRows), bytes.Count(raw, []byte("\n")), "ndjson is one record per line")
}

func TestAnUploadedJSONValueReadsBackAsItself(t *testing.T) {
	base, stored := objectStore(t)
	payload := map[string]any{
		"blob": strings.Repeat("x", 5000),
		"n":    json.Number("1.50"),
	}

	env, err := ToEnvelope(payload, 64, &Upload{
		URL: base + "/obj",
		Key: "k",
	}, 0, nil)
	require.NoError(t, err)
	require.Equal(t, REFERENCE, env.Output.Type)
	require.Equal(t, JSON, env.Output.ContentType)

	body, _ := stored.Load("/obj")
	require.Equal(t, int64(len(body.([]byte))), env.Output.Size)

	value, err := consume(base, env.Output).Value()
	require.NoError(t, err)
	require.Equal(t, payload, value)
}

func TestTextRoundTripsThroughStorage(t *testing.T) {
	base, _ := objectStore(t)
	text := strings.Repeat("héllo wörld\n", 20)

	env, err := ToEnvelope(Result{
		Output:      text,
		ContentType: TEXT,
	}, 16, &Upload{
		URL: base + "/obj",
		Key: "k",
	}, 0, nil)
	require.NoError(t, err)
	require.Equal(t, REFERENCE, env.Output.Type)

	value, err := consume(base, env.Output).Value()
	require.NoError(t, err)
	require.Equal(t, text, value)
}
