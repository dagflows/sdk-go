package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dagflows/sdk-go/internal/authoring"
	"github.com/dagflows/sdk-go/internal/runtime"
	"github.com/stretchr/testify/require"
)

func handler(*runtime.Ctx, *runtime.Inputs) (any, error) {
	return map[string]any{}, nil
}

func demo() *authoring.Workflow {
	wf := authoring.NewWorkflow("demo", authoring.WorkflowOptions{
		Version: "1.26",
	})
	wf.Node(handler, authoring.NodeOptions{
		Key: "extract",
	})

	return wf
}

func run(t *testing.T, workflows []*authoring.Workflow, args ...string) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	code := Main{
		Args:      args,
		Workflows: workflows,
		Stdout:    &stdout,
		Stderr:    &stderr,
	}.Run()

	return code, stdout.String(), stderr.String()
}

func TestRoutingExitCodes(t *testing.T) {
	t.Setenv(runtime.InputEnv, "")

	code, stdout, _ := run(t, nil)
	require.Equal(t, USAGE, code)
	require.Contains(t, stdout, "dagflows - author and run workflow nodes")

	code, stdout, _ = run(t, nil, "help")
	require.Equal(t, OK, code)
	require.Contains(t, stdout, "build manifest")

	code, _, stderr := run(t, nil, "nonsense")
	require.Equal(t, USAGE, code)
	require.Contains(t, stderr, "dagflows nonsense: unknown command 'nonsense'")

	code, stdout, _ = run(t, nil, "nonsense", "--json")
	require.Equal(t, USAGE, code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
	require.Equal(t, false, payload["ok"])
	require.Equal(t, "nonsense", payload["command"])
}

func TestValidateAndManifestInProcess(t *testing.T) {
	t.Chdir(t.TempDir())

	code, stdout, _ := run(t, []*authoring.Workflow{demo()}, "build", "validate", "--json")
	require.Equal(t, OK, code)
	require.Equal(t, "{\n  \"ok\": true,\n  \"nodes\": [\n    \"extract\"\n  ],\n  \"runtime\": {\n    \"language\": \"go\",\n    \"version\": \"1.26\"\n  }\n}\n", stdout)

	code, stdout, _ = run(t, []*authoring.Workflow{demo()}, "build", "manifest", "-o", "out/m.json")
	require.Equal(t, OK, code)
	require.Equal(t, "wrote out/m.json with 1 node(s)\n", stdout)
	require.FileExists(t, "out/m.json")

	code, _, stderr := run(t, nil, "build", "manifest")
	require.Equal(t, FAILED, code)
	require.Contains(t, stderr, "declared no workflow")

	code, _, stderr = run(t, []*authoring.Workflow{demo()}, "build", "manifest", "-o")
	require.Equal(t, USAGE, code)
	require.Contains(t, stderr, "-o needs a path")
}

func TestDriftNamesWhatChanged(t *testing.T) {
	committed := map[string]any{
		"runtime":  map[string]any{"language": "go"},
		"workflow": map[string]any{"name": "a"},
		"nodes": []any{
			map[string]any{"key": "gone"},
			map[string]any{"key": "same", "config": 1.0},
		},
	}
	fresh := map[string]any{
		"runtime": map[string]any{
			"language": "go",
			"version":  "1.26",
		},
		"workflow": map[string]any{"name": "b"},
		"nodes": []any{
			map[string]any{"key": "same", "config": 2.0},
			map[string]any{"key": "new"},
		},
	}

	require.Equal(t, []string{
		"node 'gone' was removed",
		"node 'new' was added",
		"node 'same' changed",
		"runtime changed: map[language:go] -> map[language:go version:1.26]",
		"workflow settings changed",
	}, drift(committed, fresh))

	require.Equal(t, []string{
		"the committed manifest differs from the source",
	}, drift(map[string]any{"v": 1.0}, map[string]any{"v": 2.0}))
}

func TestParseInvokeAcceptsTheInformationalFlagAndRefusesOthers(t *testing.T) {
	node, err := parseInvoke([]string{"invoke", "--node", "compute"})
	require.NoError(t, err)
	require.Equal(t, "compute", node)

	node, err = parseInvoke(nil)
	require.NoError(t, err)
	require.Empty(t, node)

	_, err = parseInvoke([]string{"invoke", "--bogus"})
	require.ErrorContains(t, err, "unknown argument '--bogus', usage:")

	_, err = parseInvoke([]string{"invoke", "--node"})
	require.ErrorContains(t, err, "--node needs a value")
}

func TestInputSpecsBecomeEntries(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rows.jsonl"), []byte("{\"n\": 1}\n\n{\"n\": 2}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hello"), 0o644))

	var options devOptions
	require.NoError(t, options.addInput("a="+filepath.Join(dir, "rows.jsonl")))
	require.NoError(t, options.addInput("b="+filepath.Join(dir, "note.txt")))
	require.NoError(t, options.addInput(`c={"n": 1}`))
	require.NoError(t, options.addInput(`d=[1, 2]`))

	encoded, err := json.Marshal(options.inputs)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"a": {"type": "INLINE", "content_type": "application/x-ndjson", "data": [{"n": 1}, {"n": 2}]},
		"b": {"type": "INLINE", "content_type": "text/plain", "data": "hello"},
		"c": {"type": "INLINE", "content_type": "application/json", "data": {"n": 1}},
		"d": {"type": "INLINE", "content_type": "application/x-ndjson", "data": [1, 2]}
	}`, string(encoded))

	err = options.addInput("no-equals-sign")
	require.ErrorContains(t, err, "<parent>=<file or json>")
	require.Equal(t, USAGE, err.(*commandError).code)

	err = options.addInput("e=./nope.ndjson")
	require.ErrorContains(t, err, "neither a file that exists nor valid json")
	require.Equal(t, FAILED, err.(*commandError).code)
}

func TestTheLocalEnvelopeCarriesWhatTheNodeBelieves(t *testing.T) {
	options := devOptions{
		node:           "extract",
		memoryLimitMB:  256,
		inlineMaxBytes: 64,
	}

	encoded, err := json.Marshal(options.envelope())
	require.NoError(t, err)
	require.JSONEq(t, `{
		"ctx": {"workflow_run_id": "local", "node_key": "extract", "language": "go", "entrypoint": "app",
		        "config": {}, "timeout_seconds": 0, "attempt": 0, "memory_limit_mb": 256, "inline_max_bytes": 64},
		"payload": {"inputs": {}}
	}`, string(encoded))
}
