package runtime

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Tests for wire compatibility against the cross-SDK contract fixtures.
//
// The fixtures in sdk-contract/fixtures were produced by the Python SDK, so this
// runtime must read and write the same bytes.

const contractFixtures = "../../sdk-contract/fixtures"

func contractFixture(t *testing.T, name string) []byte {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(contractFixtures, filepath.FromSlash(name)))
	if err != nil {
		t.Skipf("contract fixture %s not on disk", name)
	}

	return raw
}

func compacted(t *testing.T, raw []byte) string {
	t.Helper()

	var out bytes.Buffer
	require.NoError(t, json.Compact(&out, raw))

	return out.String()
}

type (
	vectorColour string
	vectorOrder  struct {
		ID     int64  `json:"id"`
		Amount int64  `json:"amount"`
		Note   string `json:"note"`
	}
)

// Tests that the compact encoder writes the contract's bytes: the same JSON,
// and the same size Python measures, for the same values.
func TestTheEncoderWritesTheContractsBytes(t *testing.T) {
	var vectors []struct {
		Name  string `json:"name"`
		JSON  string `json:"json"`
		Bytes int64  `json:"bytes"`
	}
	require.NoError(t, json.Unmarshal(contractFixture(t, "codec/vectors.json"), &vectors))

	values := map[string]any{
		"non-ascii is utf8":                         map[string]any{"city": "München", "note": "日本語"},
		"compact":                                   map[string]any{"a": []any{1, 2, map[string]any{"b": nil}}, "c": true},
		"bytes are base64":                          map[string]any{"blob": []byte{0x00, 0x01, 0xfe, 0xff}},
		"timestamp is rfc3339 utc":                  map[string]any{"when": time.Date(2026, 8, 30, 12, 0, 5, 0, time.UTC)},
		"timestamp keeps its offset":                map[string]any{"when": time.Date(2026, 8, 30, 12, 0, 0, 0, time.FixedZone("", 2*60*60))},
		"decimal is a string":                       map[string]any{"price": Decimal("10.50")},
		"enum is its value":                         map[string]any{"colour": vectorColour("red")},
		"object is its fields in declaration order": vectorOrder{ID: 1, Amount: 100},
		"int64 is exact":                            map[string]any{"id": int64(9007199254740993)},
	}

	require.Len(t, values, len(vectors), "every vector needs a Go value")

	for _, v := range vectors {
		value, ok := values[v.Name]
		require.True(t, ok, "no Go value for vector %q", v.Name)

		encoded, err := compact(value)
		require.NoError(t, err, v.Name)
		require.Equal(t, v.JSON, string(encoded), v.Name)

		size, err := encodedLen(newCompactEncoder(), value)
		require.NoError(t, err, v.Name)
		require.Equal(t, v.Bytes, size, v.Name)
	}
}

func TestTheSuccessEnvelopeMatchesTheContract(t *testing.T) {
	expected := compacted(t, contractFixture(t, "envelope/output-with-meta-and-routing.json"))

	envelope, err := ToEnvelope(Result[any]{
		Output: map[string]any{"total": 350},
		Next:   []string{"process_payment", "notify"},
		Meta:   map[string]any{"orders": 2},
	}, 1<<20, nil, 0, nil)
	require.NoError(t, err)

	encoded, err := compact(envelope)
	require.NoError(t, err)
	require.Equal(t, expected, string(encoded))
}

func TestTheFailedEnvelopesMatchTheContract(t *testing.T) {
	cases := map[string]error{
		"envelope/output-failed-with-code-and-details.json": &Fail{
			Message:  "card declined",
			Category: EXECUTION,
			Code:     "card_declined",
			Details:  map[string]any{"last4": "4242"},
		},
		"envelope/output-failed-plain.json": &Fail{Message: "no", Category: EXECUTION},
	}

	for name, failure := range cases {
		expected := compacted(t, contractFixture(t, name))

		encoded, err := compact(Failure(failure))
		require.NoError(t, err, name)
		require.Equal(t, expected, string(encoded), name)
	}
}

func TestTheInputEnvelopeCarriesTriggerAndCapabilities(t *testing.T) {
	var envelope struct {
		Ctx     map[string]any `json:"ctx"`
		Payload struct {
			Inputs map[string]any `json:"inputs"`
		} `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(contractFixture(t, "envelope/input-with-trigger-and-capabilities.json"), &envelope))

	ctx := CtxFromRaw(envelope.Ctx)
	require.Equal(t, RunInfo{
		WorkflowRunID:  "run-1",
		NodeKey:        "on_order",
		Attempt:        2,
		Language:       "python",
		RuntimeVersion: "3.12",
	}, ctx.Run())
	require.Equal(t, Limits{MemoryMB: 512, MilliCores: 1000, TimeoutMs: 300_000}, ctx.Limits())
	require.True(t, ctx.Has("stream/v1"))
	require.False(t, ctx.Has("gpu/v1"))
	require.Equal(t, &TriggerInfo{
		Kind:       "webhook",
		ID:         "delivery-7",
		ReceivedAt: "2026-08-30T12:00:00Z",
		Attributes: map[string]any{"source": "shop"},
	}, ctx.Trigger())
	require.Nil(t, CtxFromRaw(map[string]any{}).Trigger(), "a run nothing triggered has no trigger")

	type orderPlaced struct {
		OrderID string `json:"order_id"`
		Amount  int64  `json:"amount"`
	}

	event, err := NewInputs(envelope.Payload.Inputs, ctx.MemoryMB).Get("order_placed")
	require.NoError(t, err)

	decoded, err := Typed[orderPlaced](event).Value()
	require.NoError(t, err)
	require.Equal(t, orderPlaced{OrderID: "o-1", Amount: 100}, decoded)
}

func contractEntry(data any, contentType ContentType) map[string]any {
	return map[string]any{"type": INLINE, "content_type": string(contentType), "data": data}
}

func TestTypedDecodingNamesTheInputAndTheField(t *testing.T) {
	type order struct {
		ID int64 `json:"id"`
	}
	type orders struct {
		Orders []order `json:"orders"`
	}

	handle, err := NewInputs(map[string]any{
		"fetch_orders": contractEntry(map[string]any{"orders": []any{map[string]any{"id": "one"}}}, JSON),
	}, 0).Get("fetch_orders")
	require.NoError(t, err)

	_, err = Typed[orders](handle).Value()
	require.ErrorContains(t, err, "fetch_orders")
	require.ErrorContains(t, err, "id")
}

func TestTypedDecodingKeepsLargeIntegersExact(t *testing.T) {
	type row struct {
		ID int64 `json:"id"`
	}

	handle, err := NewInputs(map[string]any{
		"p": contractEntry(map[string]any{"id": json.Number("9007199254740993")}, JSON),
	}, 0).Get("p")
	require.NoError(t, err)

	decoded, err := Typed[row](handle).Value()
	require.NoError(t, err)
	require.Equal(t, int64(9007199254740993), decoded.ID)

	// The untyped view keeps the same digits.
	raw, err := handle.Value()
	require.NoError(t, err)
	require.Equal(t, json.Number("9007199254740993"), raw.(map[string]any)["id"])
}

func TestTheProfileTypesDecode(t *testing.T) {
	type colour string
	type everything struct {
		When   time.Time `json:"when"`
		Price  Decimal   `json:"price"`
		Blob   []byte    `json:"blob"`
		Maybe  *string   `json:"maybe"`
		Colour colour    `json:"colour"`
		Tags   []string  `json:"tags"`
		Bag    map[string]any
		Note   string `json:"note,omitempty"`
	}

	handle, err := NewInputs(map[string]any{
		"p": contractEntry(map[string]any{
			"when":   "2026-08-30T12:00:05Z",
			"price":  "10.50",
			"blob":   "AAH+/w==",
			"maybe":  nil,
			"colour": "red",
			"tags":   []any{"a", "b"},
			"Bag":    map[string]any{"n": json.Number("1")},
		}, JSON),
	}, 0).Get("p")
	require.NoError(t, err)

	decoded, err := Typed[everything](handle).Value()
	require.NoError(t, err)
	require.True(t, decoded.When.Equal(time.Date(2026, 8, 30, 12, 0, 5, 0, time.UTC)))
	require.Equal(t, Decimal("10.50"), decoded.Price)
	require.Equal(t, []byte{0x00, 0x01, 0xfe, 0xff}, decoded.Blob)
	require.Nil(t, decoded.Maybe)
	require.Equal(t, colour("red"), decoded.Colour)
	require.Equal(t, []string{"a", "b"}, decoded.Tags)
	require.Equal(t, map[string]any{"n": json.Number("1")}, decoded.Bag, "a map of any keeps numbers exact")
	require.Empty(t, decoded.Note, "a missing optional field is its zero value")
}

func TestTypedRowsDecodeEachRecord(t *testing.T) {
	type line struct {
		N int64 `json:"n"`
	}

	handle, err := NewInputs(map[string]any{
		"p": contractEntry([]any{map[string]any{"n": 1}, map[string]any{"n": 2}}, NDJSON),
	}, 0).Get("p")
	require.NoError(t, err)

	var total int64
	for row, err := range Stream[line](handle) {
		require.NoError(t, err)
		total += row.N
	}
	require.Equal(t, int64(3), total)

	// A record that does not fit ends the stream with the input named.
	bad, err := NewInputs(map[string]any{
		"p": contractEntry([]any{map[string]any{"n": 1}, map[string]any{"n": "two"}}, NDJSON),
	}, 0).Get("p")
	require.NoError(t, err)

	var seen []line
	var failed error
	for row, err := range Stream[line](bad) {
		if err != nil {
			failed = err
			break
		}
		seen = append(seen, row)
	}
	require.Equal(t, []line{{N: 1}}, seen)
	require.ErrorContains(t, failed, "p")
	require.ErrorContains(t, failed, "n")
}

// Tests that a typed handle reads the same bytes the untyped one does: one
// download, two views.
func TestATypedHandleIsTheSameHandle(t *testing.T) {
	type totals struct {
		Total int64 `json:"total"`
	}

	handle, err := NewInputs(map[string]any{
		"p": contractEntry(map[string]any{"total": json.Number("350")}, JSON),
	}, 0).Get("p")
	require.NoError(t, err)

	typed := Typed[totals](handle)
	require.Equal(t, "p", typed.Key())
	require.Equal(t, JSON, typed.ContentType())

	decoded, err := typed.Value()
	require.NoError(t, err)
	require.Equal(t, totals{Total: 350}, decoded)

	raw, err := handle.Value()
	require.NoError(t, err)
	require.Equal(t, map[string]any{"total": json.Number("350")}, raw)
}
