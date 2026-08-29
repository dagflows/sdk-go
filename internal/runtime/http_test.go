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

	body, err := stream(rawURL)
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

func joined(chunks [][]byte) []byte {
	var out []byte

	for _, chunk := range chunks {
		out = append(out, chunk...)
	}

	return out
}

func TestAFailedReadIsRetryableOnTheWorkflowPolicy(t *testing.T) {
	base, _ := newStore(t, nil)

	_, err := stream(base + "/boom")
	fail, ok := errors.AsType[*Fail](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, INFRASTRUCTURE, fail.Category)
	require.NotNil(t, fail.Abort)
	require.False(t, *fail.Abort)
	require.Zero(t, fail.RetryAfterMs)
}

func TestAFailureNeverLogsTheSignature(t *testing.T) {
	base, _ := newStore(t, nil)

	_, err := stream(base + "/boom?X-Amz-Signature=deadbeef&X-Amz-Expires=900")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "deadbeef")
	require.Contains(t, err.Error(), "boom")

	// A dial failure is wrapped by net/http with the full URL inside it.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	closed := "http://" + listener.Addr().String()
	listener.Close()

	_, err = stream(closed + "/obj?X-Amz-Signature=deadbeef")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "deadbeef")
	require.Contains(t, err.Error(), "/obj")

	_, err = put(closed+"/obj?X-Amz-Signature=deadbeef", []byte("{}"), JSON)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "deadbeef")
}

func TestAPutCarriesItsLengthAndContentType(t *testing.T) {
	base, got := newStore(t, nil)
	body := []byte(`{"value":42}`)

	_, err := put(base+"/out", body, JSON)
	require.NoError(t, err)

	seen := got.snapshot()
	require.Equal(t, body, seen.body)
	require.Equal(t, "12", seen.length, "S3 and R2 refuse a chunked presigned PUT")
	require.Equal(t, JSON, seen.contentType)
}

func TestAPutReturnsTheETag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"abc"`)
	}))
	t.Cleanup(server.Close)

	etag, err := put(server.URL+"/part", []byte("x"), BYTES)
	require.NoError(t, err)
	require.Equal(t, `"abc"`, etag)
}

func TestAFailedUploadIsReportedAsInfrastructure(t *testing.T) {
	base, _ := newStore(t, nil)

	_, err := put(base+"/boom", []byte("{}"), JSON)
	fail, ok := errors.AsType[*Fail](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, INFRASTRUCTURE, fail.Category)
	require.False(t, *fail.Abort)
	require.Contains(t, fail.Message, "output")
}

func TestPostReturnsTheResponseAndDeleteNeverFails(t *testing.T) {
	var deleted bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			w.Write(append([]byte("echo:"), raw...))

		case http.MethodDelete:
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(server.Close)

	answer, err := post(server.URL+"/complete", []byte("<x/>"), "application/xml")
	require.NoError(t, err)
	require.Equal(t, "echo:<x/>", string(answer))

	del(server.URL + "/abort")
	require.True(t, deleted)

	del("http://127.0.0.1:1/nowhere")
	del("::not a url::")
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

	_, err := stream(server.URL + "/never")
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

	_, err := put(server.URL+"/obj?X-Amz-Signature=deadbeefcafe&X-Amz-Expires=900", []byte("{}"), JSON)
	require.Error(t, err)
	require.Contains(t, err.Error(), "HTTP 403 Forbidden: <Error><Code>SignatureDoesNotMatch</Code>")
	require.NotContains(t, err.Error(), "deadbeefcafe")
	require.Contains(t, err.Error(), "[scrubbed]")

	_, err = stream(server.URL + "/obj")
	require.ErrorContains(t, err, "SignatureDoesNotMatch")
}

func TestAnErrorBodyIsBoundedInTheMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(strings.Repeat("y", 10_000)))
	}))
	t.Cleanup(server.Close)

	_, err := stream(server.URL + "/obj")
	require.Error(t, err)
	require.Less(t, len(err.Error()), 2048+128)
}
