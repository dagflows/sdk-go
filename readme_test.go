package dagflows_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var (
	fence = regexp.MustCompile("(?s)(?:<!--\\s*not-tested:\\s*(?P<why>[^>]*?)\\s*-->\\s*\\n)?```(?P<lang>\\w*)\\n(?P<body>.*?)```")
	named = regexp.MustCompile(`^//\s*file:\s*(\S+)\s*$`)
	input = regexp.MustCompile(`--input\s+\w+=(\S+)`)
)

var bySuffix = map[string]string{
	".json":   `{"orders": [{"id": 1, "amount": 100}, {"id": 2, "amount": 250}]}`,
	".ndjson": "{\"n\": 1}\n{\"n\": 2}\n",
	".jsonl":  "{\"n\": 1}\n{\"n\": 2}\n",
	".csv":    "n,name\n1,ana\n2,bo\n",
	".txt":    "hello\n",
}

type block struct {
	body string
	why  string
}

func fenced(t *testing.T, language string) []block {
	t.Helper()

	text, err := os.ReadFile("README.md")
	require.NoError(t, err)

	var out []block

	for _, m := range fence.FindAllStringSubmatch(string(text), -1) {
		if m[2] == language {
			out = append(out, block{
				body: m[3],
				why:  m[1],
			})
		}
	}

	return out
}

func blocks(t *testing.T, language string) []string {
	t.Helper()

	var out []string

	for _, b := range fenced(t, language) {
		if b.why == "" {
			out = append(out, b.body)
		}
	}

	return out
}

func namedFiles(t *testing.T) map[string]string {
	t.Helper()

	out := map[string]string{}

	for _, body := range blocks(t, "go") {
		first, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
		if m := named.FindStringSubmatch(first); m != nil {
			out[m[1]] = body
		}
	}

	return out
}

func commandLines(t *testing.T) []string {
	t.Helper()

	var lines []string

	for _, body := range blocks(t, "bash") {
		for line := range strings.Lines(body) {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				lines = append(lines, line)
			}
		}
	}

	return lines
}

func referencedInputs(t *testing.T) []string {
	t.Helper()

	var found []string

	for _, line := range commandLines(t) {
		for _, m := range input.FindAllStringSubmatch(line, -1) {
			value := strings.Trim(m[1], `"'`)
			if strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[") {
				continue
			}

			found = append(found, value)
		}
	}

	return found
}

func documentedProject(t *testing.T) string {
	t.Helper()

	files := namedFiles(t)
	require.NotEmpty(t, files, "no block names a file, so nothing here is verifying anything")

	dir := t.TempDir()

	sdk, err := filepath.Abs(".")
	require.NoError(t, err)

	gomod := fmt.Sprintf("module example.com/readme\n\ngo 1.27\n\nrequire github.com/dagflows/sdk-go v0.0.0\n\nreplace github.com/dagflows/sdk-go => %q\n", filepath.ToSlash(sdk))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644))

	gosum, err := os.ReadFile(filepath.Join("testdata", "example", "go.sum"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.sum"), gosum, 0o644))

	for relative, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(relative))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}

	for _, name := range referencedInputs(t) {
		body, ok := bySuffix[filepath.Ext(name)]
		require.True(t, ok, "the README passes --input %s, and this harness has no body for %q files. Add one to bySuffix.", name, filepath.Ext(name))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}

	return dir
}

func inProject(t *testing.T, dir string, env []string, argv ...string) run {
	t.Helper()

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(append(stripPlatformEnv(os.Environ()), "GOWORK=off"), env...)

	out, err := cmd.CombinedOutput()
	code := 0

	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("running %v: %v", argv, err)
	}

	return run{
		stdout: string(out),
		code:   code,
	}
}

func parseBlock(body string) error {
	fset := token.NewFileSet()

	var err error

	for _, source := range []string{body, "package p\n" + body, "package p\nfunc _() {\n" + body + "\n}\n"} {
		if _, err = parser.ParseFile(fset, "readme.go", source, parser.SkipObjectResolution); err == nil {
			return nil
		}
	}

	return err
}

func TestEveryGoBlockParses(t *testing.T) {
	for _, body := range blocks(t, "go") {
		require.NoError(t, parseBlock(body), "a go block does not parse:\n%s", body)
	}
}

func exportedNames(t *testing.T) map[string]bool {
	t.Helper()

	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, "dagflows.go", nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	names := map[string]bool{}

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			names[d.Name.Name] = true

		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					names[s.Name.Name] = true

				case *ast.ValueSpec:
					for _, name := range s.Names {
						names[name.Name] = true
					}
				}
			}
		}
	}

	return names
}

var selector = regexp.MustCompile(`\bdagflows\.([A-Z]\w*)`)

func TestEveryReferencedNameExists(t *testing.T) {
	names := exportedNames(t)
	require.Contains(t, names, "Main")

	for _, body := range blocks(t, "go") {
		for _, m := range selector.FindAllStringSubmatch(body, -1) {
			require.True(t, names[m[1]], "the README uses dagflows.%s, which does not exist", m[1])
		}
	}
}

func TestTheDocumentedProjectCompilesAndEmitsAManifest(t *testing.T) {
	dir := documentedProject(t)

	vet := inProject(t, dir, nil, "go", "vet", "./...")
	require.Equal(t, 0, vet.code, "the documented project does not compile:\n%s", vet.stdout)

	result := inProject(t, dir, nil, "go", "run", ".", "build", "manifest", "-o", "m.json")
	require.Equal(t, 0, result.code, result.stdout)

	manifest := readJSON(t, filepath.Join(dir, "m.json"))
	require.NotEmpty(t, manifest["nodes"], "the documented workflow declares no nodes")
}

func TestTheReadmeShowsCommands(t *testing.T) {
	require.GreaterOrEqual(t, len(commandLines(t)), 6, "the CLI section stopped being covered")
}

func TestEverySkippedBlockSaysWhy(t *testing.T) {
	for _, language := range []string{"bash", "go"} {
		for _, b := range fenced(t, language) {
			if b.why != "" {
				require.Greater(t, len(b.why), 8, "a %s block is marked not-tested with no real reason: %q\n%s", language, b.why, b.body)
			}
		}
	}
}

func TestWhatIsSkippedIsReported(t *testing.T) {
	var skipped []string

	for _, language := range []string{"bash", "go"} {
		for _, b := range fenced(t, language) {
			if b.why != "" {
				skipped = append(skipped, fmt.Sprintf("not tested (%s): %s", language, b.why))
				t.Logf("not tested (%s): %s", language, b.why)
			}
		}
	}

	require.LessOrEqual(t, len(skipped), 4, "%d blocks are exempt from testing, which is enough to hide a broken README. Make them runnable or delete them.", len(skipped))
}

func TestEveryDocumentedCommandRuns(t *testing.T) {
	dir := documentedProject(t)

	for _, line := range commandLines(t) {
		env, argv := splitCommand(line)
		require.True(t, len(argv) >= 2 && argv[0] == "go" && argv[1] == "run",
			"this harness only runs `go run ...` commands, and the README shows:\n  %s\nMark its block if it cannot run here:\n  <!-- not-tested: needs a deployed platform -->", line)

		result := inProject(t, dir, env, argv...)
		require.Equal(t, 0, result.code, "the README shows a command that does not run:\n  %s\n  exit %d\n%s", line, result.code, result.stdout)
	}
}

func TestNoReadmeCommandClaimsABareDagflowsCommand(t *testing.T) {
	text, err := os.ReadFile("README.md")
	require.NoError(t, err)
	require.Empty(t, claimsBareCommand(string(text)))
}
