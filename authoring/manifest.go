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

// Manifest is the top-level document written to dagflows-manifest.json.
type Manifest struct {
	V        int                `json:"v"`
	Runtime  RuntimeManifest    `json:"runtime"`
	Workflow *WorkflowManifest  `json:"workflow,omitempty"`
	Triggers []*TriggerManifest `json:"triggers,omitempty"`
	Nodes    []*NodeManifest    `json:"nodes"`
}

// RuntimeManifest names the language and runtime version the project builds against.
type RuntimeManifest struct {
	Language string `json:"language"`
	Version  string `json:"version,omitempty"`
}

// WorkflowManifest holds the workflow-level limits and warning policy, and the
// default retry and execution blocks declared for the project.
type WorkflowManifest struct {
	Name               string `json:"name"`
	MaxConcurrentNodes int    `json:"max_concurrent_nodes,omitempty"`
	MaxCycleCount      int    `json:"max_cycle_count,omitempty"`
	// OnWarning is what the deploy does when the platform has to adjust a
	// declared value: "allow" clamps and carries on, "reject" refuses.
	OnWarning OnWarning          `json:"on_warning,omitempty"`
	Retry     *RetryManifest     `json:"retry,omitempty"`
	Execution *ExecutionManifest `json:"execution,omitempty"`
}

// TriggerManifest is an event source the workflow declares, with the schema of
// the event it delivers.
type TriggerManifest struct {
	Key    string `json:"key"`
	Schema Schema `json:"schema,omitempty"`
}

// NodeManifest is one node of the DAG: its key, its dependencies, and the
// configuration and typed contract declared for it.
type NodeManifest struct {
	Key             string             `json:"key"`
	Entrypoint      string             `json:"entrypoint"`
	Type            string             `json:"type,omitempty"`
	Depends         []string           `json:"depends,omitempty"`
	ExternalDepends []string           `json:"external_depends,omitempty"`
	TriggeredBy     []string           `json:"triggered_by,omitempty"`
	Config          config             `json:"config,omitzero"`
	Retry           *RetryManifest     `json:"retry,omitempty"`
	Execution       *ExecutionManifest `json:"execution,omitempty"`
	Transfer        *TransferManifest  `json:"transfer,omitempty"`
	Mode            string             `json:"mode,omitempty"`
	IO              *IOManifest        `json:"io,omitempty"`
}

// IOManifest is the node's typed contract: what it expects of each parent
// (only the expectations it stated) and what it produces.
type IOManifest struct {
	Inputs ioInputs      `json:"inputs,omitzero"`
	Output *PortManifest `json:"output,omitempty"`
}

// ioInputs keeps expectations in parent order, as the other SDKs emit them.
type ioInputs []ioInput

// ioInput pairs a parent key with the port the node expects from it.
type ioInput struct {
	key  string
	port *PortManifest
}

// IsZero reports whether the node stated no expectations of its parents.
func (in ioInputs) IsZero() bool {
	return len(in) == 0
}

// MarshalJSON encodes the expectations as a JSON object keyed by parent node key.
func (in ioInputs) MarshalJSON() ([]byte, error) {
	var out bytes.Buffer

	out.WriteByte('{')

	for i, e := range in {
		if i > 0 {
			out.WriteByte(',')
		}

		key, err := json.Marshal(e.key)
		if err != nil {
			return nil, err
		}

		value, err := marshalCompact(e.port)
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

// TransferManifest holds the node transfer settings, where nil indicates a
// value the author left unset.
type TransferManifest struct {
	MaxOutputMB   *int64 `json:"max_output_mb,omitempty"`
	ConnTimeoutMs *int64 `json:"conn_timeout_ms,omitempty"`
	IdleTimeoutMs *int64 `json:"idle_timeout_ms,omitempty"`
}

// ExecutionManifest holds the node execution settings, where an empty field
// indicates a value the author left unset.
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

// entry is one key and value of a config block.
type entry struct {
	key   string
	value any
}

// config represents an ordered key-value list serialized as a JSON object.
type config []entry

// IsZero reports whether the config block holds no entries.
func (c config) IsZero() bool {
	return len(c) == 0
}

// set replaces the value stored for key, appending an entry when the key is new.
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

// MarshalJSON encodes the entries as a JSON object in insertion order.
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

// Keys returns the manifest's node keys in declaration order.
func (m *Manifest) Keys() []string {
	keys := make([]string, 0, len(m.Nodes))

	for _, node := range m.Nodes {
		keys = append(keys, node.Key)
	}

	return keys
}
