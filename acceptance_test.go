package dagflows_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	df "github.com/dagflows/sdk-go"
	"github.com/stretchr/testify/require"
)

// The builder's acceptance copy: the untyped branching demo every SDK emits,
// so the builder's parser is tested against what this SDK really writes.
func anything(*df.Ctx, df.None) (any, error) {
	return map[string]any{}, nil
}

func referenceWorkflow() *df.Workflow {
	wf := df.NewWorkflow("branching-demo", df.WorkflowOptions{
		Version:            "1.27",
		MaxConcurrentNodes: 5,
	})
	step1 := wf.Node(anything, df.Root, df.NodeOptions{
		Key: "step_1",
		Execution: &df.Execution{
			Machine: "m",
		},
	})
	step2 := wf.Node(handler, df.Depends(step1), df.NodeOptions{
		Key: "step_2",
		Execution: &df.Execution{
			Machine:     "m",
			TimeoutSecs: 30,
		},
		Retry: &df.Retry{
			MaxAttempts:      new(2),
			InitialBackoffMs: new(1000),
		},
	})
	wf.Node(handler, df.Depends(step2, wf.ExternalNode("crunch")), df.NodeOptions{
		Key: "report",
	})

	return wf
}

const (
	fixturePath   = "testdata/go-sdk-manifest.json"
	referenceDir  = "../sdk-contract/reference/go"
	goFixture     = "../sdk-contract/fixtures/manifest/typed-demo.go.json"
	pythonFixture = "../sdk-contract/fixtures/manifest/typed-demo.python.json"
)

func TestTheAcceptanceFixtureIsByteStable(t *testing.T) {
	manifest, err := referenceWorkflow().Manifest()
	require.NoError(t, err)

	encoded, err := manifest.Encode()
	require.NoError(t, err)

	if os.Getenv("UPDATE_FIXTURES") != "" {
		require.NoError(t, os.WriteFile(fixturePath, encoded, 0o644))
	}

	committed, err := os.ReadFile(fixturePath)
	require.NoError(t, err)
	require.Equal(t, string(committed), string(encoded), "the fixture drifted; set UPDATE_FIXTURES=1 to regenerate it")
}

// The conformance fixture: the reference typed workflow in sdk-contract,
// compiled and run as the builder runs it, must emit byte for byte what is
// committed there.
func TestTheReferenceWorkflowEmitsTheCommittedFixture(t *testing.T) {
	if _, err := os.Stat(referenceDir); err != nil {
		t.Skipf("the contract's reference program is not on disk at %s", referenceDir)
	}

	dir := t.TempDir()
	binary := buildIn(dir, referenceDir, "reference")

	result := runBinary(t, binary, dir, nil, "build", "manifest", "-o", "m.json")
	require.Equal(t, 0, result.code, result.stderr)

	encoded, err := os.ReadFile(filepath.Join(dir, "m.json"))
	require.NoError(t, err)

	if os.Getenv("UPDATE_FIXTURES") != "" {
		require.NoError(t, os.WriteFile(goFixture, encoded, 0o644))
	}

	committed, err := os.ReadFile(goFixture)
	require.NoError(t, err)
	require.Equal(t, string(committed), string(encoded), "the contract fixture drifted; set UPDATE_FIXTURES=1 to regenerate it")
}

// Tests that the Go emitter and the Python emitter agree on the typed
// contract: the same graph declared in each language gives the same
// manifest, except for what names the language itself.
func TestTheFixtureAgreesWithPythonsExceptWhereTheLanguageDiffers(t *testing.T) {
	theirs, err := os.ReadFile(pythonFixture)
	if err != nil {
		t.Skipf("python fixture not on disk at %s", pythonFixture)
	}

	ours, err := os.ReadFile(goFixture)
	require.NoError(t, err)

	var python_, go_ map[string]any

	require.NoError(t, json.Unmarshal(theirs, &python_))
	require.NoError(t, json.Unmarshal(ours, &go_))

	require.Equal(t, map[string]any{"language": "python", "version": "3.12"}, python_["runtime"])
	require.Equal(t, map[string]any{"language": "go", "version": "1.27"}, go_["runtime"])
	require.Equal(t, python_["v"], go_["v"])
	require.Equal(t, python_["workflow"], go_["workflow"])
	require.Equal(t, python_["triggers"], go_["triggers"])

	pythonNodes := python_["nodes"].([]any)
	goNodes := go_["nodes"].([]any)
	require.Len(t, goNodes, len(pythonNodes))

	for i := range pythonNodes {
		expected := pythonNodes[i].(map[string]any)
		expected["entrypoint"] = "app"
		require.Equal(t, expected, goNodes[i].(map[string]any), "node %d", i)
	}
}
