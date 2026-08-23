package cli

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/dagflows/sdk-go/internal/authoring"
	"github.com/dagflows/sdk-go/internal/runtime"
)

func devUsage() string {
	return fmt.Sprintf(`usage: %s dev <command>

  run <node> [--input <parent>=<file|json>] [options]
      run a node locally: no VM, no platform, no network

      --input users=rows.ndjson     a file, content type inferred from the suffix
      --input users='{"n": 1}'      inline json
      --memory-limit-mb 512         what the node believes it has
      --inline-max-bytes 262144     when an output would offload
      --keep-fixture <path>         write the envelope instead of discarding it

  fixture <node> [-o fixture.json]
      write a starting fixture, so nobody has to invent one`, invocation())
}

// bySuffix maps an input file's suffix to its content type.
var bySuffix = map[string]runtime.ContentType{
	".ndjson": runtime.NDJSON,
	".jsonl":  runtime.NDJSON,
	".csv":    runtime.CSV,
	".json":   runtime.JSON,
	".txt":    runtime.TEXT,
}

type devOptions struct {
	node           string
	inputs         ordered
	memoryLimitMB  int
	inlineMaxBytes int
	keepFixture    string
	out            string
}

func (m Main) dev(argv []string, asJSON bool) error {
	if len(argv) == 0 {
		return misuse("%s", devUsage())
	}

	switch command, rest := argv[0], argv[1:]; command {
	case "run":
		return m.devRun(rest, asJSON)

	case "fixture":
		return m.devFixture(rest, asJSON)

	default:
		return misuse("unknown dev command '%s'\n\n%s", command, devUsage())
	}
}

// devRun executes the specified node in a local isolated child process.
func (m Main) devRun(argv []string, asJSON bool) error {
	options, err := m.parseDev(argv, false)
	if err != nil {
		return err
	}

	envelope, err := json.MarshalIndent(options.envelope(), "", "  ")
	if err != nil {
		return fail("%s", err)
	}

	scratch, err := os.MkdirTemp("", "dagflows-dev-")
	if err != nil {
		return fail("%s", err)
	}

	defer os.RemoveAll(scratch)

	inputPath := filepath.Join(scratch, "input.json")
	if options.keepFixture != "" {
		inputPath = options.keepFixture

		if dir := filepath.Dir(inputPath); dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fail("%s", err)
			}
		}
	}

	outputPath := filepath.Join(scratch, "output.json")
	if err := os.WriteFile(inputPath, envelope, 0o644); err != nil {
		return fail("%s", err)
	}

	self, err := os.Executable()
	if err != nil {
		return fail("cannot find this binary to run the node: %s", err)
	}

	child := exec.Command(self)
	child.Env = append(environment(), runtime.InputEnv+"="+inputPath, runtime.OutputEnv+"="+outputPath)

	var stdout, stderr bytes.Buffer

	child.Stdout, child.Stderr = &stdout, &stderr
	runErr := child.Run()

	body, readErr := os.ReadFile(outputPath)
	if readErr != nil {
		logs := strings.TrimSpace(stderr.String())
		if logs == "" {
			logs = strings.TrimSpace(stdout.String())
		}

		return fail("%s wrote no output.\nexit code %d\n%s", options.node, exitCode(runErr), logs)
	}

	result, err := decodeObject(body)
	if err != nil {
		return fail("%s wrote an output that is not a JSON envelope: %s", options.node, err)
	}

	return m.reportRun(options.node, result, stdout.String(), asJSON)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}

	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode()
	}

	return -1
}

// environment returns the current process environment without DAGFLOWS variables.
func environment() []string {
	return slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, runtime.InputEnv+"=") || strings.HasPrefix(kv, runtime.OutputEnv+"=")
	})
}

func (m Main) devFixture(argv []string, asJSON bool) error {
	options, err := m.parseDev(argv, true)
	if err != nil {
		return err
	}

	out := options.out
	if out == "" {
		out = "fixture.json"
	}

	envelope, err := json.MarshalIndent(options.envelope(), "", "  ")
	if err != nil {
		return fail("%s", err)
	}

	if err := os.WriteFile(out, append(envelope, '\n'), 0o644); err != nil {
		return fail("%s", err)
	}

	hint := fmt.Sprintf("%s=%s %s=out.json %s", runtime.InputEnv, out, runtime.OutputEnv, invocation())

	m.emit(ordered{
		{"ok", true},
		{"path", out},
		{"run", hint},
	}, asJSON, fmt.Sprintf("wrote %s\nrun it with: %s", out, hint))

	return nil
}

// reportRun formats and outputs the node execution result envelope.
func (m Main) reportRun(node string, result map[string]any, logs string, asJSON bool) error {
	status := "SUCCESS"

	if s, ok := result["status"].(string); ok {
		status = s
	}

	block, _ := result["output"].(map[string]any)

	if asJSON {
		m.emit(ordered{
			{"ok", status == "SUCCESS"},
			{"result", result},
			{"logs", logs},
		}, true, "")

		if status != "SUCCESS" {
			return &commandError{
				code:     FAILED,
				reported: true,
			}
		}

		return nil
	}

	if trimmed := strings.TrimSpace(logs); trimmed != "" {
		fmt.Fprintln(m.Stdout, strings.TrimRight(logs, "\r\n"))
	}

	if status != "SUCCESS" {
		failure, _ := result["error"].(map[string]any)
		category, message := "permanent", "no message"

		if c := runtime.Str(failure["category"]); c != "" {
			category = c
		}

		if msg := runtime.Str(failure["message"]); msg != "" {
			message = msg
		}

		fmt.Fprintf(m.Stderr, "\n%s -> FAILED (%s)\n  %s\n", node, category, message)

		if retry, ok := result["retry"]; ok {
			encoded, _ := json.Marshal(retry)
			fmt.Fprintf(m.Stderr, "  retry: %s\n", encoded)
		}

		return &commandError{
			code:     FAILED,
			reported: true,
		}
	}

	fmt.Fprintf(m.Stdout, "\n%s -> SUCCESS\n", node)

	if block["type"] == runtime.REFERENCE {
		fmt.Fprintf(m.Stdout, "  %s bytes uploaded as %s\n", runtime.Str(block["size"]), runtime.Str(block["content_type"]))
	} else {
		rendered, _ := json.MarshalIndent(block["data"], "", "  ")

		for line := range strings.SplitSeq(string(rendered), "\n") {
			fmt.Fprintln(m.Stdout, "  "+line)
		}
	}

	if routes, ok := result["next"].([]any); ok && len(routes) > 0 {
		names := make([]string, 0, len(routes))

		for _, route := range routes {
			names = append(names, runtime.Str(route))
		}

		fmt.Fprintf(m.Stdout, "  next: %s\n", strings.Join(names, ", "))
	}

	if stop, _ := result["stop"].(bool); stop {
		fmt.Fprintln(m.Stdout, "  stop: the branch halts here")
	}

	return nil
}

func (m Main) parseDev(argv []string, forFixture bool) (*devOptions, error) {
	options := &devOptions{
		memoryLimitMB:  512,
		inlineMaxBytes: runtime.DefaultInlineMaxBytes,
	}

	value := func(i *int) (string, error) {
		if *i+1 >= len(argv) {
			return "", misuse("%s needs a value\n\n%s", argv[*i], devUsage())
		}

		*i++

		return argv[*i], nil
	}

	for i := 0; i < len(argv); i++ {
		item := argv[i]
		var err error

		switch {
		case item == "--input":
			var spec string

			if spec, err = value(&i); err == nil {
				err = options.addInput(spec)
			}

		case item == "--memory-limit-mb":
			options.memoryLimitMB, err = intValue(&i, argv, value)

		case item == "--inline-max-bytes":
			options.inlineMaxBytes, err = intValue(&i, argv, value)

		case item == "--keep-fixture":
			options.keepFixture, err = value(&i)

		case (item == "-o" || item == "--out") && forFixture:
			options.out, err = value(&i)

		case strings.HasPrefix(item, "-"):
			err = misuse("unknown argument '%s'\n\n%s", item, devUsage())

		case options.node != "":
			err = misuse("name one node, got '%s' and '%s'", options.node, item)

		default:
			options.node = item
		}

		if err != nil {
			return nil, err
		}
	}

	if options.node == "" {
		return nil, misuse("name the node to run\n\n%s", devUsage())
	}

	if _, err := m.handler(options.node); err != nil {
		return nil, fail("%s", err)
	}

	return options, nil
}

func intValue(i *int, argv []string, value func(*int) (string, error)) (int, error) {
	raw, err := value(i)
	if err != nil {
		return 0, err
	}

	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, misuse("%s wants a number, got '%s'", argv[*i-1], raw)
	}

	return n, nil
}

// addInput parses parent=source into an input envelope item.
func (o *devOptions) addInput(spec string) error {
	name, source, found := strings.Cut(spec, "=")
	if !found || name == "" || source == "" {
		return misuse("--input wants <parent>=<file or json>, got '%s'", spec)
	}

	if info, err := os.Stat(source); err == nil && !info.IsDir() {
		contentType, ok := bySuffix[strings.ToLower(filepath.Ext(source))]
		if !ok {
			contentType = runtime.JSON
		}

		data, err := readInput(source, contentType)
		if err != nil {
			return fail("--input %s: %s", name, err)
		}

		o.inputs = append(o.inputs, entryOf(name, runtime.INLINE, contentType, data))

		return nil
	}

	data, err := runtime.DecodeJSON([]byte(source))
	if err != nil {
		return fail("--input %s: '%s' is neither a file that exists nor valid json", name, source)
	}

	contentType := runtime.JSON

	if _, isList := data.([]any); isList {
		contentType = runtime.NDJSON
	}

	o.inputs = append(o.inputs, entryOf(name, runtime.INLINE, contentType, data))

	return nil
}

func entryOf(name, kind string, contentType runtime.ContentType, data any) pair {
	return pair{
		key: name,
		value: ordered{
			{"type", kind},
			{"content_type", contentType},
			{"data", data},
		},
	}
}

// readInput reads and decodes an input fixture file based on its content type.
func readInput(path string, contentType runtime.ContentType) (any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	switch contentType {
	case runtime.NDJSON:
		rows := []any{}

		for number, line := range strings.Split(string(raw), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}

			row, err := runtime.DecodeJSON([]byte(line))
			if err != nil {
				return nil, fmt.Errorf("line %d is not valid JSON: %w", number+1, err)
			}

			rows = append(rows, row)
		}

		return rows, nil

	case runtime.CSV:
		records, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
		if err != nil {
			return nil, err
		}

		rows := []any{}

		if len(records) == 0 {
			return rows, nil
		}

		for _, record := range records[1:] {
			row := ordered{}

			for i, column := range records[0] {
				cell := any(nil)
				if i < len(record) {
					cell = record[i]
				}

				row = append(row, pair{column, cell})
			}

			rows = append(rows, row)
		}

		return rows, nil

	case runtime.TEXT:
		return string(raw), nil

	default:
		return runtime.DecodeJSON(raw)
	}
}

// envelope constructs the simulated input envelope for local execution.
func (o *devOptions) envelope() ordered {
	inputs := o.inputs
	if inputs == nil {
		inputs = ordered{}
	}

	return ordered{
		{"ctx", ordered{
			{"workflow_run_id", "local"},
			{"node_key", o.node},
			{"language", authoring.Language},
			{"entrypoint", authoring.Entrypoint},
			{"config", map[string]any{}},
			{"timeout_seconds", 0},
			{"attempt", 0},
			{"memory_limit_mb", o.memoryLimitMB},
			{"inline_max_bytes", o.inlineMaxBytes},
		}},
		{"payload", ordered{
			{"inputs", inputs},
		}},
	}
}
