// Command app provides an example node binary for integration testing.
package main

import (
	"encoding/json"
	"fmt"

	df "github.com/dagflows/sdk-go"
)

// count counts records received from the seed parent input.
func count(_ *df.Ctx, inputs *df.Inputs) (map[string]any, error) {
	fmt.Println("working")

	seed, err := inputs.Get("seed")
	if err != nil {
		return nil, err
	}

	rows := 0

	for _, err := range seed.Iter() {
		if err != nil {
			return nil, err
		}

		rows++
	}

	return map[string]any{"rows": rows}, nil
}

// compute performs arithmetic multiplication on the seed factor input.
func compute(_ *df.Ctx, inputs *df.Inputs) (map[string]any, error) {
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

type exported = df.Result[*df.Written]

// export streams processed rows to the output writer and determines next-hop routing.
func export(ctx *df.Ctx, inputs *df.Inputs) (exported, error) {
	seed, err := inputs.One()
	if err != nil {
		return exported{}, err
	}

	out := ctx.Out(df.NDJSON)
	defer out.Abort()

	written := 0

	for row, err := range seed.Iter() {
		if err != nil {
			return exported{}, err
		}

		if err := out.Write(row); err != nil {
			return exported{}, err
		}

		written++
	}

	if err := out.Close(); err != nil {
		return exported{}, err
	}

	ref, err := out.Ref()
	if err != nil {
		return exported{}, err
	}

	next := "process"
	if written == 0 {
		next = "empty"
	}

	return exported{
		Output: ref,
		Next:   []string{next},
	}, nil
}

func fails(*df.Ctx, df.None) (any, error) {
	return nil, &df.Fail{
		Message:  "upstream returned 503",
		Category: df.INFRASTRUCTURE,
		Abort:    new(false),
	}
}

func crashes(*df.Ctx, df.None) (any, error) {
	var rows []int

	return rows[3], nil
}

func version(*df.Ctx, df.None) (map[string]any, error) {
	return map[string]any{
		"sdk": df.Version(),
	}, nil
}

// The typed pair: a child's input type is its parent's output type, and the
// platform decodes the parent into it before the handler runs.
type Order struct {
	ID     int64 `json:"id"`
	Amount int64 `json:"amount"`
}

type Orders struct {
	Orders []Order `json:"orders"`
}

type Totals struct {
	Total int64 `json:"total"`
}

func fetchOrders(*df.Ctx, df.None) (Orders, error) {
	return Orders{Orders: []Order{{ID: 1, Amount: 100}, {ID: 2, Amount: 250}}}, nil
}

func total(_ *df.Ctx, orders Orders) (Totals, error) {
	var sum int64

	for _, order := range orders.Orders {
		sum += order.Amount
	}

	return Totals{Total: sum}, nil
}

func main() {
	wf := df.NewWorkflow("demo", df.WorkflowOptions{
		Version:            "1.27",
		MaxConcurrentNodes: 5,
	})

	counted := wf.Node(count, df.Depends(), df.NodeOptions{
		Execution: &df.Execution{
			Machine: "m",
		},
	})
	computed := wf.Node(compute, df.Depends(counted), df.NodeOptions{
		Execution: &df.Execution{
			Machine:     "m",
			TimeoutSecs: 30,
		},
		Retry: &df.Retry{
			MaxAttempts: new(2),
			RetryOn:     []df.RetryCategory{df.RetryOnInfrastructure, df.RetryOnTimeout},
		},
	})
	wf.NodeResult(export, df.Depends(computed, wf.ExternalNode("crunch")), df.NodeOptions{
		Key: "report",
		Transfer: &df.Transfer{
			MaxOutputMB: 64,
		},
	})
	wf.Node(fails, df.Root)
	wf.Node(crashes, df.Root)
	wf.Node(version, df.Root)

	fetched := wf.Node(fetchOrders, df.Root, df.NodeOptions{Key: "fetch_orders"})
	wf.Node(total, fetched)

	df.Main()
}
