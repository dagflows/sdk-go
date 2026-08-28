package authoring

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	goruntime "runtime"
	"slices"
	"strings"
	"sync"

	"github.com/dagflows/sdk-go/internal/runtime"
)

var (
	nodeKeyPattern        = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)
	runtimeVersionPattern = regexp.MustCompile(`^[0-9][0-9A-Za-z.\-]{0,31}$`)
)

// WorkflowOptions configures workflow-level runtime settings.
type WorkflowOptions struct {
	Version            string
	MaxConcurrentNodes int
	MaxCycleCount      int
	// Retry sets default retry settings inherited and overridden field-by-field by nodes.
	Retry *RetryConfig
}

// NodeOptions configures an individual node registration.
type NodeOptions struct {
	Key       string
	Depends   []*NodeRef
	Execution *ExecutionConfig
	Retry     *RetryConfig
	Config    map[string]any
	Type      string
}

// NodeRef is a reference handle to a declared workflow node.
type NodeRef struct {
	Key        string
	Entrypoint string
	handler    runtime.Handler
}

// External reports whether the node is defined in an external workflow project.
func (n *NodeRef) External() bool {
	return n.handler == nil
}

func (n *NodeRef) String() string {
	if n.External() {
		return fmt.Sprintf("<NodeRef '%s' external>", n.Key)
	}

	return fmt.Sprintf("<NodeRef '%s' %s>", n.Key, n.Entrypoint)
}

// Workflow represents a project's registered DAG topology.
type Workflow struct {
	Name               string
	Version            string
	MaxConcurrentNodes int
	MaxCycleCount      int
	Retry              *RetryConfig

	nodes    []*NodeManifest
	handlers map[string]runtime.Handler
	errs     []error
}

var (
	declaredMu sync.Mutex
	declared   []*Workflow
)

// NewWorkflow initializes and registers a new Workflow definition.
func NewWorkflow(name string, opts WorkflowOptions) *Workflow {
	wf := &Workflow{
		Name:               name,
		Version:            opts.Version,
		MaxConcurrentNodes: opts.MaxConcurrentNodes,
		MaxCycleCount:      opts.MaxCycleCount,
		Retry:              opts.Retry,
		handlers:           map[string]runtime.Handler{},
	}

	if opts.Version != "" && !runtimeVersionPattern.MatchString(opts.Version) {
		wf.fail(fmt.Errorf("version '%s' invalid, want a version like '1.26', not an image reference", opts.Version))
	}

	if opts.MaxConcurrentNodes < 0 || opts.MaxCycleCount < 0 {
		wf.fail(errors.New("workflow limits cannot be negative"))
	}

	if opts.Retry != nil {
		if err := opts.Retry.validate(); err != nil {
			wf.fail(fmt.Errorf("workflow retry default: %w", err))
		}
	}

	declaredMu.Lock()
	declared = append(declared, wf)
	declaredMu.Unlock()

	return wf
}

// Declared returns all workflows registered in the current process.
func Declared() []*Workflow {
	declaredMu.Lock()
	defer declaredMu.Unlock()

	return slices.Clone(declared)
}

func (wf *Workflow) fail(err error) {
	wf.errs = append(wf.errs, err)
}

// Node registers a handler function as a workflow node.
func (wf *Workflow) Node(fn runtime.Handler, opts NodeOptions) *NodeRef {
	key := opts.Key

	if key == "" {
		derived, err := functionName(fn)
		if err != nil {
			wf.fail(err)
		}

		key = derived
	}

	ref := &NodeRef{
		Key:        key,
		Entrypoint: Entrypoint,
		handler:    fn,
	}

	if fn == nil {
		wf.fail(fmt.Errorf("node '%s' has no handler", key))

		return ref
	}

	if err := wf.claim(key); err != nil {
		wf.fail(err)

		return ref
	}

	local, external, err := splitDepends(key, opts.Depends)
	if err != nil {
		wf.fail(err)
	}

	node := &NodeManifest{
		Key:             key,
		Entrypoint:      Entrypoint,
		Type:            opts.Type,
		Depends:         local,
		ExternalDepends: external,
	}

	for _, k := range slices.Sorted(maps.Keys(opts.Config)) {
		node.Config.set(k, opts.Config[k])
	}

	if opts.Execution != nil {
		if err := opts.Execution.validate(); err != nil {
			wf.fail(fmt.Errorf("node '%s': %w", key, err))
		}

		for _, e := range opts.Execution.asConfig() {
			node.Config.set(e.key, e.value)
		}

		node.TimeoutSeconds = opts.Execution.Timeout
	}

	if opts.Retry != nil {
		if err := opts.Retry.validate(); err != nil {
			wf.fail(fmt.Errorf("node '%s': %w", key, err))
		}

		node.Retry = opts.Retry.asManifest()
	}

	wf.nodes = append(wf.nodes, node)
	wf.handlers[key] = fn

	return ref
}

// ExternalNode registers a dependency on an upstream node located in another workflow.
func (wf *Workflow) ExternalNode(key string) *NodeRef {
	if err := checkKey(key); err != nil {
		wf.fail(err)
	}

	return &NodeRef{
		Key: key,
	}
}

// Handler retrieves the handler registered for the given node key.
func (wf *Workflow) Handler(key string) (runtime.Handler, bool) {
	fn, ok := wf.handlers[key]

	return fn, ok
}

// Keys returns all registered node keys in declaration order.
func (wf *Workflow) Keys() []string {
	keys := make([]string, 0, len(wf.nodes))

	for _, node := range wf.nodes {
		keys = append(keys, node.Key)
	}

	return keys
}

// Manifest validates and constructs the workflow's Manifest representation.
func (wf *Workflow) Manifest() (*Manifest, error) {
	if len(wf.errs) > 0 {
		return nil, wf.errs[0]
	}

	if len(wf.nodes) == 0 {
		return nil, errors.New("this workflow declares no nodes, so there is nothing to build")
	}

	keys := wf.Keys()

	for _, node := range wf.nodes {
		for _, parent := range node.Depends {
			if !slices.Contains(keys, parent) {
				return nil, fmt.Errorf(
					"node '%s' depends on '%s', which this workflow does not define; its nodes are: %s",
					node.Key, parent, strings.Join(slices.Sorted(slices.Values(keys)), ", "),
				)
			}
		}
	}

	out := &Manifest{
		V: ManifestVersion,
		Runtime: RuntimeManifest{
			Language: Language,
			Version:  wf.Version,
		},
	}

	if wf.Name != "" {
		out.Workflow = &WorkflowManifest{
			Name:               wf.Name,
			MaxConcurrentNodes: wf.MaxConcurrentNodes,
			MaxCycleCount:      wf.MaxCycleCount,
		}

		if wf.Retry != nil {
			out.Workflow.Retry = wf.Retry.asManifest()
		}
	}

	out.Nodes = make([]*NodeManifest, 0, len(wf.nodes))

	for _, node := range wf.nodes {
		copied := *node
		out.Nodes = append(out.Nodes, &copied)
	}

	return out, nil
}

func (wf *Workflow) claim(key string) error {
	if err := checkKey(key); err != nil {
		return err
	}

	if _, taken := wf.handlers[key]; taken {
		return fmt.Errorf("duplicate node key '%s' in this project", key)
	}

	return nil
}

func checkKey(key string) error {
	if !nodeKeyPattern.MatchString(key) {
		return fmt.Errorf("node key '%s' invalid, want a letter then letters, digits, _ or -, max 64 chars", key)
	}

	return nil
}

// splitDepends categorizes dependencies into local node keys and external node keys.
func splitDepends(key string, depends []*NodeRef) (local, external []string, err error) {
	for _, parent := range depends {
		if parent == nil {
			return nil, nil, fmt.Errorf("node '%s' depends on a nil handle; pass the handle Node returned, or wf.ExternalNode(...) for a node in another project", key)
		}

		if parent.External() {
			external = append(external, parent.Key)
		} else {
			local = append(local, parent.Key)
		}
	}

	return local, external, nil
}

// functionName derives the node key identifier from the handler function reflection metadata.
func functionName(fn runtime.Handler) (string, error) {
	if fn == nil {
		return "", errors.New("a node needs a handler")
	}

	full := goruntime.FuncForPC(reflect.ValueOf(fn).Pointer()).Name()
	name := full[strings.LastIndex(full, "/")+1:]
	name = strings.TrimSuffix(name, "-fm")
	name = name[strings.LastIndex(name, ".")+1:]

	if name == "" || strings.HasPrefix(name, "func") && strings.Trim(name[4:], "0123456789") == "" {
		return "", fmt.Errorf("cannot derive a node key from %s; name the function, or set Key", full)
	}

	return name, nil
}
