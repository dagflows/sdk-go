package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/dagflows/sdk-go/internal/authoring"
	"github.com/dagflows/sdk-go/internal/runtime"
)

// ManifestFileName specifies the default build manifest file name.
const ManifestFileName = "dagflows-manifest.json"

func buildUsage() string {
	return fmt.Sprintf(`usage: %s build <command>

  manifest [-o %s]   emit the manifest the builder consumes
  manifest --check                    fail if the committed manifest is stale
  validate                            check the declaration, writing nothing

Both read the nodes this binary registered. No node body runs.`, invocation(), ManifestFileName)
}

func (m Main) build(argv []string, asJSON bool) error {
	if len(argv) == 0 {
		return misuse("%s", buildUsage())
	}

	switch command, rest := argv[0], argv[1:]; command {
	case "manifest":
		return m.manifest(rest, asJSON)

	case "validate":
		return m.validate(rest, asJSON)

	default:
		return misuse("unknown build command '%s'\n\n%s", command, buildUsage())
	}
}

func (m Main) manifest(argv []string, asJSON bool) error {
	argv, checking := takeFlag(argv, "--check")

	out, err := parseBuild(argv, true)
	if err != nil {
		return err
	}

	body, err := m.emitBody()
	if err != nil {
		return err
	}

	if checking {
		return m.check(body, out, asJSON)
	}

	encoded, err := body.Encode()
	if err != nil {
		return fail("%s", err)
	}

	if dir := filepath.Dir(out); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fail("%s", err)
		}
	}

	if err := os.WriteFile(out, encoded, 0o644); err != nil {
		return fail("%s", err)
	}

	m.emit(ordered{
		{"ok", true},
		{"path", out},
		{"nodes", body.Keys()},
	}, asJSON, fmt.Sprintf("wrote %s with %d node(s)", out, len(body.Nodes)))

	return nil
}

func (m Main) validate(argv []string, asJSON bool) error {
	if _, err := parseBuild(argv, false); err != nil {
		return err
	}

	body, err := m.emitBody()
	if err != nil {
		return err
	}

	keys := body.Keys()
	name := "this workflow"

	if body.Workflow != nil {
		name = fmt.Sprintf("workflow '%s'", body.Workflow.Name)
	}

	m.emit(ordered{
		{"ok", true},
		{"nodes", keys},
		{"runtime", body.Runtime},
	}, asJSON, fmt.Sprintf("%s is valid: %d node(s) - %s", name, len(keys), strings.Join(keys, ", ")))

	return nil
}

// check verifies the committed manifest file against current declarations.
func (m Main) check(body *authoring.Manifest, out string, asJSON bool) error {
	regenerate := fmt.Sprintf("%s build manifest", invocation())
	if out != ManifestFileName {
		regenerate += " -o " + out
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		return fail("%s does not exist yet, run: %s", out, regenerate)
	}

	committed, err := decodeObject(raw)
	if err != nil {
		return fail("%s is not valid json: %s", out, err)
	}

	encoded, err := body.Encode()
	if err != nil {
		return fail("%s", err)
	}

	fresh, _ := decodeObject(encoded)

	if reflect.DeepEqual(committed, fresh) {
		m.emit(ordered{
			{"ok", true},
			{"path", out},
			{"stale", false},
		}, asJSON, fmt.Sprintf("%s is up to date", out))

		return nil
	}

	lines := drift(committed, fresh)
	if asJSON {
		m.emit(ordered{
			{"ok", false},
			{"path", out},
			{"stale", true},
			{"drift", lines},
		}, true, "")

		return &commandError{
			code:     FAILED,
			reported: true,
		}
	}

	return fail("%s is stale:\n  %s\nrun: %s", out, strings.Join(lines, "\n  "), regenerate)
}

func decodeObject(raw []byte) (map[string]any, error) {
	value, err := runtime.DecodeJSON(raw)
	if err != nil {
		return nil, err
	}

	object, _ := value.(map[string]any)

	return object, nil
}

// drift detects discrepancies between committed and generated manifest schemas.
func drift(committed, fresh map[string]any) []string {
	was := nodesByKey(committed)
	now := nodesByKey(fresh)

	var lines []string

	for key := range now {
		if _, ok := was[key]; !ok {
			lines = append(lines, fmt.Sprintf("node '%s' was added", key))
		}
	}

	for key, before := range was {
		after, ok := now[key]

		switch {
		case !ok:
			lines = append(lines, fmt.Sprintf("node '%s' was removed", key))

		case !reflect.DeepEqual(before, after):
			lines = append(lines, fmt.Sprintf("node '%s' changed", key))
		}
	}

	if !reflect.DeepEqual(committed["runtime"], fresh["runtime"]) {
		lines = append(lines, fmt.Sprintf("runtime changed: %v -> %v", committed["runtime"], fresh["runtime"]))
	}

	if !reflect.DeepEqual(committed["workflow"], fresh["workflow"]) {
		lines = append(lines, "workflow settings changed")
	}

	slices.Sort(lines)

	if len(lines) == 0 {
		return []string{"the committed manifest differs from the source"}
	}

	return lines
}

func nodesByKey(manifest map[string]any) map[string]any {
	out := map[string]any{}
	nodes, _ := manifest["nodes"].([]any)

	for _, raw := range nodes {
		if node, ok := raw.(map[string]any); ok {
			out[runtime.Str(node["key"])] = node
		}
	}

	return out
}

// parseBuild parses the output path argument for build subcommands.
func parseBuild(argv []string, allowOut bool) (string, error) {
	out := ManifestFileName

	for i := 0; i < len(argv); i++ {
		item := argv[i]

		switch {
		case allowOut && (item == "-o" || item == "--out"):
			if i+1 >= len(argv) {
				return "", misuse("%s needs a path\n\n%s", item, buildUsage())
			}

			i++
			out = argv[i]

		default:
			return "", misuse("unknown argument '%s'\n\n%s", item, buildUsage())
		}
	}

	return out, nil
}

// emitBody retrieves and serializes the registered workflow manifest.
func (m Main) emitBody() (*authoring.Manifest, error) {
	switch len(m.Workflows) {
	case 0:
		return nil, fail("this binary declared no workflow, create one with dagflows.NewWorkflow(...) before calling dagflows.Main()")

	case 1:

	default:
		names := make([]string, 0, len(m.Workflows))

		for _, wf := range m.Workflows {
			name := wf.Name
			if name == "" {
				name = "<unnamed>"
			}

			names = append(names, "'"+name+"'")
		}

		return nil, fail("this binary declared %d workflows (%s), expected one", len(m.Workflows), strings.Join(names, ", "))
	}

	body, err := m.Workflows[0].Manifest()
	if err != nil {
		return nil, fail("%s", err)
	}

	return body, nil
}
