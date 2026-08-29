// Package cli implements the CLI entrypoint for local node execution, manifest generation, and debugging.
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dagflows/sdk-go/authoring"
	"github.com/dagflows/sdk-go/runtime"
)

// Process exit codes.
const (
	OK     = 0
	FAILED = 1
	USAGE  = 2
)

const help = `dagflows - author and run workflow nodes

  build manifest [-o dagflows-manifest.json] [--check]
  build validate
  dev   run <node> [--input <parent>=<file|json>] [options]
  dev   fixture <node> [-o fixture.json]

  --json    machine readable output, on any command above

Run them through the node binary: go run . <command> in the module, or
./app <command> once built. With DAGFLOWS_INPUT set and no command, the
binary runs the node the envelope names.

Exit codes: 0 success, 1 the operation failed, 2 the command was wrong.`

// Main encapsulates an invocation of the binary entrypoint.
type Main struct {
	Args      []string
	Workflows []*authoring.Workflow
	Stdout    io.Writer
	Stderr    io.Writer
}

// commandError represents a user-facing CLI error with an associated exit code.
type commandError struct {
	message  string
	code     int
	reported bool
}

func (e *commandError) Error() string {
	return e.message
}

func fail(format string, args ...any) error {
	return &commandError{
		message: fmt.Sprintf(format, args...),
		code:    FAILED,
	}
}

func misuse(format string, args ...any) error {
	return &commandError{
		message: fmt.Sprintf(format, args...),
		code:    USAGE,
	}
}

// Run executes the command routing logic and returns the corresponding exit code.
func (m Main) Run() int {
	if m.Stdout == nil {
		m.Stdout = os.Stdout
	}

	if m.Stderr == nil {
		m.Stderr = os.Stderr
	}

	if (len(m.Args) > 0 && m.Args[0] == "invoke") || (len(m.Args) == 0 && os.Getenv(runtime.InputEnv) != "") {
		return m.invoke(m.Args)
	}

	argv, asJSON := takeFlag(m.Args, "--json")
	if len(argv) == 0 || slices.Contains([]string{"-h", "--help", "help"}, argv[0]) {
		fmt.Fprintln(m.Stdout, help)
		if len(argv) == 0 {
			return USAGE
		}

		return OK
	}

	command, rest := argv[0], argv[1:]
	var err error

	switch command {
	case "build":
		err = m.build(rest, asJSON)

	case "dev":
		err = m.dev(rest, asJSON)

	default:
		err = misuse("unknown command '%s'\n\n%s", command, help)
	}

	if err != nil {
		return m.report(command, err, asJSON)
	}

	return OK
}

// report writes command failure output and returns the associated exit code.
func (m Main) report(command string, err error, asJSON bool) int {
	cmdErr, ok := err.(*commandError)
	if !ok {
		cmdErr = &commandError{
			message: err.Error(),
			code:    FAILED,
		}
	}

	switch {
	case cmdErr.reported:

	case asJSON:
		m.emit(ordered{
			{"ok", false},
			{"command", command},
			{"error", cmdErr.message},
		}, true, "")

	default:
		fmt.Fprintf(m.Stderr, "dagflows %s: %s\n", command, cmdErr.message)
	}

	return cmdErr.code
}

// emit prints either a structured JSON payload or plain text output.
func (m Main) emit(payload ordered, asJSON bool, human string) {
	if asJSON {
		body, _ := json.MarshalIndent(payload, "", "  ")
		m.Stdout.Write(append(body, '\n'))

		return
	}

	if human != "" {
		fmt.Fprintln(m.Stdout, human)
	}
}

func takeFlag(argv []string, name string) ([]string, bool) {
	rest := slices.DeleteFunc(slices.Clone(argv), func(item string) bool {
		return item == name
	})

	return rest, len(rest) != len(argv)
}

// ordered represents a JSON object preserving key insertion order.
type ordered []pair

type pair struct {
	key   string
	value any
}

func (o ordered) MarshalJSON() ([]byte, error) {
	var out bytes.Buffer

	out.WriteByte('{')

	for i, e := range o {
		if i > 0 {
			out.WriteByte(',')
		}

		key, _ := json.Marshal(e.key)
		value, err := json.Marshal(e.value)
		if err != nil {
			return nil, err
		}

		out.Write(key)
		out.WriteByte(':')
		out.Write(value)
	}

	out.WriteByte('}')

	return out.Bytes(), nil
}

// invocation returns a suitable command prefix for help hints and examples.
func invocation() string {
	exe, err := os.Executable()
	if err == nil && !strings.Contains(filepath.ToSlash(exe), "/go-build") && len(os.Args) > 0 {
		return os.Args[0]
	}

	return "go run ."
}
