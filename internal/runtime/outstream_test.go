package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type putStore struct {
	mu          sync.Mutex
	body        []byte
	contentType string
	puts        int
}

func newPutStore(t *testing.T) (string, *putStore) {
	t.Helper()

	store := &putStore{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)

		store.mu.Lock()
		defer store.mu.Unlock()

		store.body = raw
		store.contentType = r.Header.Get("Content-Type")
		store.puts++
	}))

	t.Cleanup(server.Close)

	return server.URL, store
}

func streamCtx(inlineMaxBytes int, url, key string) *Ctx {
	return &Ctx{
		InlineMaxBytes:  inlineMaxBytes,
		OutputUploadURL: url,
		OutputKey:       key,
		MemoryLimitMB:   512,
	}
}

func closedRef(t *testing.T, out *OutputStream) *Written {
	t.Helper()

	require.NoError(t, out.Close())

	ref, err := out.Ref()
	require.NoError(t, err)

	return ref
}

func TestRoutingCanDependOnWhatWasWritten(t *testing.T) {
	out := streamCtx(big, "", "").OutputStream(NDJSON)
	defer out.Abort()

	count := 0

	for _, row := range []any{
		map[string]any{"id": 1},
		map[string]any{"id": 2},
	} {
		require.NoError(t, out.Write(row))
		count++
	}

	next := "process"
	if count == 0 {
		next = "empty"
	}

	env := envelope(t, Result{
		Output: closedRef(t, out),
		Next:   []string{next},
	}, big)

	require.Equal(t, []string{"process"}, env.Next)
	require.Equal(t, []any{map[string]any{"id": 1}, map[string]any{"id": 2}}, env.Output.Data)
}

func TestAnEmptyStreamRoutesTheOtherWay(t *testing.T) {
	out := streamCtx(big, "", "").OutputStream(NDJSON)
	env := envelope(t, Result{
		Output: closedRef(t, out),
		Next:   []string{"empty"},
	}, big)

	require.Equal(t, []string{"empty"}, env.Next)
	require.Equal(t, []any{}, env.Output.Data)
}

func TestTheReferenceMustStillBeReturned(t *testing.T) {
	out := streamCtx(big, "", "").OutputStream(NDJSON)
	require.NoError(t, out.Write(map[string]any{"id": 1}))
	require.IsType(t, &Written{}, closedRef(t, out))
	require.Equal(t, "<OutputStream application/x-ndjson written>", out.String())
}

func TestALargeStreamIsUploadedAndTheNodeCannotTell(t *testing.T) {
	base, store := newPutStore(t)
	out := streamCtx(64, base+"/put", "k").OutputStream(NDJSON)

	for index := range 50 {
		require.NoError(t, out.Write(map[string]any{"id": index, "pad": strings.Repeat("x", 40)}))
	}

	block := envelope(t, Result{Output: closedRef(t, out)}, 64).Output
	require.Equal(t, REFERENCE, block.Type)
	require.Equal(t, "k", block.Ref)
	require.Equal(t, 50, bytes.Count(store.body, []byte("\n")))

	first, err := DecodeJSON(bytes.SplitN(store.body, []byte("\n"), 2)[0])
	require.NoError(t, err)
	require.Equal(t, map[string]any{"id": json.Number("0"), "pad": strings.Repeat("x", 40)}, first)
}

func TestASmallStreamStaysInline(t *testing.T) {
	base, store := newPutStore(t)
	out := streamCtx(big, base+"/put", "k").OutputStream(NDJSON)

	require.NoError(t, out.Write(map[string]any{"id": 1}))
	require.Equal(t, INLINE, envelope(t, Result{Output: closedRef(t, out)}, big).Output.Type)
	require.Zero(t, store.puts)
}

func TestWritingAfterTheStreamClosedIsRefused(t *testing.T) {
	out := streamCtx(big, "", "").OutputStream(NDJSON)
	require.NoError(t, out.Close())
	require.ErrorContains(t, out.Write(map[string]any{"id": 1}), "after the stream closed")

	aborted := streamCtx(big, "", "").OutputStream(NDJSON)
	aborted.Abort()
	require.ErrorContains(t, aborted.Write(map[string]any{"id": 1}), "after the stream closed")
	require.Equal(t, "<OutputStream application/x-ndjson aborted>", aborted.String())
}

func TestAskingForTheReferenceTooEarlySaysSo(t *testing.T) {
	out := streamCtx(big, "", "").OutputStream(NDJSON)
	require.NoError(t, out.Write(map[string]any{"id": 1}))

	_, err := out.Ref()
	require.ErrorContains(t, err, "before the stream closed")
	require.Equal(t, "<OutputStream application/x-ndjson open>", out.String())
}

func TestAHandlerThatFailedSendsNothing(t *testing.T) {
	base, store := newPutStore(t)

	handler := func() (err error) {
		out := streamCtx(64, base+"/put", "k").OutputStream(NDJSON)
		defer out.Abort()

		for index := range 50 {
			if err := out.Write(map[string]any{"id": index, "pad": strings.Repeat("x", 40)}); err != nil {
				return err
			}
		}

		return errors.New("the node failed halfway")
	}

	require.EqualError(t, handler(), "the node failed halfway")
	require.Zero(t, store.puts)
}

func TestAbortAfterCloseNeverUndoesTheCommit(t *testing.T) {
	out := streamCtx(big, "", "").OutputStream(NDJSON)
	require.NoError(t, out.Write(map[string]any{"id": 1}))

	ref := closedRef(t, out)
	out.Abort()

	again, err := out.Ref()
	require.NoError(t, err)
	require.Same(t, ref, again)
	require.NoError(t, out.Close(), "a second Close is a no-op")
}

func TestAStreamPastWhatTheNodeCanBufferNamesTheRemedy(t *testing.T) {
	out := streamCtx(64, "", "").OutputStream(NDJSON)

	var failure error

	for index := range 1000 {
		if failure = out.Write(map[string]any{"id": index, "pad": strings.Repeat("x", 100)}); failure != nil {
			break
		}
	}

	require.True(t, errors.Is(failure, &OutputTooLarge{}))
	require.ErrorContains(t, failure, "max_output_mb")
}

func TestCSVFromTheWriterIsCSVWhenUploaded(t *testing.T) {
	base, store := newPutStore(t)
	out := streamCtx(8, base+"/put", "k").OutputStream(CSV)

	require.NoError(t, out.Write(map[string]any{"id": 1, "name": "ana"}))
	require.NoError(t, out.Write(map[string]any{"id": 2, "name": "bo"}))

	envelope(t, Result{Output: closedRef(t, out)}, 8)

	require.Equal(t, CSV, store.contentType)
	require.True(t, bytes.HasPrefix(store.body, []byte("id,name\r\n")), "%q", store.body)
}

func TestAZeroInlineLimitOnTheWriterTakesTheDefault(t *testing.T) {
	out := (&Ctx{InlineMaxBytes: 0}).OutputStream(NDJSON)
	require.NoError(t, out.Write(map[string]any{"id": 1}))
	require.Equal(t, INLINE, envelope(t, Result{Output: closedRef(t, out)}, 0).Output.Type)
}
