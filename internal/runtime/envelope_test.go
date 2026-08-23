package runtime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeInput(t *testing.T, envelope any) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "input.json")
	body, err := json.Marshal(envelope)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, body, 0o644))

	return path
}

func inlineEntry(data any) map[string]any {
	return map[string]any{
		"type": INLINE,
		"data": data,
	}
}

func inputsOf(entries map[string]any) *Inputs {
	return NewInputs(entries, 0)
}

func mustGet(t *testing.T, inputs *Inputs, key string) *Input {
	t.Helper()

	handle, err := inputs.Get(key)
	require.NoError(t, err)

	return handle
}

func TestLoadReadsCtxAndInputs(t *testing.T) {
	path := writeInput(t, map[string]any{
		"ctx": map[string]any{
			"workflow_run_id":  "wr_1",
			"node_key":         "transform",
			"language":         "go",
			"runtime_version":  "1.26",
			"abi":              "deb13",
			"timeout_seconds":  600,
			"memory_limit_mb":  512,
			"inline_max_bytes": 1024,
		},
		"payload": map[string]any{
			"inputs": map[string]any{
				"seed": inlineEntry(map[string]any{"factor": 3}),
			},
		},
	})

	ctx, inputs, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "transform", ctx.NodeKey)
	require.Equal(t, "1.26", ctx.RuntimeVersion)
	require.Equal(t, 512, ctx.MemoryLimitMB)
	require.Equal(t, 1024, ctx.InlineMaxBytes)

	value, err := mustGet(t, inputs, "seed").Value()
	require.NoError(t, err)
	require.Equal(t, json.Number("3"), value.(map[string]any)["factor"])
}

func TestCtxKeepsFieldsTheSDKDoesNotKnow(t *testing.T) {
	ctx := CtxFromRaw(map[string]any{
		"node_key":      "n",
		"something_new": 7,
	})

	require.Equal(t, 7, ctx.Raw["something_new"])
}

func TestInlineMaxBytesFallsBackWhenUnstated(t *testing.T) {
	require.Equal(t, DefaultInlineMaxBytes, CtxFromRaw(map[string]any{}).InlineMaxBytes)
	require.Equal(t, DefaultInlineMaxBytes, CtxFromRaw(nil).InlineMaxBytes)
}

func TestNumericFieldsCoerceIntsAndFloatsAndDefaultOtherwise(t *testing.T) {
	ctx := CtxFromRaw(map[string]any{
		"attempt":          json.Number("2"),
		"memory_limit_mb":  json.Number("512.0"),
		"milli_cores":      "lots",
		"timeout_seconds":  nil,
		"inline_max_bytes": true,
		"config":           "not a map",
	})

	require.Equal(t, 2, ctx.Attempt)
	require.Equal(t, 512, ctx.MemoryLimitMB)
	require.Zero(t, ctx.MilliCores)
	require.Zero(t, ctx.TimeoutSeconds)
	require.Equal(t, DefaultInlineMaxBytes, ctx.InlineMaxBytes)
	require.Empty(t, ctx.Config)
}

func TestMissingInputPathNamesTheLocalWorkflow(t *testing.T) {
	t.Setenv(InputEnv, "")

	_, _, err := Load("")
	require.ErrorContains(t, err, "fixture")
}

func TestLoadReadsThePathFromTheEnvironment(t *testing.T) {
	path := writeInput(t, map[string]any{
		"ctx": map[string]any{
			"node_key": "env",
		},
	})
	t.Setenv(InputEnv, path)

	ctx, _, err := Load("")
	require.NoError(t, err)
	require.Equal(t, "env", ctx.NodeKey)
}

func TestAnEmptyFileIsANodeWithNoParents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.json")
	require.NoError(t, os.WriteFile(path, nil, 0o644))

	ctx, inputs, err := Load(path)
	require.NoError(t, err)
	require.Empty(t, ctx.NodeKey)
	require.Zero(t, inputs.Len())
}

func TestAMalformedEnvelopeNamesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.json")
	require.NoError(t, os.WriteFile(path, []byte("{nope"), 0o644))

	_, _, err := Load(path)
	require.ErrorContains(t, err, "input.json is not a JSON envelope")
}

func TestAMissingInputNamesTheParentsThatExist(t *testing.T) {
	inputs := inputsOf(map[string]any{
		"alpha": inlineEntry(1),
		"beta":  inlineEntry(2),
	})

	_, err := inputs.Get("gamma")
	require.EqualError(t, err, "no input named 'gamma', this node's parents are: alpha, beta")

	_, err = inputsOf(nil).Get("gamma")
	require.ErrorContains(t, err, "parents are: none")
}

func TestOneRefusesWhenItIsNotOne(t *testing.T) {
	_, err := inputsOf(nil).One()
	require.ErrorContains(t, err, "none")

	_, err = inputsOf(map[string]any{
		"a": inlineEntry(1),
		"b": inlineEntry(2),
	}).One()
	require.ErrorContains(t, err, "a, b")
}

func TestOneAvoidsHardcodingARenameableKey(t *testing.T) {
	inputs := inputsOf(map[string]any{
		"whatever_the_user_called_it": inlineEntry(map[string]any{"n": 1}),
	})

	handle, err := inputs.One()
	require.NoError(t, err)

	value, err := handle.Value()
	require.NoError(t, err)
	require.Equal(t, 1, value.(map[string]any)["n"])
}

func TestAnInlineValueReadsAsAValue(t *testing.T) {
	single := mustGet(t, inputsOf(map[string]any{
		"a": inlineEntry(map[string]any{"n": 1}),
	}), "a")

	value, err := single.Value()
	require.NoError(t, err)
	require.Equal(t, map[string]any{"n": 1}, value)
	require.Equal(t, "<Input 'a' INLINE application/json>", single.String())
}

func TestAnInlineSequenceIteratesPerElement(t *testing.T) {
	rows := mustGet(t, inputsOf(map[string]any{
		"a": map[string]any{
			"type":         INLINE,
			"content_type": NDJSON,
			"data":         []any{1, 2, 3},
		},
	}), "a")

	require.Equal(t, []any{1, 2, 3}, collect(t, rows.Iter()))
}

func TestASingleValueIteratesOnce(t *testing.T) {
	single := mustGet(t, inputsOf(map[string]any{
		"a": inlineEntry(map[string]any{"n": 1}),
	}), "a")

	require.Equal(t, []any{map[string]any{"n": 1}}, collect(t, single.Iter()))
}

func TestAReferenceTooBigToMaterialiseNamesBothNumbers(t *testing.T) {
	ref := mustGet(t, NewInputs(map[string]any{
		"big": map[string]any{
			"type":         REFERENCE,
			"url":          "https://storage.test/obj",
			"size":         104857600,
			"content_type": NDJSON,
		},
	}, 512), "big")

	require.Equal(t, int64(104857600), ref.Size())
	require.Equal(t, NDJSON, ref.ContentType())
	require.Equal(t, "<Input 'big' REFERENCE 104857600B application/x-ndjson>", ref.String())

	_, err := ref.Value()

	var tooLarge *InputTooLarge

	require.ErrorAs(t, err, &tooLarge)
	require.True(t, errors.Is(err, &InputTooLarge{}))
	require.Contains(t, err.Error(), "104857600")
	require.Contains(t, err.Error(), "512")
	require.Contains(t, err.Error(), "iterate")
}

func TestContentTypeDefaultsToJSONWhenUnstated(t *testing.T) {
	require.Equal(t, JSON, mustGet(t, inputsOf(map[string]any{"a": inlineEntry(1)}), "a").ContentType())
	require.Equal(t, JSON, mustGet(t, inputsOf(map[string]any{"a": map[string]any{"content_type": ""}}), "a").ContentType())
}

func TestKeysAreSortedAndLenCounts(t *testing.T) {
	inputs := inputsOf(map[string]any{
		"b": inlineEntry(1),
		"a": inlineEntry(2),
		"c": "not an entry",
	})

	var keys []string

	for key := range inputs.Keys() {
		keys = append(keys, key)
	}

	require.Equal(t, []string{"a", "b", "c"}, keys)
	require.Equal(t, 3, inputs.Len())

	// Non-map entries fallback to empty entries without failing.
	require.Equal(t, INLINE, mustGet(t, inputs, "c").Type())
}

func TestNoMultipartInCtxReadsAsNil(t *testing.T) {
	require.Nil(t, (&Ctx{}).Multipart())
	require.Nil(t, CtxFromRaw(map[string]any{"output_multipart": nil}).Multipart())
	require.Nil(t, CtxFromRaw(map[string]any{
		"output_multipart": map[string]any{
			"part_urls": []any{},
			"part_size": 0,
		},
	}).Multipart())
	require.Nil(t, CtxFromRaw(map[string]any{
		"output_multipart": map[string]any{
			"part_urls":    []any{"u"},
			"part_size":    5,
			"complete_url": "",
		},
	}).Multipart())
}

func TestMultipartIsReadFromRawDefensively(t *testing.T) {
	multipart := CtxFromRaw(map[string]any{
		"output_multipart": map[string]any{
			"upload_id":    "u1",
			"part_size":    json.Number("5242880"),
			"part_urls":    []any{"https://a", "https://b"},
			"complete_url": "https://c",
		},
	}).Multipart()

	require.NotNil(t, multipart)
	require.Equal(t, "u1", multipart.UploadID)
	require.Equal(t, int64(5242880), multipart.PartSize)
	require.Equal(t, []string{"https://a", "https://b"}, multipart.PartURLs)
	require.Empty(t, multipart.AbortURL)
	require.Equal(t, int64(2*5242880), multipart.Capacity())
}
