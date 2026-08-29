package dagflows_test

import (
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const module = "github.com/dagflows/sdk-go"

func goList(t *testing.T, args ...string) []string {
	t.Helper()

	cmd := exec.Command("go", append([]string{"list"}, args...)...)
	cmd.Env = append(os.Environ(), "GOWORK=off")

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)

	return strings.Fields(string(out))
}

func TestEveryPackageIsStandardLibraryOnly(t *testing.T) {
	deps := goList(t, "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "./...")

	for _, dep := range deps {
		require.True(t, strings.HasPrefix(dep, module), "%s reaches outside the standard library", dep)
	}

	require.NotEmpty(t, deps)
}

func TestTheRuntimeNeverImportsTheAuthoringHalf(t *testing.T) {
	deps := goList(t, "-deps", "-f", "{{.ImportPath}}", "./runtime")

	require.NotContains(t, deps, module+"/authoring")
	require.NotContains(t, deps, module+"/internal/cli")
	require.NotContains(t, deps, module)
}

func TestTheExampleCompilesToAStaticLinuxBinary(t *testing.T) {
	out := filepath.Join(t.TempDir(), "app")

	build := exec.Command("go", "build", "-trimpath", "-o", out, ".")
	build.Dir = filepath.Join("testdata", "example")
	build.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")

	output, err := build.CombinedOutput()
	require.NoError(t, err, "%s", output)

	binary, err := elf.Open(out)
	require.NoError(t, err)
	defer binary.Close()

	require.Nil(t, binary.Section(".interp"), "a static binary has no interpreter")

	_, err = binary.DynamicSymbols()
	require.Error(t, err, "a static binary has no dynamic symbol table")
}

func TestNothingBuildsAnExecutableNamedDagflows(t *testing.T) {
	mains := goList(t, "-f", "{{if eq .Name \"main\"}}{{.ImportPath}}{{end}}", "./...")

	for _, main := range mains {
		require.True(t, strings.HasPrefix(main, module+"/scripts/"), "%s: only scripts may be main packages", main)
		require.NotEqual(t, "dagflows", filepath.Base(main), "%s would install as the reserved name", main)
	}

	for _, dir := range []string{"testdata/example", "testdata/shapes"} {
		require.NotEqual(t, "dagflows", filepath.Base(dir))
	}
}
