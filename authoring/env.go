package authoring

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The names of the variables a node gets. The builder holds a manifest to the
// same rules, so a name it would refuse is refused where it is written.

// maxEnvNames is the most variables one node names, its workflow's and its own
// together.
const maxEnvNames = 50

// maxEnvNameLength is the longest a variable's name may be.
const maxEnvNameLength = 128

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedEnvNames are set in a node's environment by the platform, or
// describe its session.
var reservedEnvNames = []string{"HOME", "LOGNAME", "PATH", "PWD", "PYTHONUNBUFFERED", "SHELL", "TMPDIR", "USER"}

// reservedEnvPrefixes start names that something other than the node's code
// reads or sets.
var reservedEnvPrefixes = []struct{ prefix, whose string }{
	{"DAGFLOWS_", "the platform's"},
	{"LD_", "the dynamic loader's"},
}

// checkEnv reports why the names env lists cannot be variables, nil when they can.
func checkEnv(where string, env []string) error {
	if len(env) > maxEnvNames {
		return fmt.Errorf("%s names %d variables, past the %d one node may name", where, len(env), maxEnvNames)
	}

	seen := make(map[string]bool, len(env))
	for _, name := range env {
		if err := checkEnvName(name); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}

		if seen[name] {
			return fmt.Errorf("%s names the variable %q twice", where, name)
		}

		seen[name] = true
	}

	return nil
}

// checkEnvCount reports a node whose names and its workflow's, each counted
// once, pass the limit.
func checkEnvCount(key string, workflow, node []string) error {
	names := make(map[string]bool, len(workflow)+len(node))
	for _, name := range slices.Concat(workflow, node) {
		names[name] = true
	}

	if len(names) > maxEnvNames {
		return fmt.Errorf("node '%s' names %d variables with its workflow's, past the %d one node may name",
			key, len(names), maxEnvNames)
	}

	return nil
}

// checkEnvName reports why name cannot be a variable's, nil when it can.
func checkEnvName(name string) error {
	switch {
	case len(name) > maxEnvNameLength:
		return fmt.Errorf("the variable name %.32q... is %d characters, past the %d a name may have",
			name, len(name), maxEnvNameLength)
	case !envNamePattern.MatchString(name):
		return fmt.Errorf("%q is not a variable name, use letters, digits and _, starting with a letter or _", name)
	case slices.Contains(reservedEnvNames, name):
		return fmt.Errorf("%q is set in a node's environment by the platform, so it cannot be a variable", name)
	}

	for _, r := range reservedEnvPrefixes {
		if strings.HasPrefix(name, r.prefix) {
			return fmt.Errorf("names starting with %s are %s, so %q cannot be a variable", r.prefix, r.whose, name)
		}
	}

	return nil
}
