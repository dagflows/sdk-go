package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

const partSize = 5 * 1024

type partStore struct {
	mu        sync.Mutex
	parts     map[int][]byte
	completed []byte
	aborted   bool
	noETag    bool
}

func newPartStore(t *testing.T) (string, *partStore) {
	t.Helper()

	store := &partStore{
		parts: map[int][]byte{},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		defer store.mu.Unlock()

		switch r.Method {
		case http.MethodPut:
			number, _ := strconv.Atoi(r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
			store.parts[number], _ = io.ReadAll(r.Body)

			if !store.noETag {
				w.Header().Set("ETag", fmt.Sprintf(`"etag-%d"`, number))
			}

		case http.MethodPost:
			store.completed, _ = io.ReadAll(r.Body)

		case http.MethodDelete:
			store.aborted = true
			w.WriteHeader(http.StatusNoContent)
		}
	}))

	t.Cleanup(server.Close)

	return server.URL, store
}

func uploadOf(base string, parts int) *Multipart {
	urls := make([]string, 0, parts)

	for n := 1; n <= parts; n++ {
		urls = append(urls, fmt.Sprintf("%s/part/%d", base, n))
	}

	return &Multipart{
		UploadID:    "upload-1",
		PartSize:    partSize,
		PartURLs:    urls,
		CompleteURL: base + "/complete",
		AbortURL:    base + "/abort",
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

func (s *partStore) stored() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []byte

	for _, number := range slices.Sorted(maps.Keys(s.parts)) {
		out = append(out, s.parts[number]...)
	}

	return out
}

func tier3(t *testing.T, base string, output any, parts int) (*Envelope, error) {
	t.Helper()

	return ToEnvelope(
		Result{
			Output:      output,
			ContentType: NDJSON,
		},
		1024,
		&Upload{
			URL: base + "/put",
			Key: "node-outputs/run/export/0",
		},
		512,
		uploadOf(base, parts),
	)
}

func TestALargeStreamIsUploadedInParts(t *testing.T) {
	base, store := newPartStore(t)
	env, err := tier3(t, base, paddedRows(400), 20)
	require.NoError(t, err)

	block := env.Output
	require.Equal(t, REFERENCE, block.Type)
	require.Equal(t, "node-outputs/run/export/0", block.Ref)
	require.Greater(t, len(store.parts), 1, "a large output must arrive in more than one part")

	numbers := slices.Sorted(maps.Keys(store.parts))
	for _, number := range numbers[:len(numbers)-1] {
		require.Len(t, store.parts[number], partSize, "part %d is short", number)
	}

	last := len(store.parts[numbers[len(numbers)-1]])
	require.Greater(t, last, 0)
	require.LessOrEqual(t, last, partSize)
	require.Equal(t, int64(len(store.stored())), block.Size)
}

func TestThePartsReassembleIntoWhatTheNodeWrote(t *testing.T) {
	base, store := newPartStore(t)
	_, err := tier3(t, base, paddedRows(400), 20)
	require.NoError(t, err)

	lines := bytes.Split(bytes.TrimSuffix(store.stored(), []byte("\n")), []byte("\n"))
	require.Len(t, lines, 400)

	for index, line := range lines {
		row, err := DecodeJSON(line)
		require.NoError(t, err)
		require.Equal(t, json.Number(strconv.Itoa(index)), row.(map[string]any)["id"])
	}
}

func TestCompletionNamesEveryPartWithItsOwnETag(t *testing.T) {
	base, store := newPartStore(t)
	_, err := tier3(t, base, paddedRows(400), 20)
	require.NoError(t, err)

	document := string(store.completed)
	require.True(t, strings.HasPrefix(document, "<CompleteMultipartUpload>"))

	for number := range store.parts {
		require.Contains(t, document, fmt.Sprintf("<PartNumber>%d</PartNumber>", number))
		require.Contains(t, document, fmt.Sprintf(`<ETag>"etag-%d"</ETag>`, number))
	}
}

func TestASmallOutputNeverOpensAnUpload(t *testing.T) {
	base, store := newPartStore(t)
	env, err := ToEnvelope(
		Result{
			Output:      lazy(map[string]any{"id": 1}),
			ContentType: NDJSON,
		},
		big,
		&Upload{
			URL: base + "/put",
			Key: "k",
		},
		512,
		uploadOf(base, 20),
	)

	require.NoError(t, err)
	require.Equal(t, INLINE, env.Output.Type)
	require.Empty(t, store.parts, "an upload was opened for an output that fits inline")
}

func TestRunningOutOfPartsNamesTheRealCeiling(t *testing.T) {
	base, store := newPartStore(t)
	_, err := tier3(t, base, paddedRows(400), 2)
	require.True(t, errors.Is(err, &OutputTooLarge{}))
	require.ErrorContains(t, err, strconv.Itoa(2*partSize))
	require.ErrorContains(t, err, "max_output_mb")
	require.True(t, store.aborted, "a failed upload must be aborted")
}

func TestAPartWithoutAnETagIsAStorageMisconfiguration(t *testing.T) {
	base, store := newPartStore(t)
	store.noETag = true

	_, err := tier3(t, base, paddedRows(400), 20)
	require.True(t, errors.Is(err, &OutputTooLarge{}))
	require.ErrorContains(t, err, "returned no ETag")
	require.ErrorContains(t, err, "storage misconfiguration")
	require.Nil(t, store.completed)
}

func TestAFailureMidStreamAbortsTheUpload(t *testing.T) {
	base, store := newPartStore(t)

	explodes := func(yield func(any, error) bool) {
		for index := range 400 {
			if index == 300 {
				yield(nil, errors.New("the node failed halfway"))
				return
			}

			if !yield(map[string]any{"id": index, "pad": strings.Repeat("x", 100)}, nil) {
				return
			}
		}
	}

	_, err := tier3(t, base, iter.Seq2[any, error](explodes), 20)
	require.EqualError(t, err, "the node failed halfway")
	require.True(t, store.aborted, "a partial upload must not be left behind")
	require.Nil(t, store.completed, "a failed node must not complete its upload")
}

func multipartCtx(base string) *Ctx {
	urls := make([]any, 0, 20)

	for n := 1; n <= 20; n++ {
		urls = append(urls, fmt.Sprintf("%s/part/%d", base, n))
	}

	return &Ctx{
		InlineMaxBytes:  1024,
		OutputUploadURL: base + "/put",
		OutputKey:       "k",
		MemoryLimitMB:   512,
		Raw: map[string]any{
			"output_multipart": map[string]any{
				"upload_id":    "upload-1",
				"part_size":    partSize,
				"part_urls":    urls,
				"complete_url": base + "/complete",
				"abort_url":    base + "/abort",
			},
		},
	}
}

func TestTheWriterStreamsTheSameWay(t *testing.T) {
	base, store := newPartStore(t)
	out := multipartCtx(base).OutputStream(NDJSON)
	defer out.Abort()

	count := 0

	for row := range paddedRows(400) {
		require.NoError(t, out.Write(row))
		count++
	}

	block := envelope(t, Result{
		Output: closedRef(t, out),
		Next:   []string{"done"},
	}, 1024).Output

	require.Equal(t, 400, count)
	require.Equal(t, REFERENCE, block.Type)
	require.Equal(t, "k", block.Ref)
	require.Greater(t, len(store.parts), 1)
	require.NotNil(t, store.completed)
}

func TestAWriterThatFailsAbortsToo(t *testing.T) {
	base, store := newPartStore(t)

	func() {
		out := multipartCtx(base).OutputStream(NDJSON)
		defer out.Abort()

		for row := range paddedRows(400) {
			require.NoError(t, out.Write(row))
		}
	}()

	require.True(t, store.aborted)
	require.Nil(t, store.completed)
}

func TestAnEmptyUploadIsAbortedNotCompleted(t *testing.T) {
	base, store := newPartStore(t)
	uploader := NewPartUploader(uploadOf(base, 2), NDJSON)

	size, err := uploader.Finish()
	require.NoError(t, err)
	require.Zero(t, size)
	require.True(t, store.aborted)
	require.Nil(t, store.completed)
	require.Zero(t, uploader.PartsSent())
	require.Zero(t, uploader.BytesSent())
}

func TestCompletionEscapesTheETagAsText(t *testing.T) {
	uploader := &PartUploader{
		etags: []string{`"a<b>&c"`},
	}

	require.Equal(
		t,
		`<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>"a&lt;b&gt;&amp;c"</ETag></Part></CompleteMultipartUpload>`,
		string(uploader.completion()),
	)
}

func TestAPanicMidStreamAbortsTheUploadToo(t *testing.T) {
	base, store := newPartStore(t)

	explodes := func(yield func(any, error) bool) {
		for index := range 400 {
			if index == 300 {
				panic("the node panicked halfway")
			}

			if !yield(map[string]any{"id": index, "pad": strings.Repeat("x", 100)}, nil) {
				return
			}
		}
	}

	require.PanicsWithValue(t, "the node panicked halfway", func() {
		_, _ = tier3(t, base, iter.Seq2[any, error](explodes), 20)
	})
	require.Greater(t, len(store.parts), 1, "the fixture must have spilled before the panic")
	require.True(t, store.aborted, "a partial upload must not be left behind")
	require.Nil(t, store.completed)
}
