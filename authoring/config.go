// Package authoring declares workflow DAG topologies and serializes the build manifest.
package authoring

import (
	"fmt"
	"slices"
	"strings"
)

const millisPerSecond = 1_000

// Execution defines resource and execution configuration options for a node.
type Execution struct {
	// Machine names one from the platform catalog (e.g. "gp-2", "gp-4", "gp-8").
	Machine string
	// TimeoutSecs bounds a single execution of the node.
	TimeoutSecs int
	// GPU is reserved for future hardware acceleration options.
	GPU string
}

func (c *Execution) validate() error {
	if c.GPU != "" {
		return fmt.Errorf("gpu is reserved, this platform does not offer one yet")
	}

	if c.Machine != "" && strings.TrimSpace(c.Machine) == "" {
		return fmt.Errorf("machine cannot be blank, leave it out to take the platform default")
	}

	return nonNegative(
		field{"timeout_secs", c.TimeoutSecs},
	)
}

// Transfer configures how a node moves data in and out of storage.
type Transfer struct {
	// MaxOutputMB is how much this node expects to emit. Leaving it out asks
	// for no multipart upload, which is not the same as asking for zero.
	MaxOutputMB int
	// ConnTimeoutSecs bounds establishing a connection.
	ConnTimeoutSecs int
	// IdleTimeoutSecs bounds the gap between chunks. This never bounds the
	// whole transfer: a large upload that keeps moving is not stalled.
	IdleTimeoutSecs int
}

func (c *Transfer) validate() error {
	return nonNegative(
		field{"max_output_mb", c.MaxOutputMB},
		field{"conn_timeout_secs", c.ConnTimeoutSecs},
		field{"idle_timeout_secs", c.IdleTimeoutSecs},
	)
}

// asManifest serializes configured transfer options for the build manifest.
func (c *Transfer) asManifest() *TransferManifest {
	if c.MaxOutputMB == 0 && c.ConnTimeoutSecs == 0 && c.IdleTimeoutSecs == 0 {
		return nil
	}

	out := &TransferManifest{}
	if c.MaxOutputMB != 0 {
		mb := int64(c.MaxOutputMB)
		out.MaxOutputMB = &mb
	}

	if c.ConnTimeoutSecs != 0 {
		ms := int64(c.ConnTimeoutSecs) * millisPerSecond
		out.ConnTimeoutMs = &ms
	}

	if c.IdleTimeoutSecs != 0 {
		ms := int64(c.IdleTimeoutSecs) * millisPerSecond
		out.IdleTimeoutMs = &ms
	}

	return out
}

// asManifest serializes configured execution options for the build manifest.
func (c *Execution) asManifest() *ExecutionManifest {
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

// Retry configures optional node retry settings where unstated fields inherit platform defaults.
type Retry struct {
	// MaxAttempts includes the initial execution (1 means no retries).
	MaxAttempts *int
	// InitialBackoffMs is the starting delay that doubles per attempt up to MaxBackoffMs.
	InitialBackoffMs *int
	MaxBackoffMs     *int
	// RetryOn specifies error categories to retry (empty list disables category retries).
	RetryOn []RetryCategory
}

// OnWarning is what a deploy does when the platform has to adjust a declared
// value. A named type so a typo is a compile error rather than a policy the
// platform quietly ignores.
type OnWarning string

const (
	// OnWarningAllow clamps and carries on, reporting the adjustment.
	OnWarningAllow OnWarning = "allow"
	// OnWarningReject refuses the deploy rather than run settings the author
	// did not write.
	OnWarningReject OnWarning = "reject"
)

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

func (r *Retry) validate() error {
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

func (r *Retry) asManifest() *RetryManifest {
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
