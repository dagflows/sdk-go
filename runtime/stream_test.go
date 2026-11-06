package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// channel stands for the platform's storage channel: it keeps what each PUT
// sent, and answers with status and words.
type channel struct {
	server *httptest.Server

	mu      sync.Mutex
	uploads []sent
	status  int
	words   string
	// delay is how long it takes to answer.
	delay time.Duration
}

// sent is one PUT the channel was sent.
type sent struct {
	// length is the length the PUT named, -1 for one in chunks.
	length      int64
	contentType string
	body        []byte
	// ended says the body ended, its last chunk sent, rather than being cut.
	ended bool
	// leftOpen says the body was neither ended nor cut, but left open for
	// longer than a test waits.
	leftOpen bool
}

// leftOpenAfter is how long the channel waits on a body that stops before it
// gives up, so a stream left open fails its test rather than hanging it.
const leftOpenAfter = 5 * time.Second

// newChannel starts a channel that stores what it is sent, and returns its
// address.
func newChannel(t *testing.T) (string, *channel) {
	t.Helper()

	c := &channel{status: http.StatusOK}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(leftOpenAfter))
		body, err := io.ReadAll(r.Body)

		c.mu.Lock()
		c.uploads = append(c.uploads, sent{
			length:      r.ContentLength,
			contentType: r.Header.Get("Content-Type"),
			body:        body,
			ended:       err == nil,
			leftOpen:    errors.Is(err, os.ErrDeadlineExceeded),
		})
		status, words, delay := c.status, c.words, c.delay
		c.mu.Unlock()

		if err != nil {
			return
		}

		time.Sleep(delay)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, words)
	}))
	t.Cleanup(c.server.Close)

	return c.server.URL + "/output", c
}

// answer makes the channel answer status and words.
func (c *channel) answer(status int, words string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.status, c.words = status, words
}

// slow makes the channel take delay to answer.
func (c *channel) slow(delay time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.delay = delay
}

// settled closes the channel, which waits for the PUTs it is serving, and
// returns every PUT it was sent.
func (c *channel) settled() []sent {
	c.server.Close()

	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]sent(nil), c.uploads...)
}

// one is the one PUT the channel was sent.
func (c *channel) one(t *testing.T) sent {
	t.Helper()

	uploads := c.settled()
	require.Len(t, uploads, 1)

	return uploads[0]
}

// assertCut checks a PUT's body was cut: never ended, and not left open.
func assertCut(t *testing.T, upload sent) {
	t.Helper()

	require.False(t, upload.ended, "an abandoned stream ended its body")
	require.False(t, upload.leftOpen, "a stream was left open rather than abandoned")
}

// storedNothing checks that every PUT the channel was sent was cut.
func (c *channel) storedNothing(t *testing.T) {
	t.Helper()

	for _, upload := range c.settled() {
		assertCut(t, upload)
	}
}

func paddedRows(count int) iter.Seq[any] {
	return func(yield func(any) bool) {
		for index := range count {
			if !yield(map[string]any{"id": index, "pad": strings.Repeat("x", 100)}) {
				return
			}
		}
	}
}

// streaming is an upload to the channel at url that takes a stream.
func streaming(url string) *Upload {
	return &Upload{URL: url, Key: "node-outputs/run/export/0", Stream: true}
}

// assertRows checks body holds count rows in the order they were written.
func assertRows(t *testing.T, body []byte, count int) {
	t.Helper()

	lines := bytes.Split(bytes.TrimSuffix(body, []byte("\n")), []byte("\n"))
	require.Len(t, lines, count)

	for index, line := range lines {
		row, err := DecodeJSON(line)
		require.NoError(t, err)
		require.Equal(t, json.Number(strconv.Itoa(index)), row.(map[string]any)["id"])
	}
}

// Tests that rows past the inline limit, when the upload takes a stream, leave
// as they come in one PUT of no length named, every row in order, which the
// reference names by the key and the bytes sent.
func TestALargeStreamIsOnePutInChunks(t *testing.T) {
	url, c := newChannel(t)

	env, err := ToEnvelope(Result[any]{Output: paddedRows(400), ContentType: NDJSON}, 1024, streaming(url), 512)
	require.NoError(t, err)

	upload := c.one(t)
	require.EqualValues(t, -1, upload.length, "a stream names no length")
	require.True(t, upload.ended)
	require.Equal(t, NDJSON, upload.contentType)
	assertRows(t, upload.body, 400)

	require.Equal(t, REFERENCE, env.Output.Type)
	require.Equal(t, "node-outputs/run/export/0", env.Output.Ref)
	require.EqualValues(t, len(upload.body), env.Output.Size)
}

// Tests that an output that fits inline opens no upload.
func TestASmallOutputNeverOpensAnUpload(t *testing.T) {
	url, c := newChannel(t)

	env, err := ToEnvelope(Result[any]{Output: lazy(map[string]any{"id": 1}), ContentType: NDJSON}, big, streaming(url), 512)
	require.NoError(t, err)
	require.Equal(t, INLINE, env.Output.Type)
	require.Empty(t, c.settled())
}

// Tests that rows, when the upload takes no stream, are held and sent in one
// PUT that names their length.
func TestRowsAreSentWithTheirLengthWhenTheUploadTakesNoStream(t *testing.T) {
	url, c := newChannel(t)
	upload := streaming(url)
	upload.Stream = false

	env, err := ToEnvelope(Result[any]{Output: paddedRows(400), ContentType: NDJSON}, 1024, upload, 512)
	require.NoError(t, err)

	got := c.one(t)
	require.EqualValues(t, len(got.body), got.length)
	assertRows(t, got.body, 400)
	require.Equal(t, REFERENCE, env.Output.Type)
}

// Tests that a node that fails while it streams abandons the upload: its body,
// sent in part, never ends, so the channel stores none of it.
func TestAFailureMidStreamAbandonsTheUpload(t *testing.T) {
	url, c := newChannel(t)

	explodes := func(yield func(any, error) bool) {
		for index := range 2000 {
			if index == 1500 {
				yield(nil, errors.New("the node failed halfway"))
				return
			}

			if !yield(map[string]any{"id": index, "pad": strings.Repeat("x", 100)}, nil) {
				return
			}
		}
	}

	_, err := ToEnvelope(Result[any]{Output: iter.Seq2[any, error](explodes), ContentType: NDJSON}, 1024, streaming(url), 512)
	require.EqualError(t, err, "the node failed halfway")
	assertCut(t, c.one(t))
}

// Tests that a node that panics while it streams abandons the upload too.
func TestAPanicMidStreamAbandonsTheUploadToo(t *testing.T) {
	url, c := newChannel(t)

	explodes := func(yield func(any, error) bool) {
		for index := range 2000 {
			if index == 1500 {
				panic("the node panicked halfway")
			}

			if !yield(map[string]any{"id": index, "pad": strings.Repeat("x", 100)}, nil) {
				return
			}
		}
	}

	require.PanicsWithValue(t, "the node panicked halfway", func() {
		_, _ = ToEnvelope(Result[any]{Output: iter.Seq2[any, error](explodes), ContentType: NDJSON}, 1024, streaming(url), 512)
	})
	assertCut(t, c.one(t))
}

// channelCtx is a context whose upload is the channel at url, taking a stream.
func channelCtx(url string) *Ctx {
	return CtxFromRaw(map[string]any{
		"inline_max_bytes":     float64(1024),
		"memory_mb":            float64(512),
		"output_upload_url":    url,
		"output_key":           "k",
		"output_upload_stream": true,
	})
}

// Tests that the writer streams the same way.
func TestTheWriterStreamsTheSameWay(t *testing.T) {
	url, c := newChannel(t)
	out := channelCtx(url).OutputStream(NDJSON)
	defer out.Abort()

	for row := range paddedRows(400) {
		require.NoError(t, out.Write(row))
	}

	block := envelope(t, Result[any]{Output: closedRef(t, out), Next: []string{"done"}}, 1024).Output

	upload := c.one(t)
	require.True(t, upload.ended)
	assertRows(t, upload.body, 400)
	require.Equal(t, REFERENCE, block.Type)
	require.Equal(t, "k", block.Ref)
}

// Tests that a writer abandoned before it closes abandons its upload too.
func TestAWriterAbandonedAbandonsItsUpload(t *testing.T) {
	url, c := newChannel(t)

	func() {
		out := channelCtx(url).OutputStream(NDJSON)
		defer out.Abort()

		for row := range paddedRows(2000) {
			require.NoError(t, out.Write(row))
		}
	}()

	assertCut(t, c.one(t))
}

// Tests that a stream abandoned before it sent anything stores nothing.
func TestAStreamAbandonedBeforeItSentAnythingStoresNothing(t *testing.T) {
	url, c := newChannel(t)
	stream := OpenStream(url, NDJSON, 0)

	require.NoError(t, stream.Write([]byte(`{"id":1}`)))
	stream.Abort()

	_, err := stream.Finish()
	require.Error(t, err, "an abandoned stream cannot be finished")
	c.storedNothing(t)
}

// Tests that an output the channel refuses as over what it may store is the
// node's OutputTooLarge, in the channel's words, streamed or with its length.
func TestAnOutputTheChannelRefusesAsTooLargeSaysWhy(t *testing.T) {
	const words = "the node's output passed the 4096 bytes it may store"

	for _, stream := range []bool{true, false} {
		url, c := newChannel(t)
		c.answer(http.StatusRequestEntityTooLarge, words)

		upload := streaming(url)
		upload.Stream = stream

		_, err := ToEnvelope(Result[any]{Output: paddedRows(400), ContentType: NDJSON}, 1024, upload, 512)
		require.True(t, errors.Is(err, &OutputTooLarge{}), "stream %t: %v", stream, err)
		require.EqualError(t, err, words)
	}
}

// Tests that an upload the channel fails is a retryable infrastructure
// failure that says what the channel answered.
func TestAnUploadTheChannelFailsIsRetryable(t *testing.T) {
	for _, stream := range []bool{true, false} {
		url, c := newChannel(t)
		c.answer(http.StatusBadGateway, "the storage failed the upload: HTTP 503")

		upload := streaming(url)
		upload.Stream = stream

		_, err := ToEnvelope(Result[any]{Output: paddedRows(400), ContentType: NDJSON}, 1024, upload, 512)

		fail, ok := errors.AsType[*Fail](err)
		require.True(t, ok, "stream %t: %v", stream, err)
		require.Equal(t, INFRASTRUCTURE, fail.Category)
		require.False(t, *fail.Abort)
		require.Contains(t, fail.Message, "HTTP 502 Bad Gateway: the storage failed the upload: HTTP 503")
	}
}

// Tests that an upload waits for the channel however long it takes to answer,
// past the idle timeout reads are held to: the channel bounds its own
// transfers to the storage.
func TestAnUploadWaitsForTheChannelHoweverLongItTakes(t *testing.T) {
	withTimeout(t, 100*time.Millisecond)

	for _, stream := range []bool{true, false} {
		url, c := newChannel(t)
		c.slow(300 * time.Millisecond)

		upload := streaming(url)
		upload.Stream = stream

		env, err := ToEnvelope(Result[any]{Output: paddedRows(400), ContentType: NDJSON}, 1024, upload, 512)
		require.NoError(t, err, "stream %t", stream)
		require.Equal(t, REFERENCE, env.Output.Type)
	}
}
