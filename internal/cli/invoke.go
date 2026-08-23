package cli

import (
	"fmt"
	"strings"

	"github.com/dagflows/sdk-go/internal/runtime"
)

const invokeUsage = "usage: ./app [invoke [--node <key>]] with DAGFLOWS_INPUT and DAGFLOWS_OUTPUT set"

// invoke executes the node indicated in the input envelope and reports outcomes.
func (m Main) invoke(argv []string) int {
	err := m.dispatch(argv)
	if err == nil {
		return OK
	}

	if reportErr := runtime.Report(err); reportErr != nil {
		fmt.Fprintf(m.Stderr, "dagflows invoke: %s\n(while reporting: %s)\n", err, reportErr)

		return FAILED
	}

	return OK
}

func (m Main) dispatch(argv []string) error {
	if _, err := parseInvoke(argv); err != nil {
		return err
	}

	ctx, inputs, err := runtime.Load("")
	if err != nil {
		return err
	}

	if ctx.NodeKey == "" {
		return &runtime.Fail{
			Message: "ctx.node_key is empty, the platform did not say which node to run",
		}
	}

	handler, err := m.handler(ctx.NodeKey)
	if err != nil {
		return err
	}

	return runtime.Write(runtime.Execute(handler, ctx, inputs), "")
}

// parseInvoke parses the optional --node flag from command line arguments.
func parseInvoke(argv []string) (string, error) {
	if len(argv) > 0 && argv[0] == "invoke" {
		argv = argv[1:]
	}

	node := ""

	for i := 0; i < len(argv); i++ {
		if argv[i] != "--node" {
			return "", &runtime.Fail{
				Message: fmt.Sprintf("unknown argument '%s', %s", argv[i], invokeUsage),
			}
		}

		if i+1 >= len(argv) {
			return "", &runtime.Fail{
				Message: "--node needs a value, " + invokeUsage,
			}
		}

		i++
		node = argv[i]
	}

	return node, nil
}

// handler locates the registered handler for the given node key.
func (m Main) handler(key string) (runtime.Handler, error) {
	var registered []string

	for _, wf := range m.Workflows {
		if fn, ok := wf.Handler(key); ok {
			return fn, nil
		}

		registered = append(registered, wf.Keys()...)
	}

	available := "none"

	if len(registered) > 0 {
		available = strings.Join(registered, ", ")
	}

	return nil, &runtime.Fail{
		Message: fmt.Sprintf("no node registered as '%s', this binary registers: %s", key, available),
	}
}
