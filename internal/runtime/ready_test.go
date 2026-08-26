package runtime

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// readyFile points the ready descriptor at a file and returns its path, so a
// test can read back whatever the signal wrote.
//
// The descriptor is deliberately left open: signalReady borrows it rather than
// closing it, and a test that closed it first would be proving the wrong thing.
func readyFile(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "signal")

	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating the signal file: %v", err)
	}

	t.Cleanup(func() { _ = file.Close() })
	t.Setenv(ReadyFDEnv, strconv.Itoa(int(file.Fd())))

	return path
}

func TestSignalReadyWritesASingleByteToTheDescriptorThePlatformNames(t *testing.T) {
	path := readyFile(t)

	signalReady()

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the signal file: %v", err)
	}

	if string(written) != "." {
		t.Errorf("signal wrote %q, want a single %q", written, ".")
	}
}

// An older worker passes no descriptor. The node still runs; it just spends its
// own timeout on startup, as it did before.
func TestSignalReadyDoesNothingWhenNoDescriptorIsNamed(t *testing.T) {
	t.Setenv(ReadyFDEnv, "")

	signalReady()
}

func TestSignalReadyDoesNothingWhenTheDescriptorIsUnusable(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "not-a-number", "9999"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(ReadyFDEnv, value)

			signalReady()
		})
	}
}

// A signal sent after the handler charges startup to the node, so the order is
// the whole point rather than the write itself.
func TestExecuteSignalsReadyBeforeTheHandlerRunsNotAfter(t *testing.T) {
	path := readyFile(t)

	var seenAtEntry []byte

	handler := func(_ *Ctx, _ *Inputs) (any, error) {
		// Reading the signal file as the handler's first act: a byte here
		// proves the signal was sent before the handler was entered.
		seenAtEntry, _ = os.ReadFile(path)

		return map[string]any{"ok": true}, nil
	}

	Execute(handler, &Ctx{InlineMaxBytes: 1 << 20}, &Inputs{})

	if string(seenAtEntry) != "." {
		t.Errorf("the handler saw %q at entry, want the signal already sent", seenAtEntry)
	}
}
