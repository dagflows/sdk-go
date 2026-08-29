package authoring

import (
	"bytes"
	"encoding/json"
)

// ManifestVersion specifies the manifest format version emitted by this SDK.
const ManifestVersion = 1

// Language specifies the runtime language identifier for project compilation.
const Language = "go"

// Entrypoint identifies the target executable name inside the microVM image.
const Entrypoint = "app"

// Manifest defines the top-level schema of dagflows-manifest.json.
type Manifest struct {
	V        int               `json:"v"`
	Runtime  RuntimeManifest   `json:"runtime"`
	Workflow *WorkflowManifest `json:"workflow,omitempty"`
	Nodes    []*NodeManifest   `json:"nodes"`
}

type RuntimeManifest struct {
	Language string `json:"language"`
	Version  string `json:"version,omitempty"`
}

type WorkflowManifest struct {
	Name               string `json:"name"`
	MaxConcurrentNodes int    `json:"max_concurrent_nodes,omitempty"`
	MaxCycleCount      int    `json:"max_cycle_count,omitempty"`
	// OnWarning is what the deploy does when the platform has to adjust a
	// declared value: "allow" clamps and carries on, "reject" refuses.
	OnWarning string             `json:"on_warning,omitempty"`
	Retry     *RetryManifest     `json:"retry,omitempty"`
	Execution *ExecutionManifest `json:"execution,omitempty"`
}

// NodeManifest describes an individual DAG node definition.
type NodeManifest struct {
	Key             string             `json:"key"`
	Entrypoint      string             `json:"entrypoint"`
	Type            string             `json:"type,omitempty"`
	Depends         []string           `json:"depends,omitempty"`
	ExternalDepends []string           `json:"external_depends,omitempty"`
	Config          config             `json:"config,omitzero"`
	Retry           *RetryManifest     `json:"retry,omitempty"`
	Execution       *ExecutionManifest `json:"execution,omitempty"`
	Transfer        *TransferManifest  `json:"transfer,omitempty"`
}

// TransferManifest defines node transfer settings for the manifest.
type TransferManifest struct {
	MaxOutputMB   *int64 `json:"max_output_mb,omitempty"`
	ConnTimeoutMs *int64 `json:"conn_timeout_ms,omitempty"`
	IdleTimeoutMs *int64 `json:"idle_timeout_ms,omitempty"`
}

// ExecutionManifest defines node execution settings for the manifest.
type ExecutionManifest struct {
	Machine   string `json:"machine,omitempty"`
	TimeoutMs *int64 `json:"timeout_ms,omitempty"`
}

// RetryManifest holds author-configured retry settings where nil indicates unset values.
type RetryManifest struct {
	MaxAttempts      *int     `json:"max_attempts,omitempty"`
	InitialBackoffMs *int     `json:"initial_backoff_ms,omitempty"`
	MaxBackoffMs     *int     `json:"max_backoff_ms,omitempty"`
	RetryOn          []string `json:"retry_on,omitempty"`
}

type entry struct {
	key   string
	value any
}

// config represents an ordered key-value list serialized as a JSON object.
type config []entry

func (c config) IsZero() bool {
	return len(c) == 0
}

func (c *config) set(key string, value any) {
	for i, e := range *c {
		if e.key == key {
			(*c)[i].value = value

			return
		}
	}

	*c = append(*c, entry{
		key:   key,
		value: value,
	})
}

func (c config) MarshalJSON() ([]byte, error) {
	var out bytes.Buffer

	out.WriteByte('{')

	for i, e := range c {
		if i > 0 {
			out.WriteByte(',')
		}

		key, err := json.Marshal(e.key)
		if err != nil {
			return nil, err
		}

		value, err := json.Marshal(e.value)
		if err != nil {
			return nil, err
		}

		out.Write(key)
		out.WriteByte(':')
		out.Write(value)
	}

	out.WriteByte('}')

	return out.Bytes(), nil
}

// Encode serializes the manifest as formatted JSON with two-space indentation.
func (m *Manifest) Encode() ([]byte, error) {
	var out bytes.Buffer

	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")

	if err := enc.Encode(m); err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}

// Keys returns registered node identifiers in declaration order.
func (m *Manifest) Keys() []string {
	keys := make([]string, 0, len(m.Nodes))

	for _, node := range m.Nodes {
		keys = append(keys, node.Key)
	}

	return keys
}
