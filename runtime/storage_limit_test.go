package runtime

import (
	"bytes"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// countedStorage answers every PUT and counts them.
func countedStorage(t *testing.T) (string, *atomic.Int64) {
	t.Helper()

	var puts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts.Add(1)
		}
	}))
	t.Cleanup(server.Close)

	return server.URL, &puts
}

// Tests that the context carries the most the output may store and whether
// the organization's storage is full, into the upload every output goes
// through, and that a platform that sends neither limits nothing.
func TestTheContextSaysWhatTheOutputMayStore(t *testing.T) {
	ctx := CtxFromRaw(map[string]any{"max_output_bytes": float64(67108864), "storage_full": true})
	require.EqualValues(t, 67108864, ctx.MaxOutputBytes)
	require.True(t, ctx.StorageFull)
	require.Equal(t, int64(67108864), ctx.upload().MaxBytes)
	require.True(t, ctx.upload().StorageFull)

	none := CtxFromRaw(map[string]any{})
	require.Zero(t, none.MaxOutputBytes)
	require.False(t, none.StorageFull)
}

// Tests that a value over the most the output may store is refused before it
// is uploaded, naming both sizes, and that one at the most is stored.
func TestAValueOverWhatItMayStoreIsRefusedBeforeItsUpload(t *testing.T) {
	base, puts := countedStorage(t)
	upload := &Upload{URL: base + "/put", Key: "node-outputs/run/export/0", MaxBytes: 1000}
	value := map[string]any{"blob": strings.Repeat("x", 2000)}

	_, err := ToEnvelope(value, 64, upload, 0)
	require.True(t, errors.Is(err, &OutputTooLarge{}))
	require.ErrorContains(t, err, "over the 1000 bytes it may store")
	require.ErrorContains(t, err, "max_output_mb")
	require.Zero(t, puts.Load(), "nothing was uploaded")

	encoded, err := encodeValue(value, JSON)
	require.NoError(t, err)
	upload.MaxBytes = int64(len(encoded))
	env, err := ToEnvelope(value, 64, upload, 0)
	require.NoError(t, err)
	require.Equal(t, REFERENCE, env.Output.Type)
	require.EqualValues(t, 1, puts.Load(), "the most it may store is stored")
}

// Tests that while the organization's storage is full an output small enough
// to return inline is kept, and one that must be stored is refused saying so.
func TestAFullStorageRefusesOnlyAnOutputThatMustBeStored(t *testing.T) {
	full := &Upload{StorageFull: true}

	env, err := ToEnvelope(map[string]any{"ok": true}, 64, full, 512)
	require.NoError(t, err)
	require.Equal(t, INLINE, env.Output.Type)

	_, err = ToEnvelope(map[string]any{"blob": strings.Repeat("x", 200)}, 64, full, 512)
	require.True(t, errors.Is(err, &OutputTooLarge{}))
	require.ErrorContains(t, err, "the organization's storage is full")
}

// Tests that rows are refused as soon as they pass the inline limit while the
// organization's storage is full, before more are read and held.
func TestRowsStopAtTheInlineLimitWhileTheStorageIsFull(t *testing.T) {
	consumed := 0
	rows := func(yield func(any) bool) {
		for index := range 1_000_000 {
			consumed++

			if !yield(map[string]any{"index": index, "pad": strings.Repeat("x", 100)}) {
				return
			}
		}
	}

	_, err := ToEnvelope(iter.Seq[any](rows), 1024, &Upload{StorageFull: true}, 512)
	require.ErrorContains(t, err, "the organization's storage is full")
	require.Less(t, consumed, 100, "consumed %d rows past a 1KB limit", consumed)
}

// Tests that the writer stops at the inline limit while the organization's
// storage is full.
func TestTheWriterStopsAtTheInlineLimitWhileTheStorageIsFull(t *testing.T) {
	full := CtxFromRaw(map[string]any{
		"inline_max_bytes": float64(64),
		"memory_mb":        float64(512),
		"storage_full":     true,
	})
	out := full.OutputStream(NDJSON)
	defer out.Abort()

	var err error

	for row := range paddedRows(1000) {
		if err = out.Write(row); err != nil {
			break
		}
	}

	require.ErrorContains(t, err, "the organization's storage is full")
}

// Tests that a streamed output stops at the most it may store: the write that
// would pass it is not sent, and the upload is abandoned, so none of it is
// stored.
func TestAStreamStopsAtWhatItMayStore(t *testing.T) {
	url, c := newChannel(t)
	maxBytes := int64(100*1024 + 100)
	upload := streaming(url)
	upload.MaxBytes = maxBytes

	_, err := ToEnvelope(Result[any]{Output: paddedRows(2000), ContentType: NDJSON}, 1024, upload, 512)
	require.True(t, errors.Is(err, &OutputTooLarge{}))
	require.ErrorContains(t, err, "passed the "+strconv.FormatInt(maxBytes, 10)+" bytes it may store")

	got := c.one(t)
	require.LessOrEqual(t, int64(len(got.body)), maxBytes, "nothing past the most was sent")
	assertCut(t, got)
}

// Tests that a streamed output of exactly the most it may store is stored.
func TestAStreamOfExactlyWhatItMayStoreIsStored(t *testing.T) {
	url, c := newChannel(t)
	chunk := []byte(strings.Repeat("x", 999) + "\n")
	stream := OpenStream(url, NDJSON, int64(30*len(chunk)))

	for range 30 {
		require.NoError(t, stream.Write(chunk))
	}

	size, err := stream.Finish()
	require.NoError(t, err)
	require.EqualValues(t, 30*len(chunk), size)

	got := c.one(t)
	require.True(t, got.ended)
	require.Equal(t, bytes.Repeat(chunk, 30), got.body)
}
