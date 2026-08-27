package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func node(t *testing.T, handler Handler, ctx map[string]any, inputs map[string]any) map[string]any {
	t.Helper()

	rawCtx := map[string]any{
		"node_key":         "n",
		"inline_max_bytes": 1 << 20,
	}

	maps.Copy(rawCtx, ctx)

	if inputs == nil {
		inputs = map[string]any{}
	}

	in := writeInput(t, map[string]any{
		"ctx": rawCtx,
		"payload": map[string]any{
			"inputs": inputs,
		},
	})

	out := filepath.Join(t.TempDir(), "out.json")
	t.Setenv(InputEnv, in)
	t.Setenv(OutputEnv, out)

	require.NoError(t, Run(handler))

	body, err := os.ReadFile(out)
	require.NoError(t, err)

	parsed, err := DecodeJSON(body)
	require.NoError(t, err)

	return parsed.(map[string]any)
}

func data(result map[string]any) any {
	return result["output"].(map[string]any)["data"]
}

var seedInput = map[string]any{
	"seed": map[string]any{
		"type": INLINE,
		"data": map[string]any{"factor": 3},
	},
}

func TestAHandlerReadsItsInputAndReturnsAValue(t *testing.T) {
	handler := func(_ *Ctx, inputs *Inputs) (any, error) {
		seed, err := inputs.Get("seed")
		if err != nil {
			return nil, err
		}

		value, err := seed.Value()
		if err != nil {
			return nil, err
		}

		factor, err := value.(map[string]any)["factor"].(json.Number).Int64()
		if err != nil {
			return nil, err
		}

		return map[string]any{"value": 14 * factor}, nil
	}

	result := node(t, handler, nil, seedInput)
	require.Equal(t, "SUCCESS", result["status"])
	require.Equal(t, map[string]any{"value": json.Number("42")}, data(result))
}

func TestRoutingAndStopReachTheEnvelope(t *testing.T) {
	result := node(t, func(*Ctx, *Inputs) (any, error) {
		return Result{
			Output: map[string]any{"ok": true},
			Next:   []string{"notify"},
		}, nil
	}, nil, nil)

	require.Equal(t, []any{"notify"}, result["next"])
	require.NotContains(t, result, "stop")

	halting := node(t, func(*Ctx, *Inputs) (any, error) {
		return &Result{
			Output: map[string]any{},
			Stop:   true,
		}, nil
	}, nil, nil)

	require.Equal(t, true, halting["stop"])
}

func TestPrintsCannotCorruptTheAnswer(t *testing.T) {
	result := node(t, func(*Ctx, *Inputs) (any, error) {
		fmt.Println("fetching records")
		fmt.Println(`{"status":"ok"}`)

		return map[string]any{"value": 1}, nil
	}, nil, nil)

	require.Equal(t, map[string]any{"value": json.Number("1")}, data(result))
}

func TestADeliberateFailureCarriesItsCategoryAndRetry(t *testing.T) {
	result := node(t, func(*Ctx, *Inputs) (any, error) {
		return nil, &Fail{
			Message:    "upstream returned 503",
			Category:   INFRASTRUCTURE,
			RetryAfter: 30,
		}
	}, nil, nil)

	require.Equal(t, "FAILED", result["status"])
	require.Equal(t, map[string]any{
		"message":  "upstream returned 503",
		"category": "infrastructure",
	}, result["error"])
	require.Equal(t, map[string]any{"after_seconds": json.Number("30")}, result["retry"])
	require.NotContains(t, result, "output")
}

func TestAFailureWithoutADelayAborts(t *testing.T) {
	result := node(t, func(*Ctx, *Inputs) (any, error) {
		return nil, &Fail{
			Message: "bad config",
		}
	}, nil, nil)

	require.Equal(t, "permanent", result["error"].(map[string]any)["category"])
	require.Equal(t, map[string]any{"abort": true}, result["retry"])
}

func TestAnUnknownCategoryIsReportedAsPermanent(t *testing.T) {
	result := node(t, func(*Ctx, *Inputs) (any, error) {
		return nil, &Fail{
			Message:    "odd",
			Category:   "whatever",
			RetryAfter: 5,
		}
	}, nil, nil)

	require.Equal(t, "permanent", result["error"].(map[string]any)["category"])
}

func TestABugInTheNodeIsPermanentAndNamed(t *testing.T) {
	result := node(t, func(*Ctx, *Inputs) (any, error) {
		return nil, fmt.Errorf("looking up the seed: %w", errors.New("key 'missing' not found"))
	}, nil, nil)

	require.Equal(t, "FAILED", result["status"])
	require.Equal(t, map[string]any{
		"message":  "looking up the seed: key 'missing' not found",
		"category": "permanent",
	}, result["error"])
	require.Equal(t, map[string]any{"abort": true}, result["retry"])
}

func TestAPanicInTheNodeIsAFailedEnvelopeNotACrash(t *testing.T) {
	result := node(t, func(*Ctx, *Inputs) (any, error) {
		var rows []int

		return rows[5], nil
	}, nil, nil)

	require.Equal(t, "FAILED", result["status"])

	failure := result["error"].(map[string]any)
	require.Equal(t, "permanent", failure["category"])
	require.Contains(t, failure["message"], "panic: runtime error: index out of range")
	require.Equal(t, map[string]any{"abort": true}, result["retry"])
}

func TestAWrappedFailIsStillAFail(t *testing.T) {
	result := node(t, func(*Ctx, *Inputs) (any, error) {
		return nil, fmt.Errorf("while syncing: %w", &Fail{
			Message:    "throttled",
			Category:   TIMEOUT,
			RetryAfter: 10,
		})
	}, nil, nil)

	require.Equal(t, map[string]any{
		"message":  "throttled",
		"category": "timeout",
	}, result["error"])
	require.Equal(t, map[string]any{"after_seconds": json.Number("10")}, result["retry"])
}

func TestAnOversizeOutputFailsTheNodeRatherThanWritingABadEnvelope(t *testing.T) {
	result := node(t, func(*Ctx, *Inputs) (any, error) {
		return map[string]any{"blob": strings.Repeat("x", 4096)}, nil
	}, map[string]any{"inline_max_bytes": 512}, nil)

	require.Equal(t, "FAILED", result["status"])
	require.Contains(t, result["error"].(map[string]any)["message"], "512")
}

func TestTheRunSuppliedThresholdIsUsed(t *testing.T) {
	result := node(t, func(*Ctx, *Inputs) (any, error) {
		return map[string]any{"blob": strings.Repeat("x", 4096)}, nil
	}, map[string]any{"inline_max_bytes": 1 << 20}, nil)

	require.Equal(t, "SUCCESS", result["status"])
}

func TestAStreamedResultInlinesWhenItIsSmall(t *testing.T) {
	result := node(t, func(*Ctx, *Inputs) (any, error) {
		return Result{
			Output: iter.Seq[any](lazy(map[string]any{"id": 1}, map[string]any{"id": 2})),
		}, nil
	}, nil, nil)

	output := result["output"].(map[string]any)
	require.Equal(t, NDJSON, output["content_type"])
	require.Equal(t, []any{map[string]any{"id": json.Number("1")}, map[string]any{"id": json.Number("2")}}, output["data"])
}

func TestCtxExposesTheLimitsTheRunIsHeldTo(t *testing.T) {
	result := node(t, func(ctx *Ctx, _ *Inputs) (any, error) {
		return map[string]any{
			"memory":  ctx.MemoryLimitMB,
			"cores":   ctx.MilliCores,
			"attempt": ctx.Attempt,
		}, nil
	}, map[string]any{
		"memory_limit_mb": 512,
		"milli_cores":     1000,
		"attempt":         2,
	}, nil)

	require.Equal(t, map[string]any{
		"memory":  json.Number("512"),
		"cores":   json.Number("1000"),
		"attempt": json.Number("2"),
	}, data(result))
}

func TestARetryableFailureNamesNoDirectiveAtAll(t *testing.T) {
	result := node(t, func(*Ctx, *Inputs) (any, error) {
		return nil, &Fail{
			Message:  "the source is down",
			Category: INFRASTRUCTURE,
			Abort:    new(false),
		}
	}, nil, nil)

	require.Equal(t, "FAILED", result["status"])
	require.Equal(t, "infrastructure", result["error"].(map[string]any)["category"])
	require.NotContains(t, result, "retry")
}

func TestAnExplicitAbortWithADelayCarriesBoth(t *testing.T) {
	result := node(t, func(*Ctx, *Inputs) (any, error) {
		return nil, &Fail{
			Message:    "x",
			RetryAfter: 5,
			Abort:      new(true),
		}
	}, nil, nil)

	require.Equal(t, map[string]any{
		"abort":         true,
		"after_seconds": json.Number("5"),
	}, result["retry"])
}

func TestTheEnvelopeIsWrittenCompactAndInOrder(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.json")

	require.NoError(t, Write(Failure(&Fail{
		Message:    "a <b>",
		RetryAfter: 3,
	}), out))

	body, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, `{"status":"FAILED","error":{"message":"a <b>","category":"permanent"},"retry":{"after_seconds":3}}`, string(body))
}

func TestWriteWithoutAnOutputPathNamesTheRemedy(t *testing.T) {
	t.Setenv(OutputEnv, "")

	err := Write(map[string]any{}, "")
	require.ErrorContains(t, err, "DAGFLOWS_OUTPUT is not set")
}

func TestRunWithoutAnInputPathReturnsTheError(t *testing.T) {
	t.Setenv(InputEnv, "")

	err := Run(func(*Ctx, *Inputs) (any, error) {
		return nil, nil
	})

	require.ErrorContains(t, err, "DAGFLOWS_INPUT is not set")
}

func TestAFailureMessageIsBoundedForTheTransport(t *testing.T) {
	huge := strings.Repeat("é", 10_000)
	failed := Failure(errors.New(huge))

	require.Less(t, len(failed.Error.Message), maxMessageBytes+32)
	require.True(t, strings.HasSuffix(failed.Error.Message, "...(truncated)"))
	require.True(t, utf8.ValidString(failed.Error.Message), "the cut must not split a rune")

	deliberate := Failure(&Fail{
		Message: huge,
	})

	require.True(t, strings.HasSuffix(deliberate.Error.Message, "...(truncated)"))
	require.Equal(t, "short", Failure(errors.New("short")).Error.Message)
}
