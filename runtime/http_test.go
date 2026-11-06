package runtime

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type received struct {
	mu          sync.Mutex
	body        []byte
	length      string
	contentType string
}

func (r *received) snapshot() received {
	r.mu.Lock()
	defer r.mu.Unlock()

	return received{
		body:        r.body,
		length:      r.length,
		contentType: r.contentType,
	}
}

// newStore sets up a test server returning GET payloads and capturing PUT data.
func newStore(t *testing.T, body []byte) (string, *received) {
	t.Helper()

	got := &received{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/boom" {
			http.Error(w, "no", http.StatusInternalServerError)
			return
		}

		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", NDJSON)
			w.Write(body)

		case http.MethodPut:
			raw, _ := io.ReadAll(r.Body)

			got.mu.Lock()
			got.body = raw
			got.length = r.Header.Get("Content-Length")
			got.contentType = r.Header.Get("Content-Type")
			got.mu.Unlock()
		}
	}))

	t.Cleanup(server.Close)

	return server.URL, got
}

func readAll(t *testing.T, rawURL string, chunk int) ([][]byte, error) {
	t.Helper()

	body, err := stream(rawURL, 0)
	if err != nil {
		return nil, err
	}

	defer body.Close()

	var chunks [][]byte

	for {
		buf := make([]byte, chunk)

		n, err := body.Read(buf)
		if n > 0 {
			chunks = append(chunks, buf[:n])
		}

		if err == io.EOF {
			return chunks, nil
		}

		if err != nil {
			return chunks, err
		}
	}
}

func TestABodyArrivesWholeAndInPieces(t *testing.T) {
	body := []byte(strings.Repeat("x", 200_000))
	base, _ := newStore(t, body)

	chunks, err := readAll(t, base+"/data", 4096)
	require.NoError(t, err)
	require.Greater(t, len(chunks), 1, "a large body must not arrive in one piece")
	require.Equal(t, body, joined(chunks))
}

// readSized reads the input at rawURL whole, held to size.
func readSized(t *testing.T, rawURL string, size int64) ([]byte, error) {
	t.Helper()

	body, err := stream(rawURL, size)
	if err != nil {
		return nil, err
	}

	defer body.Close()

	return io.ReadAll(body)
}

// Tests that an input whose body ends short of the size recorded for it, or
// runs past it, fails as the platform's, to be retried, without its link's
// signature, and that one of its size, or of no size recorded, reads whole.
func TestAnInputIsReadAgainstTheSizeRecordedForIt(t *testing.T) {
	body := []byte(strings.Repeat("x", 1000))
	base, _ := newStore(t, body)

	for name, c := range map[string]struct {
		size int64
		want string
	}{
		"short of its size": {1500, "it ended after 1000 of its 1500 bytes"},
		"past its size":     {500, "it ran past the 500 bytes recorded for it"},
	} {
		_, err := readSized(t, base+"/data?X-Amz-Signature=deadbeef", c.size)

		var failure *Fail

		require.ErrorAs(t, err, &failure, name)
		require.Equal(t, INFRASTRUCTURE, failure.Category, name)
		require.False(t, *failure.Abort, name)
		require.Contains(t, failure.Message, c.want, name)
		require.NotContains(t, failure.Message, "deadbeef", name)
	}

	for _, size := range []int64{1000, 0} {
		got, err := readSized(t, base+"/data", size)
		require.NoError(t, err, "size %d", size)
		require.Equal(t, body, got, "size %d", size)
	}
}

// Tests that an input cut the way the storage channel cuts a read, chunked and
// with no last chunk, fails as the platform's, to be retried.
func TestAnInputCutBeforeItsLastChunkFailsAsThePlatforms(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, strings.Repeat("x", 50))
		_ = http.NewResponseController(w).Flush()

		panic(http.ErrAbortHandler)
	}))
	t.Cleanup(server.Close)

	_, err := readSized(t, server.URL+"/obj", 0)

	var failure *Fail

	require.ErrorAs(t, err, &failure)
	require.Equal(t, INFRASTRUCTURE, failure.Category)
	require.False(t, *failure.Abort)
}

// Tests that a stored input is read against the size recorded for it, whether
// its rows are iterated or its bytes read.
func TestAStoredInputIsReadAgainstItsRecordedSize(t *testing.T) {
	body := []byte("{\"a\": 1}\n{\"a\": 2}\n")
	base, _ := newStore(t, body)

	entry := func(size int) map[string]any {
		return map[string]any{"type": REFERENCE, "url": base + "/rows", "size": size, "content_type": NDJSON}
	}

	whole := mustGet(t, inputsOf(map[string]any{"rows": entry(len(body))}), "rows")

	var rows []any

	for row, err := range whole.Iter() {
		require.NoError(t, err)

		rows = append(rows, row)
	}

	require.Len(t, rows, 2)

	short := mustGet(t, inputsOf(map[string]any{"rows": entry(len(body) + 1)}), "rows")

	var failed error

	for _, err := range short.Iter() {
		if err != nil {
			failed = err
		}
	}

	require.ErrorContains(t, failed, "it ended after 18 of its 19 bytes")

	reader, err := short.Bytes()
	require.NoError(t, err)

	_, err = io.ReadAll(reader)
	reader.Close()
	require.ErrorContains(t, err, "it ended after 18 of its 19 bytes")
}

func joined(chunks [][]byte) []byte {
	var out []byte

	for _, chunk := range chunks {
		out = append(out, chunk...)
	}

	return out
}

func TestAFailedReadIsRetryableOnTheWorkflowPolicy(t *testing.T) {
	base, _ := newStore(t, nil)

	_, err := stream(base+"/boom", 0)
	fail, ok := errors.AsType[*Fail](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, INFRASTRUCTURE, fail.Category)
	require.NotNil(t, fail.Abort)
	require.False(t, *fail.Abort)
	require.Zero(t, fail.RetryAfterMs)
}

func TestAFailureNeverLogsTheSignature(t *testing.T) {
	base, _ := newStore(t, nil)

	_, err := stream(base+"/boom?X-Amz-Signature=deadbeef&X-Amz-Expires=900", 0)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "deadbeef")
	require.Contains(t, err.Error(), "boom")

	// A dial failure is wrapped by net/http with the full URL inside it.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	closed := "http://" + listener.Addr().String()
	listener.Close()

	_, err = stream(closed+"/obj?X-Amz-Signature=deadbeef", 0)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "deadbeef")
	require.Contains(t, err.Error(), "/obj")

	err = store(closed+"/obj?X-Amz-Signature=deadbeef", []byte("{}"), JSON)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "deadbeef")
}

func TestAnUploadCarriesItsLengthAndContentType(t *testing.T) {
	base, got := newStore(t, nil)
	body := []byte(`{"value":42}`)

	err := store(base+"/out", body, JSON)
	require.NoError(t, err)

	seen := got.snapshot()
	require.Equal(t, body, seen.body)
	require.Equal(t, "12", seen.length, "a value names its length")
	require.Equal(t, JSON, seen.contentType)
}

func TestAFailedUploadIsReportedAsInfrastructure(t *testing.T) {
	base, _ := newStore(t, nil)

	err := store(base+"/boom", []byte("{}"), JSON)
	fail, ok := errors.AsType[*Fail](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, INFRASTRUCTURE, fail.Category)
	require.False(t, *fail.Abort)
	require.Contains(t, fail.Message, "output")
}

func withTimeout(t *testing.T, d time.Duration) {
	t.Helper()

	was := httpTimeout
	httpTimeout = d

	t.Cleanup(func() {
		httpTimeout = was
	})
}

func TestAStalledBodyTimesOutAsInfrastructure(t *testing.T) {
	withTimeout(t, 150*time.Millisecond)

	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("first\n"))
		w.(http.Flusher).Flush()
		<-release
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})

	_, err := readAll(t, server.URL+"/slow?X-Amz-Signature=deadbeef", 1024)
	fail, ok := errors.AsType[*Fail](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, INFRASTRUCTURE, fail.Category)
	require.Contains(t, fail.Message, "no data for 150ms")
	require.NotContains(t, fail.Message, "deadbeef")
}

func TestABodyThatKeepsMovingIsNeverCutByTheTimeout(t *testing.T) {
	withTimeout(t, 100*time.Millisecond)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for range 8 {
			w.Write([]byte("tick\n"))
			w.(http.Flusher).Flush()
			time.Sleep(50 * time.Millisecond)
		}
	}))
	t.Cleanup(server.Close)

	chunks, err := readAll(t, server.URL+"/drip", 1024)
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("tick\n", 8), string(joined(chunks)))
}

func TestAServerThatNeverAnswersTimesOut(t *testing.T) {
	withTimeout(t, 150*time.Millisecond)

	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})

	_, err := stream(server.URL+"/never", 0)
	fail, ok := errors.AsType[*Fail](err)
	require.True(t, ok, "%v", err)
	require.Contains(t, fail.Message, "no data for 150ms")
}

func TestAnErrorStatusCarriesTheProvidersDiagnosisWithTheSignatureScrubbed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("<Error><Code>SignatureDoesNotMatch</Code><SignatureProvided>deadbeefcafe</SignatureProvided></Error>"))
	}))
	t.Cleanup(server.Close)

	err := store(server.URL+"/obj?X-Amz-Signature=deadbeefcafe&X-Amz-Expires=900", []byte("{}"), JSON)
	require.Error(t, err)
	require.Contains(t, err.Error(), "HTTP 403 Forbidden: <Error><Code>SignatureDoesNotMatch</Code>")
	require.NotContains(t, err.Error(), "deadbeefcafe")
	require.Contains(t, err.Error(), "[scrubbed]")

	_, err = stream(server.URL+"/obj", 0)
	require.ErrorContains(t, err, "SignatureDoesNotMatch")
}

func TestAnErrorBodyIsBoundedInTheMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(strings.Repeat("y", 10_000)))
	}))
	t.Cleanup(server.Close)

	_, err := stream(server.URL+"/obj", 0)
	require.Error(t, err)
	require.Less(t, len(err.Error()), 2048+128)
}
