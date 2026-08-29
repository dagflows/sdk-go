// Package authoring declares workflow DAG topologies and serializes the build manifest.
package authoring

import (
	"fmt"
	"slices"
	"strings"
)

const millisPerSecond = 1_000

// ExecutionConfig defines resource and execution configuration options for a node.
type ExecutionConfig struct {
	// Machine names a size from the platform catalog (e.g. "s", "m", "l").
	Machine string
	// TimeoutSecs bounds a single execution of the node.
	TimeoutSecs int
	// MaxOutputMB sets maximum output artifact size in megabytes.
	MaxOutputMB int
	// GPU is reserved for future hardware acceleration options.
	GPU string
}

func (c *ExecutionConfig) validate() error {
	if c.GPU != "" {
		return fmt.Errorf("gpu is reserved, this platform does not offer one yet")
	}

	if c.Machine != "" && strings.TrimSpace(c.Machine) == "" {
		return fmt.Errorf("machine cannot be blank, leave it out to take the platform default")
	}

	return nonNegative(
		field{"timeout_secs", c.TimeoutSecs},
		field{"max_output_mb", c.MaxOutputMB},
	)
}

// asConfig transforms execution settings into node config map entries.
func (c *ExecutionConfig) asConfig() []entry {
	var out []entry

	if c.MaxOutputMB != 0 {
		out = append(out, entry{"max_output_mb", c.MaxOutputMB})
	}

	return out
}

// asManifest serializes configured execution options for the build manifest.
func (c *ExecutionConfig) asManifest() *ExecutionManifest {
	if c.Machine == "" && c.TimeoutSecs == 0 {
		return nil
	}

	out := &ExecutionManifest{}
	if c.Machine != "" {
		out.Machine = c.Machine
	}

	if c.TimeoutSecs != 0 {
		ms := int64(c.TimeoutSecs) * millisPerSecond
		out.TimeoutMs = &ms
	}

	return out
}

// RetryConfig configures optional node retry settings where unstated fields inherit platform defaults.
type RetryConfig struct {
	// MaxAttempts includes the initial execution (1 means no retries).
	MaxAttempts *int
	// InitialBackoffMs is the starting delay that doubles per attempt up to MaxBackoffMs.
	InitialBackoffMs *int
	MaxBackoffMs     *int
	// RetryOn specifies error categories to retry (empty list disables category retries).
	RetryOn []RetryCategory
}

// RetryCategory identifies a retryable failure category.
type RetryCategory string

// Permanent failures are non-retryable and deliberately excluded from constants.
const (
	RetryOnInfrastructure RetryCategory = "infrastructure"
	RetryOnTimeout        RetryCategory = "timeout"
	RetryOnExecution      RetryCategory = "execution"
)

// RetryableCategories lists all valid failure categories that can be retried.
var RetryableCategories = []RetryCategory{
	RetryOnInfrastructure,
	RetryOnTimeout,
	RetryOnExecution,
}

func (r *RetryConfig) validate() error {
	if r.MaxAttempts != nil && *r.MaxAttempts < 1 {
		return fmt.Errorf("max_attempts=%d would never run the node, 1 means run once and do not retry",
			*r.MaxAttempts)
	}

	if err := nonNegativePtr(
		ptrField{"initial_backoff_ms", r.InitialBackoffMs},
		ptrField{"max_backoff_ms", r.MaxBackoffMs},
	); err != nil {
		return err
	}

	if r.InitialBackoffMs != nil && r.MaxBackoffMs != nil && *r.MaxBackoffMs < *r.InitialBackoffMs {
		return fmt.Errorf("max_backoff_ms=%d is below initial_backoff_ms=%d, so the cap would apply from the first retry",
			*r.MaxBackoffMs, *r.InitialBackoffMs)
	}

	for _, category := range r.RetryOn {
		if slices.Contains(RetryableCategories, category) {
			continue
		}

		if category == "permanent" {
			return fmt.Errorf("retry_on names %q, which the platform reports only when a retry cannot help", category)
		}

		known := make([]string, len(RetryableCategories))
		for i, c := range RetryableCategories {
			known[i] = string(c)
		}

		return fmt.Errorf("retry_on names unknown category %q, known: %s",
			category, strings.Join(known, ", "))
	}

	return nil
}

func (r *RetryConfig) asManifest() *RetryManifest {
	out := &RetryManifest{
		MaxAttempts:      r.MaxAttempts,
		InitialBackoffMs: r.InitialBackoffMs,
		MaxBackoffMs:     r.MaxBackoffMs,
	}

	// The manifest is plain JSON, so the named type does not travel.
	if r.RetryOn != nil {
		out.RetryOn = make([]string, len(r.RetryOn))
		for i, category := range r.RetryOn {
			out.RetryOn[i] = string(category)
		}
	}

	return out
}

type ptrField struct {
	name  string
	value *int
}

func nonNegativePtr(fields ...ptrField) error {
	for _, f := range fields {
		if f.value != nil && *f.value < 0 {
			return fmt.Errorf("%s cannot be negative, got %d", f.name, *f.value)
		}
	}

	return nil
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
