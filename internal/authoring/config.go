// Package authoring declares workflow DAG topologies and serializes the build manifest.
package authoring

import "fmt"

// ExecutionConfig defines runtime resource constraints and timeout limits for a node.
type ExecutionConfig struct {
	Timeout       int
	MemoryLimitMB int
	MilliCores    int
	MaxOutputMB   int
}

func (c *ExecutionConfig) validate() error {
	return nonNegative(
		field{"timeout", c.Timeout},
		field{"memory_limit_mb", c.MemoryLimitMB},
		field{"milli_cores", c.MilliCores},
		field{"max_output_mb", c.MaxOutputMB},
	)
}

// asConfig transforms execution settings into node config map entries.
func (c *ExecutionConfig) asConfig() []entry {
	var out []entry

	if c.MemoryLimitMB != 0 {
		out = append(out, entry{"memory_limit_mb", c.MemoryLimitMB})
	}

	if c.MilliCores != 0 {
		out = append(out, entry{"milli_cores", c.MilliCores})
	}

	if c.MaxOutputMB != 0 {
		out = append(out, entry{"max_output_mb", c.MaxOutputMB})
	}

	return out
}

// RetryConfig specifies node retry attempts and initial backoff.
type RetryConfig struct {
	MaxAttempts      int
	InitialBackoffMs int
}

const defaultInitialBackoffMs = 1000

func (r *RetryConfig) validate() error {
	return nonNegative(
		field{"max_attempts", r.MaxAttempts},
		field{"initial_backoff_ms", r.InitialBackoffMs},
	)
}

func (r *RetryConfig) asManifest() *RetryManifest {
	backoff := r.InitialBackoffMs
	if backoff == 0 {
		backoff = defaultInitialBackoffMs
	}

	return &RetryManifest{
		MaxAttempts:      r.MaxAttempts,
		InitialBackoffMs: backoff,
	}
}

type field struct {
	name  string
	value int
}

func nonNegative(fields ...field) error {
	for _, f := range fields {
		if f.value < 0 {
			return fmt.Errorf("%s cannot be negative, got %d", f.name, f.value)
		}
	}

	return nil
}
