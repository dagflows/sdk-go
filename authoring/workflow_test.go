package authoring

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/dagflows/sdk-go/runtime"
	"github.com/stretchr/testify/require"
)

func extract(*runtime.Ctx, runtime.None) (any, error) {
	return map[string]any{}, nil
}

func first(*runtime.Ctx, runtime.None) (any, error) {
	return map[string]any{}, nil
}

func second(*runtime.Ctx, *runtime.Inputs) (any, error) {
	return map[string]any{}, nil
}

func only(*runtime.Ctx, runtime.None) (any, error) {
	return map[string]any{}, nil
}

type service struct {
	ran bool
}

func (s *service) Crunch(*runtime.Ctx, runtime.None) (any, error) {
	s.ran = true

	return map[string]any{}, nil
}

// The typed graph the io tests declare.
type (
	order struct {
		ID     int64  `json:"id"`
		Amount int64  `json:"amount"`
		Note   string `json:"note,omitempty"`
	}
	orders struct {
		Orders []order    `json:"orders"`
		Placed *time.Time `json:"placed"`
	}
	totals struct {
		Total int64 `json:"total"`
	}
	line struct {
		ID    int64 `json:"id"`
		Cents int64 `json:"cents"`
	}
)

func fetchOrders(*runtime.Ctx, runtime.None) (orders, error) {
	return orders{}, nil
}

func calculateTotals(_ *runtime.Ctx, in orders) (totals, error) {
	var total int64
	for _, o := range in.Orders {
		total += o.Amount
	}

	return totals{Total: total}, nil
}

func exportLines(_ *runtime.Ctx, in orders) (runtime.Rows[line], error) {
	return func(yield func(line, error) bool) {
		for _, o := range in.Orders {
			if !yield(line{ID: o.ID, Cents: o.Amount * 100}, nil) {
				return
			}
		}
	}, nil
}

func peekOrders(_ *runtime.Ctx, in *runtime.Input[orders]) (int64, error) {
	return in.Size(), nil
}

func countLines(_ *runtime.Ctx, lines runtime.Rows[line]) (int64, error) {
	var n int64
	for _, err := range lines {
		if err != nil {
			return 0, err
		}
		n++
	}

	return n, nil
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

func encoded(t *testing.T, value any) string {
	t.Helper()

	raw, err := json.Marshal(value)
	require.NoError(t, err)

	return string(raw)
}

func TestANodeIsRegisteredWithTheBinaryAsItsEntrypoint(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{
		Version: "1.27",
	})
	ref := wf.Node(extract, runtime.Root)

	node := manifestOf(t, wf).Nodes[0]
	require.Equal(t, "extract", node.Key)
	require.Equal(t, "app", node.Entrypoint)
	require.Equal(t, "<NodeRef 'extract'>", ref.String())
	require.False(t, ref.External())
}

func TestTheKeyComesFromTheFunctionNameOrTheOption(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	svc := &service{}

	require.Equal(t, "Crunch", wf.Node(svc.Crunch, runtime.Root).Key())
	require.Equal(t, "named", wf.Node(first, runtime.Root, NodeOptions{Key: "named"}).Key())
	require.Equal(t, []string{"Crunch", "named"}, wf.Keys())
	require.False(t, svc.ran, "registration never runs the body")
}

func TestAnAnonymousHandlerNeedsAKey(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(func(*runtime.Ctx, runtime.None) (any, error) {
		return nil, nil
	}, runtime.Root)

	_, err := wf.Manifest()
	require.ErrorContains(t, err, "cannot derive a node key")
	require.ErrorContains(t, err, "set Key")

	named := NewWorkflow("demo", WorkflowOptions{})
	named.Node(func(*runtime.Ctx, runtime.None) (any, error) {
		return nil, nil
	}, runtime.Root, NodeOptions{Key: "anon"})
	require.Equal(t, []string{"anon"}, manifestOf(t, named).Keys())
}

func TestTheHandlerIsReachableForDispatch(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(first, runtime.Root, NodeOptions{Key: "double"})

	fn, ok := wf.Handler("double")
	require.True(t, ok)

	out, err := fn(nil, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]any{}, out)

	_, ok = wf.Handler("absent")
	require.False(t, ok)
}

// Tests that dispatch decodes the parent into the handler's type, and that
// a parent which does not fit is refused naming the field.
func TestDispatchDecodesTheParentIntoTheHandlersType(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	fetched := wf.Node(fetchOrders, runtime.Root, NodeOptions{Key: "fetch_orders"})
	wf.Node(calculateTotals, fetched, NodeOptions{Key: "calculate_totals"})

	fn, ok := wf.Handler("calculate_totals")
	require.True(t, ok)

	out, err := fn(nil, runtime.NewInputs(map[string]any{
		"fetch_orders": map[string]any{
			"type":         "INLINE",
			"content_type": "application/json",
			"data":         map[string]any{"orders": []any{map[string]any{"id": 1, "amount": 100}, map[string]any{"id": 2, "amount": 250}}},
		},
	}, 0))
	require.NoError(t, err)
	require.Equal(t, totals{Total: 350}, out)

	_, err = fn(nil, runtime.NewInputs(map[string]any{
		"fetch_orders": map[string]any{
			"type":         "INLINE",
			"content_type": "application/json",
			"data":         map[string]any{"orders": []any{map[string]any{"id": "one"}}},
		},
	}, 0))
	require.ErrorContains(t, err, "fetch_orders")
	require.ErrorContains(t, err, "id")

	_, err = fn(nil, runtime.NewInputs(map[string]any{}, 0))
	require.ErrorContains(t, err, "no input named 'fetch_orders'")
}

func TestDependsTakesHandlesAndBecomesKeys(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	a := wf.Node(first, runtime.Root)
	wf.Node(second, runtime.Depends(a))

	nodes := nodesOf(t, wf)
	require.Empty(t, nodes["first"].Depends)
	require.Equal(t, []string{"first"}, nodes["second"].Depends)
}

func TestAParentHandleIsTheEdgeOfAOneParentNode(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	fetched := wf.Node(fetchOrders, runtime.Root, NodeOptions{Key: "fetch_orders"})
	wf.Node(calculateTotals, fetched, NodeOptions{Key: "calculate_totals"})
	wf.Node(peekOrders, runtime.Lazy(fetched), NodeOptions{Key: "peek"})

	nodes := nodesOf(t, wf)
	require.Equal(t, []string{"fetch_orders"}, nodes["calculate_totals"].Depends)
	require.Equal(t, []string{"fetch_orders"}, nodes["peek"].Depends)
}

func TestANilHandleInDependsIsRefused(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(second, runtime.Depends(nil))

	_, err := wf.Manifest()
	require.ErrorContains(t, err, "pass the handle")

	typed := NewWorkflow("demo", WorkflowOptions{})
	var absent *runtime.NodeRef[orders]
	typed.Node(calculateTotals, absent)

	_, err = typed.Manifest()
	require.ErrorContains(t, err, "pass the handle")

	var edge runtime.Edge[*runtime.Inputs]
	untyped := NewWorkflow("demo", WorkflowOptions{})
	untyped.Node(second, edge)

	_, err = untyped.Manifest()
	require.ErrorContains(t, err, "pass the handle")
}

func TestAnExternalNodeBecomesExternalDepends(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	crunch := wf.ExternalNode("crunch")
	wf.Node(second, runtime.Depends(crunch), NodeOptions{
		Key: "report",
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
	wf.Node(first, runtime.Root)

	_, err := wf.Manifest()
	require.ErrorContains(t, err, "invalid")
}

func TestExecutionSettingsSplitBetweenTheBlockAndConfig(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(first, runtime.Root, NodeOptions{
		Key: "heavy",
		Execution: &Execution{
			Machine:     "gp-8",
			TimeoutSecs: 30,
		},
		Transfer: &Transfer{
			MaxOutputMB:     64,
			ConnTimeoutSecs: 5,
		},
	})

	node := manifestOf(t, wf).Nodes[0]
	require.NotNil(t, node.Execution)
	require.Equal(t, "gp-8", node.Execution.Machine)
	require.Equal(t, int64(30_000), *node.Execution.TimeoutMs)

	require.NotNil(t, node.Transfer)
	require.Equal(t, int64(64), *node.Transfer.MaxOutputMB)
	require.Equal(t, int64(5_000), *node.Transfer.ConnTimeoutMs)
	require.Nil(t, node.Transfer.IdleTimeoutMs)

	require.JSONEq(t, `{}`, encoded(t, node.Config))
}

func TestANodeAskingForNothingStatesNoExecutionBlock(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(first, runtime.Root, NodeOptions{Key: "plain"})

	require.Nil(t, manifestOf(t, wf).Nodes[0].Execution)
}

func TestAReservedGPUIsRefusedWhereItIsWritten(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(first, runtime.Root, NodeOptions{Key: "g", Execution: &Execution{GPU: "a100"}})

	_, err := wf.Manifest()
	require.ErrorContains(t, err, "gpu")
}

func TestTheDeclaredCeilingReachesTheManifest(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(first, runtime.Root, NodeOptions{
		Transfer: &Transfer{
			MaxOutputMB: 500,
		},
	})

	node := manifestOf(t, wf).Nodes[0]
	require.NotNil(t, node.Transfer)
	require.Equal(t, int64(500), *node.Transfer.MaxOutputMB)
}

func TestConfigKeepsTheAuthorsKeysSorted(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(first, runtime.Root, NodeOptions{
		Config: map[string]any{
			"zeta":        1,
			"alpha":       "x",
			"milli_cores": 1,
		},
	})

	// The author's own keys are all that reaches config now, sorted.
	require.Equal(t, `{"alpha":"x","milli_cores":1,"zeta":1}`, encoded(t, manifestOf(t, wf).Nodes[0].Config))
}

func TestRetryReachesTheManifest(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(first, runtime.Root, NodeOptions{
		Key: "flaky",
		Retry: &Retry{
			MaxAttempts:      new(3),
			InitialBackoffMs: new(500),
		},
	})

	require.Equal(t, &RetryManifest{
		MaxAttempts:      new(3),
		InitialBackoffMs: new(500),
	}, manifestOf(t, wf).Nodes[0].Retry)

	// Unset fields remain nil so platform and workflow defaults can be applied.
	partial := NewWorkflow("demo", WorkflowOptions{})
	partial.Node(first, runtime.Root, NodeOptions{
		Retry: &Retry{
			MaxAttempts: new(2),
		},
	})

	require.Equal(t, &RetryManifest{
		MaxAttempts: new(2),
	}, manifestOf(t, partial).Nodes[0].Retry)
}

// Tests that a workflow default reaches the manifest for the platform to merge.
func TestWorkflowRetryDefaultReachesTheManifest(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{
		Retry: &Retry{
			MaxAttempts: new(4),
			RetryOn:     []RetryCategory{RetryOnInfrastructure, RetryOnTimeout},
		},
	})
	wf.Node(first, runtime.Root, NodeOptions{Key: "flaky"})

	manifest := manifestOf(t, wf)

	require.Equal(t, &RetryManifest{
		MaxAttempts: new(4),
		RetryOn:     []string{"infrastructure", "timeout"},
	}, manifest.Workflow.Retry)

	// Unconfigured node retry remains nil so platform-level merging applies defaults.
	require.Nil(t, manifest.Nodes[0].Retry)
}

// Tests that a category the platform would never retry is refused at declaration.
func TestRetryOnRefusesWhatCannotBeRetried(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(first, runtime.Root, NodeOptions{Retry: &Retry{RetryOn: []RetryCategory{"permanent"}}})

	_, err := wf.Manifest()
	require.ErrorContains(t, err, "a retry cannot help")

	unknown := NewWorkflow("demo", WorkflowOptions{})
	unknown.Node(first, runtime.Root, NodeOptions{Retry: &Retry{RetryOn: []RetryCategory{"flaky"}}})

	_, err = unknown.Manifest()
	require.ErrorContains(t, err, "unknown category")
}

func TestNegativeSettingsAreRefusedByName(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(first, runtime.Root, NodeOptions{
		Execution: &Execution{
			TimeoutSecs: -1,
		},
	})

	_, err := wf.Manifest()
	require.EqualError(t, err, "node 'first': timeout_secs cannot be negative, got -1")

	retry := NewWorkflow("demo", WorkflowOptions{})
	retry.Node(first, runtime.Root, NodeOptions{
		Retry: &Retry{
			MaxAttempts: new(-2),
		},
	})

	// Anything under one attempt would leave the node never running, which is
	// a different mistake from a negative backoff and says so.
	_, err = retry.Manifest()
	require.ErrorContains(t, err, "would never run the node")

	_, err = NewWorkflow("demo", WorkflowOptions{
		MaxCycleCount: -1,
	}).Manifest()
	require.ErrorContains(t, err, "workflow limits cannot be negative")
}

func TestADuplicateKeyIsRefused(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(first, runtime.Root, NodeOptions{Key: "same"})
	wf.Node(second, runtime.Depends(), NodeOptions{Key: "same"})

	_, err := wf.Manifest()
	require.ErrorContains(t, err, "duplicate node key 'same'")

	// A trigger shares the namespace: the builder would otherwise see two
	// things called 'same'.
	shared := NewWorkflow("demo", WorkflowOptions{})
	shared.Node(first, runtime.Root, NodeOptions{Key: "same"})
	shared.Trigger[any]("same")

	_, err = shared.Manifest()
	require.ErrorContains(t, err, "duplicate node key 'same'")
}

func TestAnInvalidKeyIsRefusedHereNotByTheBuilder(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(first, runtime.Root, NodeOptions{Key: "9lives"})

	_, err := wf.Manifest()
	require.ErrorContains(t, err, "node key '9lives' invalid")
}

func TestAnImageReferenceIsNotAVersion(t *testing.T) {
	_, err := NewWorkflow("demo", WorkflowOptions{
		Version: "golang:1.27-alpine",
	}).Manifest()

	require.ErrorContains(t, err, "not an image reference")
}

func TestTheWorkflowBlockIsOmittedWhenThisProjectIsNotTheOwner(t *testing.T) {
	wf := NewWorkflow("", WorkflowOptions{
		Version: "1.27",
	})
	wf.Node(only, runtime.Root)

	manifest := manifestOf(t, wf)
	require.Nil(t, manifest.Workflow)
	require.Equal(t, RuntimeManifest{
		Language: "go",
		Version:  "1.27",
	}, manifest.Runtime)
}

func TestWorkflowSettingsAreEmittedWhenNamed(t *testing.T) {
	wf := NewWorkflow("branching-demo", WorkflowOptions{
		Version:            "1.27",
		MaxConcurrentNodes: 5,
		MaxCycleCount:      2,
	})
	wf.Node(only, runtime.Root)

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
	elsewhere := other.Node(first, runtime.Root, NodeOptions{Key: "elsewhere"})

	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(second, runtime.Depends(elsewhere), NodeOptions{
		Key: "here",
	})
	wf.Node(only, runtime.Root, NodeOptions{Key: "also"})

	_, err := wf.Manifest()
	require.EqualError(t, err, "node 'here' depends on 'elsewhere', which this workflow does not define; its nodes are: also, here")
}

func TestATriggerFromAnotherWorkflowIsRefused(t *testing.T) {
	other := NewWorkflow("other", WorkflowOptions{})
	placed := other.Trigger[any]("order_placed")

	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(second, runtime.Depends(placed), NodeOptions{Key: "on_order"})

	_, err := wf.Manifest()
	require.ErrorContains(t, err, "node 'on_order' is triggered by 'order_placed', which this workflow does not declare")
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
		Version:            "1.27",
		MaxConcurrentNodes: 5,
	})
	step1 := wf.Node(first, runtime.Root, NodeOptions{
		Key: "step_1",
		Execution: &Execution{
			Machine: "gp-4",
		},
	})
	step2 := wf.Node(second, runtime.Depends(step1), NodeOptions{
		Key: "step_2",
		Execution: &Execution{
			Machine:     "gp-4",
			TimeoutSecs: 30,
		},
		Retry: &Retry{
			MaxAttempts:      new(2),
			InitialBackoffMs: new(1000),
		},
	})
	wf.Node(second, runtime.Depends(step2, wf.ExternalNode("crunch")), NodeOptions{
		Key:  "report",
		Type: "task",
	})

	encoded, err := manifestOf(t, wf).Encode()
	require.NoError(t, err)
	require.Equal(t, `{
  "v": 1,
  "runtime": {
    "language": "go",
    "version": "1.27"
  },
  "workflow": {
    "name": "branching-demo",
    "max_concurrent_nodes": 5
  },
  "nodes": [
    {
      "key": "step_1",
      "entrypoint": "app",
      "execution": {
        "machine": "gp-4"
      },
      "io": {
        "output": {
          "shape": "value",
          "content_type": "application/json",
          "schema": {}
        }
      }
    },
    {
      "key": "step_2",
      "entrypoint": "app",
      "depends": [
        "step_1"
      ],
      "retry": {
        "max_attempts": 2,
        "initial_backoff_ms": 1000
      },
      "execution": {
        "machine": "gp-4",
        "timeout_ms": 30000
      },
      "io": {
        "output": {
          "shape": "value",
          "content_type": "application/json",
          "schema": {}
        }
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
      ],
      "io": {
        "output": {
          "shape": "value",
          "content_type": "application/json",
          "schema": {}
        }
      }
    }
  ]
}
`, string(encoded))
}

// Tests that the handler's Go types reach the manifest as the node's
// expectations of its parent and its own output, in declaration order.
func TestTypedHandlersEmitTheirIO(t *testing.T) {
	wf := NewWorkflow("typed", WorkflowOptions{})
	fetched := wf.Node(fetchOrders, runtime.Root, NodeOptions{Key: "fetch_orders"})
	wf.Node(calculateTotals, fetched, NodeOptions{Key: "calculate_totals"})
	exported := wf.Node(exportLines, fetched, NodeOptions{Key: "export_lines"})
	wf.Node(countLines, exported, NodeOptions{Key: "count_lines"})

	nodes := nodesOf(t, wf)

	ordersSchema := `{"type":"object","title":"orders","properties":{` +
		`"orders":{"type":"array","items":{"type":"object","title":"order","properties":{` +
		`"id":{"type":"integer","format":"int64"},"amount":{"type":"integer","format":"int64"},"note":{"type":"string"}},` +
		`"required":["id","amount"]}},` +
		`"placed":{"type":["string","null"],"format":"date-time"}},"required":["orders"]}`
	lineSchema := `{"type":"object","title":"line","properties":{"id":{"type":"integer","format":"int64"},"cents":{"type":"integer","format":"int64"}},"required":["id","cents"]}`

	require.Equal(t,
		`{"output":{"shape":"value","content_type":"application/json","schema":`+ordersSchema+`}}`,
		encoded(t, nodes["fetch_orders"].IO))

	require.Equal(t,
		`{"inputs":{"fetch_orders":{"shape":"value","schema":`+ordersSchema+`}},`+
			`"output":{"shape":"value","content_type":"application/json","schema":{"type":"object","title":"totals","properties":{"total":{"type":"integer","format":"int64"}},"required":["total"]}}}`,
		encoded(t, nodes["calculate_totals"].IO))

	require.Equal(t,
		`{"inputs":{"fetch_orders":{"shape":"value","schema":`+ordersSchema+`}},`+
			`"output":{"shape":"rows","content_type":"application/x-ndjson","schema":`+lineSchema+`}}`,
		encoded(t, nodes["export_lines"].IO))

	require.Equal(t,
		`{"inputs":{"export_lines":{"shape":"rows","schema":`+lineSchema+`}},`+
			`"output":{"shape":"value","content_type":"application/json","schema":{"type":"integer","format":"int64"}}}`,
		encoded(t, nodes["count_lines"].IO))
}

// Tests that a node reading the bag states only what it declared of nodes it
// cannot see: a typed external node. A local parent's type is never copied.
func TestTheBagStatesOnlyDeclaredExpectations(t *testing.T) {
	wf := NewWorkflow("typed", WorkflowOptions{})
	fetched := wf.Node(fetchOrders, runtime.Root, NodeOptions{Key: "fetch_orders"})
	crunch := wf.External[totals]("crunch")
	anything := wf.ExternalNode("legacy")
	wf.Node(second, runtime.Depends(fetched, crunch, anything), NodeOptions{Key: "report"})

	node := nodesOf(t, wf)["report"]
	require.Equal(t, []string{"fetch_orders"}, node.Depends)
	require.Equal(t, []string{"crunch", "legacy"}, node.ExternalDepends)
	require.Equal(t,
		`{"inputs":{"crunch":{"shape":"value","schema":{"type":"object","title":"totals","properties":{"total":{"type":"integer","format":"int64"}},"required":["total"]}}},`+
			`"output":{"shape":"value","content_type":"application/json","schema":{}}}`,
		encoded(t, node.IO))
}

// Tests that a lazy handle states the expectation of its element type, and
// that a typed handle carries the same schema as the value.
func TestALazyHandleStatesItsElementType(t *testing.T) {
	wf := NewWorkflow("typed", WorkflowOptions{})
	fetched := wf.Node(fetchOrders, runtime.Root, NodeOptions{Key: "fetch_orders"})
	wf.Node(func(_ *runtime.Ctx, in *runtime.Input[totals]) (int64, error) {
		return in.Size(), nil
	}, runtime.Lazy(wf.External[totals]("crunch")), NodeOptions{Key: "peek"})
	wf.Node(func(_ *runtime.Ctx, in *runtime.Input[any]) (int64, error) {
		return in.Size(), nil
	}, runtime.Lazy(wf.ExternalNode("legacy")), NodeOptions{Key: "size"})
	_ = fetched

	nodes := nodesOf(t, wf)
	require.Contains(t, encoded(t, nodes["peek"].IO), `"inputs":{"crunch":{"shape":"value","schema":{"type":"object","title":"totals"`)
	require.NotContains(t, encoded(t, nodes["size"].IO), `"inputs"`, "an untyped handle expects nothing")
}

func TestATriggerIsATypedSourceAndItsNodesAreTriggeredByIt(t *testing.T) {
	type orderPlaced struct {
		OrderID string `json:"order_id"`
		Amount  int64  `json:"amount"`
	}

	wf := NewWorkflow("events", WorkflowOptions{})
	placed := wf.Trigger[orderPlaced]("order_placed")
	wf.Node(func(_ *runtime.Ctx, event orderPlaced) (map[string]any, error) {
		return map[string]any{"received": event.OrderID}, nil
	}, placed, NodeOptions{Key: "on_order"})
	wf.Trigger[any]("tick")

	manifest := manifestOf(t, wf)
	require.Equal(t,
		`[{"key":"order_placed","schema":{"type":"object","title":"orderPlaced","properties":{"order_id":{"type":"string"},"amount":{"type":"integer","format":"int64"}},"required":["order_id","amount"]}},{"key":"tick"}]`,
		encoded(t, manifest.Triggers))

	node := manifest.Nodes[0]
	require.Equal(t, []string{"order_placed"}, node.TriggeredBy)
	require.Empty(t, node.Depends)
	require.True(t, placed.Trigger())
	require.Equal(t, "<NodeRef 'order_placed' trigger>", placed.String())
	require.Contains(t, encoded(t, node.IO), `"inputs":{"order_placed":{"shape":"value","schema":{"type":"object","title":"orderPlaced"`)
}

func TestModeReachesTheManifestOnlyWhenItIsNotOnce(t *testing.T) {
	wf := NewWorkflow("stream", WorkflowOptions{})
	fetched := wf.Node(fetchOrders, runtime.Root, NodeOptions{Key: "fetch_orders"})
	wf.Node(calculateTotals, fetched, NodeOptions{Key: "once", Mode: ModeOnce})
	wf.Node(calculateTotals, fetched, NodeOptions{Key: "live", Mode: ModeStream})

	nodes := nodesOf(t, wf)
	require.Empty(t, nodes["fetch_orders"].Mode)
	require.Empty(t, nodes["once"].Mode)
	require.Equal(t, "stream", nodes["live"].Mode)

	bad := NewWorkflow("stream", WorkflowOptions{})
	bad.Node(first, runtime.Root, NodeOptions{Mode: "forever"})

	_, err := bad.Manifest()
	require.ErrorContains(t, err, "mode 'forever' is not a mode")
}

// Tests that a type the profile cannot carry is refused when the node is
// declared, naming the field, rather than emitted as a half schema.
func TestATypeTheManifestCannotCarryIsRefusedByName(t *testing.T) {
	type node struct {
		Next *node `json:"next"`
	}

	wf := NewWorkflow("bad", WorkflowOptions{})
	wf.Node(func(*runtime.Ctx, runtime.None) (node, error) {
		return node{}, nil
	}, runtime.Root, NodeOptions{Key: "recursive"})

	_, err := wf.Manifest()
	require.ErrorContains(t, err, "node 'recursive'")
	require.ErrorContains(t, err, "refers to itself")

	type keyed struct {
		Counts map[int]int `json:"counts"`
	}

	maps := NewWorkflow("bad", WorkflowOptions{})
	maps.Node(func(*runtime.Ctx, runtime.None) (keyed, error) {
		return keyed{}, nil
	}, runtime.Root, NodeOptions{Key: "intkeys"})

	_, err = maps.Manifest()
	require.ErrorContains(t, err, "keyed.Counts")
	require.ErrorContains(t, err, "non-string keys")
}

// Tests that configured on_warning policy settings serialize to the build manifest.
func TestOnWarningReachesTheManifest(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{OnWarning: OnWarningReject})
	wf.Node(first, runtime.Root)

	require.Equal(t, OnWarningReject, manifestOf(t, wf).Workflow.OnWarning)
}

func TestAWorkflowStatingNoPolicyEmitsNone(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{})
	wf.Node(first, runtime.Root)

	require.Empty(t, manifestOf(t, wf).Workflow.OnWarning)
}

func TestAnUnknownOnWarningPolicyIsRefused(t *testing.T) {
	wf := NewWorkflow("demo", WorkflowOptions{OnWarning: "Reject"})
	wf.Node(first, runtime.Root)

	_, err := wf.Manifest()
	require.ErrorContains(t, err, "not a policy")
}
