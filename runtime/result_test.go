package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const big = 1 << 20

func envelope(t *testing.T, value any, inlineMaxBytes int) *Envelope {
	t.Helper()

	out, err := ToEnvelope(value, inlineMaxBytes, nil, 0, nil)
	require.NoError(t, err)

	return out
}

func wire(t *testing.T, env *Envelope) map[string]any {
	t.Helper()

	raw, err := compact(env)
	require.NoError(t, err)

	parsed, err := DecodeJSON(raw)
	require.NoError(t, err)

	return parsed.(map[string]any)
}

func lazy(items ...any) iter.Seq[any] {
	return func(yield func(any) bool) {
		for _, item := range items {
			if !yield(item) {
				return
			}
		}
	}
}

func TestABareValueIsSugarForAResult(t *testing.T) {
	env := envelope(t, map[string]any{"count": 12}, big)
	require.Equal(t, "SUCCESS", env.Status)
	require.Equal(t, &Block{
		Type:        INLINE,
		ContentType: JSON,
		Data:        map[string]any{"count": 12},
	}, env.Output)

	out := wire(t, env)
	require.NotContains(t, out, "next")
	require.NotContains(t, out, "stop")
	require.Equal(t, map[string]any{
		"type":         "INLINE",
		"content_type": JSON,
		"data":         map[string]any{"count": json.Number("12")},
	}, out["output"])
}

func TestAMapShapedLikeAnEnvelopeIsStillData(t *testing.T) {
	data := map[string]any{
		"output": "x",
		"next":   []any{"notify"},
		"stop":   true,
	}

	env := envelope(t, data, big)
	require.Equal(t, data, env.Output.Data)
	require.Empty(t, env.Next)
	require.False(t, env.Stop)
}

func TestRoutingIsCarriedAsAList(t *testing.T) {
	require.Equal(t, []string{"notify"}, envelope(t, Result{Output: map[string]any{}, Next: []string{"notify"}}, big).Next)
	require.Equal(t, []string{"a", "b"}, envelope(t, &Result{Output: map[string]any{}, Next: []string{"a", "b"}}, big).Next)
}

func TestAbsentRoutingMeansEveryChild(t *testing.T) {
	require.NotContains(t, wire(t, envelope(t, Result{Output: map[string]any{}}, big)), "next")

	next, err := normaliseNext(nil)
	require.NoError(t, err)
	require.Empty(t, next)
}

func TestABlankRoutingKeyIsRefused(t *testing.T) {
	_, err := ToEnvelope(Result{Output: map[string]any{}, Next: []string{""}}, big, nil, 0, nil)
	require.ErrorContains(t, err, "Stop: true")

	_, err = ToEnvelope(Result{Output: map[string]any{}, Next: []string{"ok", "  "}}, big, nil, 0, nil)
	require.ErrorContains(t, err, "Stop: true")
}

func TestStopIsCarriedAndNeverInferred(t *testing.T) {
	require.Equal(t, true, wire(t, envelope(t, Result{Output: map[string]any{}, Stop: true}, big))["stop"])
	require.NotContains(t, wire(t, envelope(t, Result{Output: map[string]any{}, Next: []string{}}, big)), "stop")
}

func TestAContainerIsAValueAndALazyIteratorIsRows(t *testing.T) {
	listed := envelope(t, []any{1, 2, 3}, big).Output
	require.Equal(t, JSON, listed.ContentType)
	require.Equal(t, []any{1, 2, 3}, listed.Data)

	streamed := envelope(t, lazy(1, 2, 3), big).Output
	require.Equal(t, NDJSON, streamed.ContentType)
	require.Equal(t, []any{1, 2, 3}, streamed.Data)
}

func TestATypedIteratorIsRowsToo(t *testing.T) {
	type row struct {
		ID int `json:"id"`
	}

	typed := func(yield func(row) bool) {
		for i := range 2 {
			if !yield(row{ID: i}) {
				return
			}
		}
	}

	out := envelope(t, typed, big).Output
	require.Equal(t, NDJSON, out.ContentType)
	require.Equal(t, []any{row{0}, row{1}}, out.Data)

	withErr := func(yield func(row, error) bool) {
		yield(row{7}, nil)
	}

	require.Equal(t, []any{row{7}}, envelope(t, withErr, big).Output.Data)
}

func TestATypedIteratorErrorFailsTheNode(t *testing.T) {
	boom := errors.New("the source broke")

	failing := func(yield func(map[string]any, error) bool) {
		if !yield(map[string]any{"id": 1}, nil) {
			return
		}

		yield(nil, boom)
	}

	_, err := ToEnvelope(failing, big, nil, 0, nil)
	require.ErrorIs(t, err, boom)
}

func TestAnInlineSequenceTravelsAsAJSONArray(t *testing.T) {
	out := envelope(t, Result{Output: []any{map[string]any{"id": 1}}, ContentType: NDJSON}, big).Output
	require.Equal(t, NDJSON, out.ContentType)
	require.Equal(t, []any{map[string]any{"id": 1}}, out.Data)
}

func TestAStatedRowTypeMakesAListRows(t *testing.T) {
	out := envelope(t, Result{Output: []map[string]any{{"a": 1}}, ContentType: CSV}, big).Output
	require.Equal(t, CSV, out.ContentType)
	require.Equal(t, []any{map[string]any{"a": 1}}, out.Data)
}

func TestASingleValueWithARowTypeIsOneRow(t *testing.T) {
	out := envelope(t, Result{Output: map[string]any{"a": 1}, ContentType: NDJSON}, big).Output
	require.Equal(t, []any{map[string]any{"a": 1}}, out.Data)
}

func TestALazyIteratorCannotBeAJSONValue(t *testing.T) {
	_, err := ToEnvelope(Result{Output: lazy(1, 2), ContentType: JSON}, big, nil, 0, nil)
	require.ErrorContains(t, err, "materialise it with slices.Collect")
}

func TestTextTravelsAsAString(t *testing.T) {
	require.Equal(t, "42", envelope(t, Result{Output: 42, ContentType: TEXT}, big).Output.Data)
}

func TestRawBytesMustBeUploaded(t *testing.T) {
	_, err := ToEnvelope([]byte{0, 1}, big, nil, 0, nil)
	require.True(t, errors.Is(err, &OutputTooLarge{}))
	require.ErrorContains(t, err, "cannot travel in the JSON envelope")

	_, err = ToEnvelope(Result{Output: []byte{0, 1}, ContentType: NDJSON}, big, nil, 0, nil)
	require.ErrorContains(t, err, "cannot travel in the JSON envelope")
}

func TestNoOutputIsAnEmptyObject(t *testing.T) {
	require.Equal(t, map[string]any{}, envelope(t, nil, big).Output.Data)
	require.Equal(t, map[string]any{}, envelope(t, (*Result)(nil), big).Output.Data)
	require.Equal(
		t,
		`{"status":"SUCCESS","output":{"type":"INLINE","content_type":"application/json","data":{}}}`,
		string(must(compact(envelope(t, Result{}, big)))),
	)
}

func TestZeroValuedDataIsStillCarried(t *testing.T) {
	for _, value := range []any{0, "", false} {
		out := wire(t, envelope(t, value, big))["output"].(map[string]any)
		require.Contains(t, out, "data", "%v", value)
	}
}

func TestCrossingTheThresholdNamesBothNumbers(t *testing.T) {
	_, err := ToEnvelope(map[string]any{"blob": strings.Repeat("x", 200)}, 64, nil, 0, nil)
	require.True(t, errors.Is(err, &OutputTooLarge{}))
	require.ErrorContains(t, err, "64")
	require.ErrorContains(t, err, "nowhere to upload it")
}

func TestRowsStopBeingCollectedOnceTheyCannotBeInline(t *testing.T) {
	consumed := 0

	rows := func(yield func(any) bool) {
		for index := range 1_000_000 {
			consumed++

			if !yield(map[string]any{"index": index, "pad": strings.Repeat("x", 100)}) {
				return
			}
		}
	}

	_, err := ToEnvelope(iter.Seq[any](rows), 1024, nil, 0, nil)
	require.ErrorContains(t, err, "after")
	require.Less(t, consumed, 100, "consumed %d rows past a 1KB limit", consumed)
}

func TestTheThresholdIsNeverHardcoded(t *testing.T) {
	payload := map[string]any{"blob": strings.Repeat("x", 200)}
	require.Equal(t, payload, envelope(t, payload, 4096).Output.Data)

	_, err := ToEnvelope(payload, 64, nil, 0, nil)
	require.True(t, errors.Is(err, &OutputTooLarge{}))
}

func TestAMissingLimitIsTheDefaultNotUnlimited(t *testing.T) {
	consumed := 0

	endless := func(yield func(any) bool) {
		for {
			consumed++

			if !yield(map[string]any{"pad": strings.Repeat("x", 100)}) {
				return
			}
		}
	}

	_, err := ToEnvelope(iter.Seq[any](endless), 0, nil, 0, nil)
	require.True(t, errors.Is(err, &OutputTooLarge{}))
	require.Less(t, consumed, DefaultInlineMaxBytes/100)
}

func TestSizeIsMeasuredInBytesNotCharacters(t *testing.T) {
	payload := map[string]any{"text": strings.Repeat("日本語", 50)}
	exact := len(must(compact(payload)))
	require.Greater(t, exact, len(`{"text":""}`)+150, "non-ASCII must count its bytes")

	require.Equal(t, payload, envelope(t, payload, exact).Output.Data)

	_, err := ToEnvelope(payload, exact-1, nil, 0, nil)
	require.True(t, errors.Is(err, &OutputTooLarge{}))
}

func TestRowCollectionCountsTheArrayPunctuation(t *testing.T) {
	rows := []any{map[string]any{"a": 1}, map[string]any{"a": 2}, map[string]any{"a": 3}}
	exact := len(must(compact(rows)))

	require.Equal(t, rows, envelope(t, lazy(rows...), exact).Output.Data)

	_, err := ToEnvelope(lazy(rows...), exact-1, nil, 0, nil)
	require.True(t, errors.Is(err, &OutputTooLarge{}))
	require.ErrorContains(t, err, "after 3 rows")
}

func TestEnvelopeKeysAreInWireOrder(t *testing.T) {
	env := envelope(t, Result{Output: map[string]any{"n": 1}, Next: []string{"a"}, Stop: true}, big)
	require.Equal(
		t,
		`{"status":"SUCCESS","output":{"type":"INLINE","content_type":"application/json","data":{"n":1}},"next":["a"],"stop":true}`,
		string(must(compact(env))),
	)

	ref := &Block{
		Type:        REFERENCE,
		Ref:         "k",
		Size:        3,
		ContentType: NDJSON,
	}

	require.Equal(t, `{"type":"REFERENCE","ref":"k","size":3,"content_type":"application/x-ndjson"}`, string(must(compact(ref))))
}

func TestBufferCapGrowsOnlyWhenAnUploadIsOffered(t *testing.T) {
	offered := &Upload{
		URL: "https://s/put",
		Key: "k",
	}

	require.Equal(t, int64(64), bufferCap(64, nil, 512))
	require.Equal(t, int64(64), bufferCap(64, offered, 0))
	require.Equal(t, int64(512*1024*1024/4/ParseExpansion), bufferCap(64, offered, 512))
	require.Equal(t, int64(big), bufferCap(big, offered, 1))
	require.False(t, (&Upload{URL: "u"}).Offered())
}

func TestRowOutputRefusesBeforeItCouldExceedGuestMemory(t *testing.T) {
	const memoryLimitMB = 512

	offered := &Upload{
		URL: "https://s/put",
		Key: "k",
	}

	cap := bufferCap(DefaultInlineMaxBytes, offered, memoryLimitMB)
	require.LessOrEqual(t, cap*ParseExpansion, int64(memoryLimitMB*1024*1024/4))

	row := map[string]any{"pad": strings.Repeat("x", 1000)}
	rowBytes := int64(len(must(compact(row)))) + 1
	consumed := int64(0)

	rows := func(yield func(any) bool) {
		for {
			consumed++

			if !yield(row) {
				return
			}
		}
	}

	_, err := ToEnvelope(iter.Seq[any](rows), DefaultInlineMaxBytes, offered, memoryLimitMB, nil)
	require.True(t, errors.Is(err, &OutputTooLarge{}))
	require.ErrorContains(t, err, "max_output_mb")
	require.LessOrEqual(
		t,
		(consumed-1)*rowBytes*ParseExpansion,
		int64(memoryLimitMB*1024*1024/4),
		"buffered %d rows, which decode to more than a quarter of guest memory",
		consumed,
	)
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(fmt.Sprintf("unexpected error: %v", err))
	}

	return v
}

// Reserved for per-row routing. Without the guard a Result in the row position
// is flattened into an ordinary row carrying output/next/stop/meta as fields.
func TestAResultInTheRowPositionIsRefusedFromASequence(t *testing.T) {
	_, err := ToEnvelope(
		Result{Output: []any{Result{Output: map[string]any{"id": 1}}}, ContentType: NDJSON},
		1<<20, nil, 0, nil,
	)

	require.ErrorContains(t, err, "a row cannot be a Result")
}

func TestAResultInTheRowPositionIsRefusedFromAnIterator(t *testing.T) {
	rows := iter.Seq[any](func(yield func(any) bool) {
		yield(&Result{Output: map[string]any{"id": 1}, Next: []string{"b"}})
	})

	_, err := ToEnvelope(Result{Output: rows, ContentType: NDJSON}, 1<<20, nil, 0, nil)

	require.ErrorContains(t, err, "Per-row routing is not supported yet")
}

func TestTheReturnedResultItselfIsLeftAlone(t *testing.T) {
	envelope, err := ToEnvelope(
		Result{Output: []any{map[string]any{"id": 1}}, ContentType: NDJSON, Next: []string{"b"}},
		1<<20, nil, 0, nil,
	)

	require.NoError(t, err)
	require.Equal(t, []string{"b"}, envelope.Next)
	require.Equal(t, []any{map[string]any{"id": 1}}, envelope.Output.Data)
}
