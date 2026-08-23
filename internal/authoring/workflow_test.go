package authoring

import (
	"encoding/json"
	"testing"

	"github.com/dagflows/sdk-go/internal/runtime"
	"github.com/stretchr/testify/require"
)

func extract(*runtime.Ctx, *runtime.Inputs) (any, error) {
	return map[string]any{}, nil
}

func first(*runtime.Ctx, *runtime.Inputs) (any, error) {
	return map[string]any{}, nil
}

func second(*runtime.Ctx, *runtime.Inputs) (any, error) {
	return map[string]any{}, nil
}

func only(*runtime.Ctx, *runtime.Inputs) (any, error) {
	return map[string]any{}, nil
}

type service struct {
	ran bool
}

func (s *service) Crunch(*runtime.Ctx, *runtime.Inputs) (any, error) {
	s.ran = true

	return map[string]any{}, nil
}

func manifestOf(t *testing.T, wf *Workflow) *Manifest {
	t.Helper()

	manifest, err := wf.Manifest()
	require.NoError(t, err)

	return manifest
}

func nodesOf(t *testing.T, wf *Workflow) map[string]*NodeManifest {
	t.Helper()

	out := map[string]*NodeManifest{}

	for _, node := range manifestOf(t, wf).Nodes {
		out[node.Key] = node
	}

	return out
}

func TestANodeIsRegisteredWithTheBinaryAsItsEntrypoint(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{
		Version: "1.26",
	})
	ref := wf.Node(extract, NodeOptions{})

	node := manifestOf(t, wf).Nodes[0]
	require.Equal(t, "extract", node.Key)
	require.Equal(t, "app", node.Entrypoint)
	require.Equal(t, "<NodeRef 'extract' app>", ref.String())
	require.False(t, ref.External())
}

func TestTheKeyComesFromTheFunctionNameOrTheOption(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	svc := &service{}

	require.Equal(t, "Crunch", wf.Node(svc.Crunch, NodeOptions{}).Key)
	require.Equal(t, "named", wf.Node(first, NodeOptions{Key: "named"}).Key)
	require.Equal(t, []string{"Crunch", "named"}, wf.Keys())
	require.False(t, svc.ran, "registration never runs the body")
}

func TestAnAnonymousHandlerNeedsAKey(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(func(*runtime.Ctx, *runtime.Inputs) (any, error) {
		return nil, nil
	}, NodeOptions{})

	_, err := wf.Manifest()
	require.ErrorContains(t, err, "cannot derive a node key")
	require.ErrorContains(t, err, "set Key")

	named := NewWorkflow("demo", WorkflowOptions{})
	named.Node(func(*runtime.Ctx, *runtime.Inputs) (any, error) {
		return nil, nil
	}, NodeOptions{Key: "anon"})
	require.Equal(t, []string{"anon"}, manifestOf(t, named).Keys())
}

func TestTheHandlerIsReachableForDispatch(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(first, NodeOptions{Key: "double"})

	fn, ok := wf.Handler("double")
	require.True(t, ok)

	out, err := fn(nil, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]any{}, out)

	_, ok = wf.Handler("absent")
	require.False(t, ok)
}

func TestDependsTakesHandlesAndBecomesKeys(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	a := wf.Node(first, NodeOptions{})
	wf.Node(second, NodeOptions{Depends: []*NodeRef{a}})

	nodes := nodesOf(t, wf)
	require.Empty(t, nodes["first"].Depends)
	require.Equal(t, []string{"first"}, nodes["second"].Depends)
}

func TestANilHandleInDependsIsRefused(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(second, NodeOptions{Depends: []*NodeRef{nil}})

	_, err := wf.Manifest()
	require.ErrorContains(t, err, "pass the handle")
}

func TestAnExternalNodeBecomesExternalDepends(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	crunch := wf.ExternalNode("crunch")
	wf.Node(first, NodeOptions{
		Key:     "report",
		Depends: []*NodeRef{crunch},
	})

	node := manifestOf(t, wf).Nodes[0]
	require.Equal(t, []string{"crunch"}, node.ExternalDepends)
	require.Empty(t, node.Depends)
	require.True(t, crunch.External())
	require.Equal(t, "<NodeRef 'crunch' external>", crunch.String())
}

func TestAnExternalNodeKeyIsStillChecked(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.ExternalNode("9lives")
	wf.Node(first, NodeOptions{})

	_, err := wf.Manifest()
	require.ErrorContains(t, err, "invalid")
}

func TestExecutionSettingsSplitBetweenTimeoutAndConfig(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(first, NodeOptions{
		Key: "heavy",
		Execution: &ExecutionConfig{
			Timeout:       30,
			MemoryLimitMB: 256,
			MilliCores:    500,
		},
	})

	node := manifestOf(t, wf).Nodes[0]
	require.Equal(t, 30, node.TimeoutSeconds)

	raw, err := json.Marshal(node.Config)
	require.NoError(t, err)
	require.JSONEq(t, `{"memory_limit_mb": 256, "milli_cores": 500}`, string(raw))
}

func TestTheDeclaredCeilingReachesTheManifest(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(first, NodeOptions{
		Execution: &ExecutionConfig{
			MaxOutputMB: 500,
		},
	})

	raw, err := json.Marshal(manifestOf(t, wf).Nodes[0].Config)
	require.NoError(t, err)
	require.Equal(t, `{"max_output_mb":500}`, string(raw))
}

func TestConfigKeepsTheAuthorsKeysSortedThenExecutionInManifestOrder(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(first, NodeOptions{
		Config: map[string]any{
			"zeta":        1,
			"alpha":       "x",
			"milli_cores": 1,
		},
		Execution: &ExecutionConfig{
			MemoryLimitMB: 256,
			MilliCores:    500,
			MaxOutputMB:   2,
		},
	})

	raw, err := json.Marshal(manifestOf(t, wf).Nodes[0].Config)
	require.NoError(t, err)
	require.Equal(t, `{"alpha":"x","milli_cores":500,"zeta":1,"memory_limit_mb":256,"max_output_mb":2}`, string(raw))
}

func TestRetryReachesTheManifest(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(first, NodeOptions{
		Key: "flaky",
		Retry: &RetryConfig{
			MaxAttempts:      3,
			InitialBackoffMs: 500,
		},
	})

	require.Equal(t, &RetryManifest{
		MaxAttempts:      3,
		InitialBackoffMs: 500,
	}, manifestOf(t, wf).Nodes[0].Retry)

	defaulted := NewWorkflow("demo", WorkflowOptions{})
	defaulted.Node(first, NodeOptions{
		Retry: &RetryConfig{
			MaxAttempts: 2,
		},
	})

	require.Equal(t, &RetryManifest{
		MaxAttempts:      2,
		InitialBackoffMs: 1000,
	}, manifestOf(t, defaulted).Nodes[0].Retry)
}

func TestNegativeSettingsAreRefusedByName(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(first, NodeOptions{
		Execution: &ExecutionConfig{
			MemoryLimitMB: -1,
		},
	})

	_, err := wf.Manifest()
	require.EqualError(t, err, "node 'first': memory_limit_mb cannot be negative, got -1")

	retry := NewWorkflow("demo", WorkflowOptions{})
	retry.Node(first, NodeOptions{
		Retry: &RetryConfig{
			MaxAttempts: -2,
		},
	})

	_, err = retry.Manifest()
	require.ErrorContains(t, err, "max_attempts cannot be negative, got -2")

	_, err = NewWorkflow("demo", WorkflowOptions{
		MaxCycleCount: -1,
	}).Manifest()
	require.ErrorContains(t, err, "workflow limits cannot be negative")
}

func TestADuplicateKeyIsRefused(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(first, NodeOptions{Key: "same"})
	wf.Node(second, NodeOptions{Key: "same"})

	_, err := wf.Manifest()
	require.ErrorContains(t, err, "duplicate node key 'same'")
}

func TestAnInvalidKeyIsRefusedHereNotByTheBuilder(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(first, NodeOptions{Key: "9lives"})

	_, err := wf.Manifest()
	require.ErrorContains(t, err, "node key '9lives' invalid")
}

func TestAnImageReferenceIsNotAVersion(t *testing.T) {
	_, err := NewWorkflow("demo", WorkflowOptions{
		Version: "golang:1.26-alpine",
	}).Manifest()

	require.ErrorContains(t, err, "not an image reference")
}

func TestTheWorkflowBlockIsOmittedWhenThisProjectIsNotTheOwner(t *testing.T) {
	wf := NewWorkflow("", WorkflowOptions{
		Version: "1.26",
	})
	wf.Node(only, NodeOptions{})

	manifest := manifestOf(t, wf)
	require.Nil(t, manifest.Workflow)
	require.Equal(t, RuntimeManifest{
		Language: "go",
		Version:  "1.26",
	}, manifest.Runtime)
}

func TestWorkflowSettingsAreEmittedWhenNamed(t *testing.T) {
	wf := NewWorkflow("branching-demo", WorkflowOptions{
		Version:            "1.26",
		MaxConcurrentNodes: 5,
		MaxCycleCount:      2,
	})
	wf.Node(only, NodeOptions{})

	require.Equal(t, &WorkflowManifest{
		Name:               "branching-demo",
		MaxConcurrentNodes: 5,
		MaxCycleCount:      2,
	}, manifestOf(t, wf).Workflow)
}

func TestAWorkflowWithNoNodesHasNothingToBuild(t *testing.T) {
	_, err := NewWorkflow("demo", WorkflowOptions{}).Manifest()
	require.ErrorContains(t, err, "no nodes")
}

func TestAHandleFromAnotherWorkflowIsRefused(t *testing.T) {
	other := NewWorkflow("other", WorkflowOptions{})
	elsewhere := other.Node(first, NodeOptions{Key: "elsewhere"})

	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(second, NodeOptions{
		Key:     "here",
		Depends: []*NodeRef{elsewhere},
	})
	wf.Node(only, NodeOptions{Key: "also"})

	_, err := wf.Manifest()
	require.EqualError(t, err, "node 'here' depends on 'elsewhere', which this workflow does not define; its nodes are: also, here")
}

func TestDeclaredListsEveryWorkflowInOrder(t *testing.T) {
	before := len(Declared())
	a := NewWorkflow("a", WorkflowOptions{})
	b := NewWorkflow("b", WorkflowOptions{})
	all := Declared()

	require.Len(t, all, before+2)
	require.Same(t, a, all[before])
	require.Same(t, b, all[before+1])
}

func TestTheManifestEncodesInPythonsKeyOrder(t *testing.T) {
	wf := NewWorkflow("branching-demo", WorkflowOptions{
		Version:            "1.26",
		MaxConcurrentNodes: 5,
	})
	step1 := wf.Node(first, NodeOptions{
		Key: "step_1",
		Execution: &ExecutionConfig{
			MemoryLimitMB: 256,
		},
	})
	step2 := wf.Node(second, NodeOptions{
		Key:     "step_2",
		Depends: []*NodeRef{step1},
		Execution: &ExecutionConfig{
			Timeout:       30,
			MemoryLimitMB: 256,
		},
		Retry: &RetryConfig{
			MaxAttempts:      2,
			InitialBackoffMs: 1000,
		},
	})
	wf.Node(only, NodeOptions{
		Key:  "report",
		Type: "task",
		Depends: []*NodeRef{
			step2,
			wf.ExternalNode("crunch"),
		},
	})

	encoded, err := manifestOf(t, wf).Encode()
	require.NoError(t, err)
	require.Equal(t, `{
  "v": 1,
  "runtime": {
    "language": "go",
    "version": "1.26"
  },
  "workflow": {
    "name": "branching-demo",
    "max_concurrent_nodes": 5
  },
  "nodes": [
    {
      "key": "step_1",
      "entrypoint": "app",
      "config": {
        "memory_limit_mb": 256
      }
    },
    {
      "key": "step_2",
      "entrypoint": "app",
      "depends": [
        "step_1"
      ],
      "timeout_seconds": 30,
      "config": {
        "memory_limit_mb": 256
      },
      "retry": {
        "max_attempts": 2,
        "initial_backoff_ms": 1000
      }
    },
    {
      "key": "report",
      "entrypoint": "app",
      "type": "task",
      "depends": [
        "step_2"
      ],
      "external_depends": [
        "crunch"
      ]
    }
  ]
}
`, string(encoded))
}
