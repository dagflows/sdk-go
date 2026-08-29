package dagflows_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var (
	exampleBinary string
	shapesBinary  string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "dagflows-e2e-")
	if err != nil {
		panic(err)
	}

	exampleBinary = buildModule(dir, "example")
	shapesBinary = buildModule(dir, "shapes")

	code := m.Run()

	os.RemoveAll(dir)
	os.Exit(code)
}

func buildModule(dir, name string) string {
	out := filepath.Join(dir, name+exeSuffix())

	build := exec.Command("go", "build", "-o", out, ".")
	build.Dir = filepath.Join("testdata", name)
	build.Env = append(os.Environ(), "GOWORK=off")

	if output, err := build.CombinedOutput(); err != nil {
		panic(fmt.Sprintf("building testdata/%s: %v\n%s", name, err, output))
	}

	return out
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}

	return ""
}

type run struct {
	stdout string
	stderr string
	code   int
}

func cli(t *testing.T, dir string, args ...string) run {
	t.Helper()

	return runBinary(t, exampleBinary, dir, nil, args...)
}

func runBinary(t *testing.T, binary, dir string, env []string, args ...string) run {
	t.Helper()

	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = append(stripPlatformEnv(os.Environ()), env...)

	var stdout, stderr bytes.Buffer

	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()

	code := 0

	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("running %s: %v", binary, err)
	}

	return run{
		stdout: stdout.String(),
		stderr: stderr.String(),
		code:   code,
	}
}

func stripPlatformEnv(env []string) []string {
	var out []string

	for _, kv := range env {
		if !strings.HasPrefix(kv, "DAGFLOWS_INPUT=") && !strings.HasPrefix(kv, "DAGFLOWS_OUTPUT=") {
			out = append(out, kv)
		}
	}

	return out
}

func project(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	write(t, dir, "rows.ndjson", "{\"n\": 1}\n{\"n\": 2}\n")

	return dir
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var out map[string]any

	require.NoError(t, json.Unmarshal(raw, &out), "%s", raw)

	return out
}

func parseJSON(t *testing.T, text string) map[string]any {
	t.Helper()

	var out map[string]any

	require.NoError(t, json.Unmarshal([]byte(text), &out), "%s", text)

	return out
}

func TestAnUnknownCommandIsAUsageErrorNotANodeFailure(t *testing.T) {
	result := cli(t, project(t), "run")
	require.Equal(t, 2, result.code)
	require.Contains(t, result.stderr, "unknown command 'run'")
	require.NotContains(t, result.stdout, "status", "a developer mistake must not produce an envelope")
}

func TestBareInvocationPrintsHelpRatherThanCrashing(t *testing.T) {
	result := cli(t, project(t))
	require.Equal(t, 2, result.code)
	require.Contains(t, result.stdout, "dagflows - author and run workflow nodes")
	require.NotContains(t, result.stderr, "panic")
}

func TestHelpIsSuccess(t *testing.T) {
	result := cli(t, project(t), "--help")
	require.Equal(t, 0, result.code)
	require.Contains(t, result.stdout, "build manifest")
	require.Contains(t, result.stdout, "dev   run")
}

func TestInvokeIsNotAdvertised(t *testing.T) {
	require.NotContains(t, cli(t, project(t), "--help").stdout, "invoke")
}

func TestANodeRunsWithNoHandWrittenEnvelope(t *testing.T) {
	result := cli(t, project(t), "dev", "run", "count", "--input", "seed=rows.ndjson")
	require.Equal(t, 0, result.code, result.stderr)
	require.Contains(t, result.stdout, `"rows": 2`)
	require.Contains(t, result.stdout, "working", "the node's own prints belong in the output")
}

func TestInlineJSONIsAcceptedAsAnInput(t *testing.T) {
	result := cli(t, project(t), "dev", "run", "count", "--input", `seed=[{"n": 1}]`)
	require.Equal(t, 0, result.code, result.stderr)
	require.Contains(t, result.stdout, `"rows": 1`)
}

func TestAFailingNodeExitsOneAndExplainsItself(t *testing.T) {
	result := cli(t, project(t), "dev", "run", "fails", "--input", "seed=rows.ndjson")
	require.Equal(t, 1, result.code)
	require.Contains(t, result.stderr, "FAILED (infrastructure)")
	require.Contains(t, result.stderr, "upstream returned 503")
}

func TestACrashingNodeIsReportedNotSwallowed(t *testing.T) {
	result := cli(t, project(t), "dev", "run", "crashes")
	require.Equal(t, 1, result.code)
	require.Contains(t, result.stderr, "FAILED (permanent)")
	require.Contains(t, result.stderr, "panic: runtime error: index out of range")
}

func TestJSONOutputIsMachineReadable(t *testing.T) {
	result := cli(t, project(t), "dev", "run", "count", "--input", "seed=rows.ndjson", "--json")
	require.Equal(t, 0, result.code, result.stderr)

	payload := parseJSON(t, result.stdout)
	require.Equal(t, true, payload["ok"])
	require.Equal(t, map[string]any{"rows": float64(2)}, payload["result"].(map[string]any)["output"].(map[string]any)["data"])
	require.Contains(t, payload["logs"], "working")
}

func TestAMissingNodeIsNamed(t *testing.T) {
	result := cli(t, project(t), "dev", "run", "absent")
	require.Equal(t, 1, result.code)
	require.Contains(t, result.stderr, "no node registered as 'absent'")
	require.Contains(t, result.stderr, "count, compute, report")
}

func TestABadInputSpecIsAUsageError(t *testing.T) {
	result := cli(t, project(t), "dev", "run", "count", "--input", "no-equals-sign")
	require.Equal(t, 2, result.code)
	require.Contains(t, result.stderr, "<parent>=<file or json>")
}

func TestAnInputThatIsNeitherFileNorJSONSaysSo(t *testing.T) {
	result := cli(t, project(t), "dev", "run", "count", "--input", "seed=./nope.ndjson")
	require.Equal(t, 1, result.code)
	require.Contains(t, result.stderr, "neither a file that exists nor valid json")
}

func TestTheEnvelopeCanBeKeptAndRoutingIsShown(t *testing.T) {
	dir := project(t)
	result := cli(t, dir, "dev", "run", "report", "--input", "seed=rows.ndjson", "--keep-fixture", "kept/in.json")
	require.Equal(t, 0, result.code, result.stderr)
	require.Contains(t, result.stdout, "next: process")

	kept := readJSON(t, filepath.Join(dir, "kept", "in.json"))
	require.Equal(t, "report", kept["ctx"].(map[string]any)["node_key"])
}

func TestFixtureWritesARunnableEnvelope(t *testing.T) {
	dir := project(t)
	result := cli(t, dir, "dev", "fixture", "count", "--input", "seed=rows.ndjson")
	require.Equal(t, 0, result.code, result.stderr)

	envelope := readJSON(t, filepath.Join(dir, "fixture.json"))
	seed := envelope["payload"].(map[string]any)["inputs"].(map[string]any)["seed"].(map[string]any)
	require.Equal(t, []any{map[string]any{"n": float64(1)}, map[string]any{"n": float64(2)}}, seed["data"])
	require.Equal(t, "app", envelope["ctx"].(map[string]any)["entrypoint"])
	require.Equal(t, "count", envelope["ctx"].(map[string]any)["node_key"])

	ran := runBinary(t, exampleBinary, dir, []string{"DAGFLOWS_INPUT=fixture.json", "DAGFLOWS_OUTPUT=out.json"})
	require.Equal(t, 0, ran.code, ran.stderr)

	written := readJSON(t, filepath.Join(dir, "out.json"))
	require.Equal(t, map[string]any{"rows": float64(2)}, written["output"].(map[string]any)["data"])
}

func TestTheContentTypeFollowsTheFileSuffix(t *testing.T) {
	dir := project(t)
	write(t, dir, "rows.csv", "n,name\r\n1,ana\r\n")
	cli(t, dir, "dev", "fixture", "count", "--input", "seed=rows.csv", "-o", "csv.json")

	entry := readJSON(t, filepath.Join(dir, "csv.json"))["payload"].(map[string]any)["inputs"].(map[string]any)["seed"].(map[string]any)
	require.Equal(t, "text/csv", entry["content_type"])
	require.Equal(t, []any{map[string]any{"n": "1", "name": "ana"}}, entry["data"])
}

func TestTheFixtureHintIsACommandThatRuns(t *testing.T) {
	dir := project(t)
	result := cli(t, dir, "dev", "fixture", "count", "--input", "seed=rows.ndjson")
	require.Equal(t, 0, result.code, result.stderr)

	_, hint, found := strings.Cut(result.stdout, "run it with: ")
	require.True(t, found, result.stdout)

	env, argv := splitCommand(strings.TrimSpace(hint))
	require.NotEmpty(t, argv)

	ran := runBinary(t, argv[0], dir, env, argv[1:]...)
	require.Equal(t, 0, ran.code, "the hint did not run: %s\n%s", hint, ran.stderr)

	written := readJSON(t, filepath.Join(dir, "out.json"))
	require.Equal(t, map[string]any{"rows": float64(2)}, written["output"].(map[string]any)["data"])
}

func splitCommand(line string) (env []string, argv []string) {
	argv = strings.Fields(line)

	for len(argv) > 0 && strings.Contains(argv[0], "=") && !strings.HasPrefix(argv[0], "-") {
		env = append(env, argv[0])
		argv = argv[1:]
	}

	return env, argv
}

func TestTheNodeKeyInAFixtureIsTheOneNamed(t *testing.T) {
	dir := project(t)
	cli(t, dir, "dev", "fixture", "compute", "-o", "m.json")

	ctx := readJSON(t, filepath.Join(dir, "m.json"))["ctx"].(map[string]any)
	require.Equal(t, "compute", ctx["node_key"])
	require.Equal(t, "go", ctx["language"])
	require.Equal(t, float64(512), ctx["memory_mb"])
}

func TestTheManifestCommandWritesWhatTheBuilderReads(t *testing.T) {
	dir := project(t)
	result := cli(t, dir, "build", "manifest")
	require.Equal(t, 0, result.code, result.stderr)
	require.Contains(t, result.stdout, "wrote dagflows-manifest.json with 6 node(s)")

	manifest := readJSON(t, filepath.Join(dir, "dagflows-manifest.json"))
	require.Equal(t, float64(1), manifest["v"])
	require.Equal(t, map[string]any{"language": "go", "version": "1.26"}, manifest["runtime"])
	require.Equal(t, "demo", manifest["workflow"].(map[string]any)["name"])

	nodes := map[string]map[string]any{}

	for _, raw := range manifest["nodes"].([]any) {
		node := raw.(map[string]any)
		nodes[node["key"].(string)] = node
	}

	require.Equal(t, "app", nodes["count"]["entrypoint"])
	require.Equal(t, "app", nodes["report"]["entrypoint"], "every node names the one binary")
	require.Equal(t, []any{"compute"}, nodes["report"]["depends"])
	require.Equal(t, []any{"crunch"}, nodes["report"]["external_depends"])
}

func TestTheOutputPathCanBeChosenAndIsCreated(t *testing.T) {
	dir := project(t)
	result := cli(t, dir, "build", "manifest", "-o", "build/out.json")
	require.Equal(t, 0, result.code, result.stderr)
	require.FileExists(t, filepath.Join(dir, "build", "out.json"))
}

func TestTheManifestCommandAnswersInJSON(t *testing.T) {
	result := cli(t, project(t), "build", "manifest", "--json")
	require.Equal(t, 0, result.code, result.stderr)

	payload := parseJSON(t, result.stdout)
	require.Equal(t, true, payload["ok"])
	require.Equal(t, "dagflows-manifest.json", payload["path"])
	require.Equal(t, []any{"count", "compute", "report", "fails", "crashes", "version"}, payload["nodes"])
}

func TestValidateWritesNothing(t *testing.T) {
	dir := project(t)
	result := cli(t, dir, "build", "validate")
	require.Equal(t, 0, result.code, result.stderr)
	require.Contains(t, result.stdout, "workflow 'demo' is valid: 6 node(s) - count, compute, report, fails, crashes, version")
	require.NoFileExists(t, filepath.Join(dir, "dagflows-manifest.json"))

	asJSON := parseJSON(t, cli(t, dir, "build", "validate", "--json").stdout)
	require.Equal(t, true, asJSON["ok"])
	require.Equal(t, map[string]any{"language": "go", "version": "1.26"}, asJSON["runtime"])
}

func TestABinaryDeclaringNoWorkflowIsReported(t *testing.T) {
	result := runBinary(t, shapesBinary, project(t), []string{"SHAPE=none"}, "build", "manifest")
	require.Equal(t, 1, result.code)
	require.Contains(t, result.stderr, "declared no workflow")
}

func TestTwoWorkflowsInOneBinaryAreReported(t *testing.T) {
	result := runBinary(t, shapesBinary, project(t), []string{"SHAPE=two"}, "build", "manifest")
	require.Equal(t, 1, result.code)
	require.Contains(t, result.stderr, "declared 2 workflows ('a', 'b')")
}

func TestAUsageMistakeExitsDifferentlyFromAFailure(t *testing.T) {
	dir := project(t)
	mistake := cli(t, dir, "build", "manifest", "--bogus")
	require.Equal(t, 2, mistake.code, "an unknown flag is a usage error")
	require.Contains(t, mistake.stderr, "build <command>")

	broken := runBinary(t, shapesBinary, dir, []string{"SHAPE=badkey"}, "build", "manifest")
	require.Equal(t, 1, broken.code, "a bad declaration is a real failure")
	require.Contains(t, broken.stderr, "node key '9lives' invalid")
}

func TestADeclarationRefusalIsACleanErrorNotATraceback(t *testing.T) {
	dir := project(t)

	for _, shape := range []string{"badkey", "anonymous", "empty"} {
		result := runBinary(t, shapesBinary, dir, []string{"SHAPE=" + shape}, "build", "manifest")
		require.Equal(t, 1, result.code, shape)
		require.NotContains(t, result.stderr, "goroutine", shape)
		require.NotContains(t, result.stderr, "panic", shape)
		require.NoFileExists(t, filepath.Join(dir, "dagflows-manifest.json"), "a manifest error must not write a manifest")
	}

	anonymous := runBinary(t, shapesBinary, dir, []string{"SHAPE=anonymous"}, "build", "manifest")
	require.Contains(t, anonymous.stderr, "cannot derive a node key")

	empty := runBinary(t, shapesBinary, dir, []string{"SHAPE=empty"}, "build", "manifest")
	require.Contains(t, empty.stderr, "declares no nodes")
}

func TestANodeNeverWritesAnEnvelopeForAManifestError(t *testing.T) {
	dir := project(t)
	result := runBinary(t, shapesBinary, dir, []string{"SHAPE=badkey"}, "build", "manifest")
	require.Equal(t, 1, result.code)
	require.NoFileExists(t, filepath.Join(dir, "dagflows-manifest.json"))
	require.NotContains(t, result.stdout, "status")
}

func TestCheckPassesWhenTheManifestMatchesTheSource(t *testing.T) {
	dir := project(t)
	cli(t, dir, "build", "manifest")

	result := cli(t, dir, "build", "manifest", "--check")
	require.Equal(t, 0, result.code, result.stderr)
	require.Contains(t, result.stdout, "up to date")
}

func staleManifest(t *testing.T, dir string, mutate func(manifest map[string]any)) {
	t.Helper()

	path := filepath.Join(dir, "dagflows-manifest.json")
	manifest := readJSON(t, path)
	mutate(manifest)

	body, err := json.MarshalIndent(manifest, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(body, '\n'), 0o644))
}

func TestCheckNamesTheNodeThatWasRemoved(t *testing.T) {
	dir := project(t)
	cli(t, dir, "build", "manifest")

	staleManifest(t, dir, func(manifest map[string]any) {
		manifest["nodes"] = manifest["nodes"].([]any)[:5]
	})

	result := cli(t, dir, "build", "manifest", "--check")
	require.Equal(t, 1, result.code)
	require.Contains(t, result.stderr, "node 'version' was added")
	require.Contains(t, result.stderr, "build manifest")
}

func TestCheckNamesANodeWhoseSettingsChanged(t *testing.T) {
	dir := project(t)
	cli(t, dir, "build", "manifest")

	staleManifest(t, dir, func(manifest map[string]any) {
		node := manifest["nodes"].([]any)[0].(map[string]any)
		node["execution"].(map[string]any)["machine"] = "xl"
	})

	result := cli(t, dir, "build", "manifest", "--check")
	require.Equal(t, 1, result.code)
	require.Contains(t, result.stderr, "node 'count' changed")
}

func TestCheckNamesRuntimeAndWorkflowDrift(t *testing.T) {
	dir := project(t)
	cli(t, dir, "build", "manifest")

	staleManifest(t, dir, func(manifest map[string]any) {
		manifest["runtime"].(map[string]any)["version"] = "1.25"
		manifest["workflow"].(map[string]any)["max_concurrent_nodes"] = 1
		manifest["nodes"] = append(manifest["nodes"].([]any), map[string]any{"key": "ghost", "entrypoint": "app"})
	})

	result := cli(t, dir, "build", "manifest", "--check", "--json")
	require.Equal(t, 1, result.code)

	payload := parseJSON(t, result.stdout)
	require.Equal(t, []any{
		"node 'ghost' was removed",
		"runtime changed: map[language:go version:1.25] -> map[language:go version:1.26]",
		"workflow settings changed",
	}, payload["drift"])
}

func TestCheckReportsAMissingManifestRatherThanPassing(t *testing.T) {
	result := cli(t, project(t), "build", "manifest", "--check")
	require.Equal(t, 1, result.code)
	require.Contains(t, result.stderr, "does not exist yet")
}

func TestCheckDoesNotWriteTheFile(t *testing.T) {
	dir := project(t)
	result := cli(t, dir, "build", "manifest", "--check")
	require.Equal(t, 1, result.code)
	require.NoFileExists(t, filepath.Join(dir, "dagflows-manifest.json"))
}

func TestCheckReportsDriftAsJSON(t *testing.T) {
	dir := project(t)
	cli(t, dir, "build", "manifest")

	staleManifest(t, dir, func(manifest map[string]any) {
		manifest["nodes"] = manifest["nodes"].([]any)[:5]
	})

	result := cli(t, dir, "build", "manifest", "--check", "--json")
	require.Equal(t, 1, result.code)

	payload := parseJSON(t, result.stdout)
	require.Equal(t, false, payload["ok"])
	require.Equal(t, true, payload["stale"])
	require.Equal(t, []any{"node 'version' was added"}, payload["drift"])
}

func TestCheckRefusesAManifestThatIsNotJSON(t *testing.T) {
	dir := project(t)
	write(t, dir, "dagflows-manifest.json", "{nope")

	result := cli(t, dir, "build", "manifest", "--check")
	require.Equal(t, 1, result.code)
	require.Contains(t, result.stderr, "is not valid json")
}

var bareCommand = regexp.MustCompile(`(?:-m )?\bdagflows (?:build|dev|invoke)(?:[^:]|$)`)

func claimsBareCommand(text string) string {
	for _, found := range bareCommand.FindAllString(text, -1) {
		if !strings.HasPrefix(found, "-m ") {
			return found
		}
	}

	return ""
}

func TestTheBareCommandPatternMatchesWhatItShould(t *testing.T) {
	require.NotEmpty(t, claimsBareCommand("usage: dagflows build <command>"))
	require.NotEmpty(t, claimsBareCommand("run: dagflows build manifest"))
	require.NotEmpty(t, claimsBareCommand("dagflows dev fixture"))

	require.Empty(t, claimsBareCommand("usage: python -m dagflows build <command>"))
	require.Empty(t, claimsBareCommand("dagflows build: name the module"))
	require.Empty(t, claimsBareCommand("go run . build manifest"))
	require.Empty(t, claimsBareCommand("./app build manifest"))
}

func TestNoHintClaimsABareDagflowsCommand(t *testing.T) {
	dir := project(t)
	printed := []string{
		cli(t, dir, "build").stderr,
		cli(t, dir, "build", "manifest", "--nope").stderr,
		cli(t, dir, "build", "manifest", "--check").stderr,
		cli(t, dir, "dev").stderr,
		cli(t, dir, "dev", "run").stderr,
		cli(t, dir, "dev", "fixture", "count").stdout,
		cli(t, dir, "--help").stdout,
		cli(t, dir, "nonsense").stderr,
	}

	for _, text := range printed {
		found := claimsBareCommand(text)
		require.Empty(t, found, "%q tells the user to run a command this module must not install:\n%s", found, text)
	}
}

func invoke(t *testing.T, dir string, envelope any, args ...string) (run, map[string]any) {
	t.Helper()

	body, err := json.Marshal(envelope)
	require.NoError(t, err)
	write(t, dir, "in.json", string(body))

	result := runBinary(t, exampleBinary, dir, []string{"DAGFLOWS_INPUT=in.json", "DAGFLOWS_OUTPUT=out.json"}, args...)

	return result, readJSON(t, filepath.Join(dir, "out.json"))
}

func computeEnvelope(nodeKey string) map[string]any {
	return map[string]any{
		"ctx": map[string]any{
			"node_key":         nodeKey,
			"entrypoint":       "app",
			"inline_max_bytes": 1 << 20,
		},
		"payload": map[string]any{
			"inputs": map[string]any{
				"seed": map[string]any{
					"type": "INLINE",
					"data": map[string]any{"factor": 3},
				},
			},
		},
	}
}

func TestTheWorkerArgvRunsTheNodeTheEnvelopeNames(t *testing.T) {
	result, written := invoke(t, project(t), computeEnvelope("compute"))
	require.Equal(t, 0, result.code, result.stderr)
	require.Equal(t, "SUCCESS", written["status"])
	require.Equal(t, map[string]any{"value": float64(42)}, written["output"].(map[string]any)["data"])
}

func TestTheInvokeWordAndNodeFlagAreAcceptedAsInformational(t *testing.T) {
	_, written := invoke(t, project(t), computeEnvelope("compute"), "invoke", "--node", "something-else")
	require.Equal(t, "SUCCESS", written["status"])
}

func TestAMissingNodeKeyIsReportedNotGuessed(t *testing.T) {
	result, written := invoke(t, project(t), computeEnvelope(""))
	require.Equal(t, 0, result.code)
	require.Equal(t, "FAILED", written["status"])
	require.Contains(t, written["error"].(map[string]any)["message"], "ctx.node_key is empty")
}

func TestAnUnregisteredNodeNamesWhatExists(t *testing.T) {
	_, written := invoke(t, project(t), computeEnvelope("nowhere"))
	require.Equal(t, "FAILED", written["status"])

	message := written["error"].(map[string]any)["message"].(string)
	require.Contains(t, message, "no node registered as 'nowhere'")
	require.Contains(t, message, "count, compute, report")
}

func TestABadInvocationIsReportedAsANodeFailure(t *testing.T) {
	_, written := invoke(t, project(t), computeEnvelope("compute"), "invoke", "--bogus")
	require.Equal(t, "FAILED", written["status"])
	require.Contains(t, written["error"].(map[string]any)["message"], "usage:")
}

func TestAnEmptyEnvelopeStillDispatches(t *testing.T) {
	dir := project(t)
	write(t, dir, "in.json", "")
	runBinary(t, exampleBinary, dir, []string{"DAGFLOWS_INPUT=in.json", "DAGFLOWS_OUTPUT=out.json"})

	written := readJSON(t, filepath.Join(dir, "out.json"))
	require.Equal(t, "FAILED", written["status"])
	require.Contains(t, written["error"].(map[string]any)["message"], "ctx.node_key is empty")
}

func TestAPanickingNodeStillWritesAnEnvelope(t *testing.T) {
	result, written := invoke(t, project(t), computeEnvelope("crashes"))
	require.Equal(t, 0, result.code)
	require.Equal(t, "FAILED", written["status"])
	require.Contains(t, result.stderr, "goroutine", "the stack goes to the log, not the envelope")
}

func TestTheVersionABinaryReportsIsTheReplacedSourceTree(t *testing.T) {
	_, written := invoke(t, project(t), computeEnvelope("version"))
	require.Equal(t, map[string]any{"sdk": "(devel)"}, written["output"].(map[string]any)["data"])
}

func TestAMissingInputFileIsAFailedEnvelope(t *testing.T) {
	dir := project(t)
	runBinary(t, exampleBinary, dir, []string{"DAGFLOWS_INPUT=absent.json", "DAGFLOWS_OUTPUT=out.json"})

	written := readJSON(t, filepath.Join(dir, "out.json"))
	require.Equal(t, "FAILED", written["status"])
	require.Contains(t, written["error"].(map[string]any)["message"], "absent.json")
}

func TestAGracefulNodeFailureExitsZeroSoTheWorkerReadsTheEnvelope(t *testing.T) {
	result, written := invoke(t, project(t), computeEnvelope("fails"))
	require.Equal(t, 0, result.code, result.stderr)
	require.Equal(t, "FAILED", written["status"])
	require.Equal(t, map[string]any{"message": "upstream returned 503", "category": "infrastructure"}, written["error"])
	require.NotContains(t, written, "retry", "abort=false with no delay leaves retry to the policy")
}
