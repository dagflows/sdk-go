package dagflows_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dagflows/sdk-go"
	"github.com/stretchr/testify/require"
)

func referenceWorkflow() *dagflows.Workflow {
	wf := dagflows.NewWorkflow("branching-demo", dagflows.WorkflowOptions{
		Version:            "1.26",
		MaxConcurrentNodes: 5,
	})
	step1 := wf.Node(handler, dagflows.NodeOptions{
		Key: "step_1",
		Execution: &dagflows.ExecutionConfig{
			Machine: "m",
		},
	})
	step2 := wf.Node(handler, dagflows.NodeOptions{
		Key:     "step_2",
		Depends: []*dagflows.NodeRef{step1},
		Execution: &dagflows.ExecutionConfig{
			Machine:     "m",
			TimeoutSecs: 30,
		},
		Retry: &dagflows.RetryConfig{
			MaxAttempts:      new(2),
			InitialBackoffMs: new(1000),
		},
	})
	wf.Node(handler, dagflows.NodeOptions{
		Key: "report",
		Depends: []*dagflows.NodeRef{
			step2,
			wf.ExternalNode("crunch"),
		},
	})

	return wf
}

const fixturePath = "testdata/go-sdk-manifest.json"

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

func TestTheFixtureAgreesWithPythonsExceptWhereTheLanguageDiffers(t *testing.T) {
	python := filepath.Join("..", "go-builder", "internal", "core", "domain", "testdata", "python-sdk-manifest.json")

	theirs, err := os.ReadFile(python)
	if err != nil {
		t.Skipf("python fixture not on disk at %s", python)
	}

	ours, err := os.ReadFile(fixturePath)
	require.NoError(t, err)

	var python_, go_ map[string]any

	require.NoError(t, json.Unmarshal(theirs, &python_))
	require.NoError(t, json.Unmarshal(ours, &go_))

	require.Equal(t, map[string]any{"language": "python", "version": "3.12"}, python_["runtime"])
	require.Equal(t, map[string]any{"language": "go", "version": "1.26"}, go_["runtime"])
	require.Equal(t, python_["v"], go_["v"])
	require.Equal(t, python_["workflow"], go_["workflow"])

	pythonNodes := python_["nodes"].([]any)
	goNodes := go_["nodes"].([]any)
	require.Len(t, goNodes, len(pythonNodes))

	for i := range pythonNodes {
		expected := pythonNodes[i].(map[string]any)
		expected["entrypoint"] = "app"
		require.Equal(t, expected, goNodes[i].(map[string]any), "node %d", i)
	}
}
