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

	"github.com/dagflows/sdk-go/runtime"
)

// Workflow authoring and manifest generation.
//
// Registering a handler with wf.Node records its node configuration and
// reflects its signature into the manifest without executing handler code,
// so importing a node package during development and build steps is safe.

var (
	nodeKeyPattern        = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)
	runtimeVersionPattern = regexp.MustCompile(`^[0-9][0-9A-Za-z.\-]{0,31}$`)
)

// WorkflowOptions configures workflow-level runtime settings.
type WorkflowOptions struct {
	Version            string
	MaxConcurrentNodes int
	MaxCycleCount      int
	OnWarning          OnWarning
	// Retry sets default retry settings inherited and overridden field-by-field by nodes.
	Retry *Retry
}

// NodeOptions configures an individual node registration. Parents are not
// here: they are the edge passed to Node, which is what types the handler.
type NodeOptions struct {
	Key       string
	Execution *Execution
	Transfer  *Transfer
	Retry     *Retry
	Config    map[string]any
	Type      string
	// Mode is the node's execution lifecycle; unset means ModeOnce.
	Mode Mode
}

// Workflow is the declared DAG topology and node registry for a project.
type Workflow struct {
	Name               string
	Version            string
	MaxConcurrentNodes int
	MaxCycleCount      int
	OnWarning          OnWarning
	Retry              *Retry

	nodes    []*NodeManifest
	triggers []*TriggerManifest
	handlers map[string]runtime.Handler
	keys     map[string]bool
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
		OnWarning:          opts.OnWarning,
		Retry:              opts.Retry,
		handlers:           map[string]runtime.Handler{},
		keys:               map[string]bool{},
	}

	if opts.Version != "" && !runtimeVersionPattern.MatchString(opts.Version) {
		wf.fail(fmt.Errorf("version '%s' invalid, want a version like '1.27', not an image reference", opts.Version))
	}

	if opts.MaxConcurrentNodes < 0 || opts.MaxCycleCount < 0 {
		wf.fail(errors.New("workflow limits cannot be negative"))
	}

	// Validates that on_warning names a supported policy.
	switch opts.OnWarning {
	case "", OnWarningAllow, OnWarningReject:
	default:
		wf.fail(fmt.Errorf("on_warning '%s' is not a policy, use '%s' or '%s'",
			opts.OnWarning, OnWarningAllow, OnWarningReject))
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

// fail records an authoring error, reported when the manifest is built.
func (wf *Workflow) fail(err error) {
	wf.errs = append(wf.errs, err)
}

// Node registers a handler as a workflow node. In is where the handler's
// input comes from — the parent handle itself for one parent, Depends(...)
// for the bag, Root for none, Lazy(parent) for a typed handle — and Out is
// what the node produces, which is what its handle carries to its children.
func (wf *Workflow) Node[In, Out any](fn func(*runtime.Ctx, In) (Out, error), in runtime.Edge[In], opts ...NodeOptions) *runtime.NodeRef[Out] {
	return register[In, Out](wf, fn, in, opts, func(ctx *runtime.Ctx, arg In) (any, error) {
		return fn(ctx, arg)
	})
}

// NodeResult registers a handler that returns a Result, for routing and
// metadata. The handle is still typed by the payload, not the wrapper.
func (wf *Workflow) NodeResult[In, Out any](fn func(*runtime.Ctx, In) (runtime.Result[Out], error), in runtime.Edge[In], opts ...NodeOptions) *runtime.NodeRef[Out] {
	return register[In, Out](wf, fn, in, opts, func(ctx *runtime.Ctx, arg In) (any, error) {
		return fn(ctx, arg)
	})
}

// register records the node's manifest entry and its dispatch closure,
// returning the typed handle even when a declaration is rejected.
func register[In, Out any](wf *Workflow, fn any, in runtime.Edge[In], opts []NodeOptions, call func(*runtime.Ctx, In) (any, error)) *runtime.NodeRef[Out] {
	var options NodeOptions
	if len(opts) > 0 {
		options = opts[0]
	}

	key := options.Key
	if key == "" {
		derived, err := functionName(fn)
		if err != nil {
			wf.fail(err)
		}

		key = derived
	}

	ref := runtime.NewNodeRef[Out](key, runtime.KindNode)

	if fn == nil || reflect.ValueOf(fn).IsNil() {
		wf.fail(fmt.Errorf("node '%s' has no handler", key))

		return ref
	}

	if err := wf.claim(key); err != nil {
		wf.fail(err)

		return ref
	}

	if isNil(in) {
		wf.fail(nilHandle(key))

		return ref
	}

	edge := runtime.Describe(in)
	node := &NodeManifest{
		Key:        key,
		Entrypoint: Entrypoint,
		Type:       options.Type,
	}

	for _, parent := range edge.Parents {
		if isNil(parent) {
			wf.fail(nilHandle(key))

			return ref
		}

		// Categorizes parents into trigger sources, external node keys, and
		// local node keys.
		switch {
		case parent.Trigger():
			node.TriggeredBy = append(node.TriggeredBy, parent.Key())
		case parent.External():
			node.ExternalDepends = append(node.ExternalDepends, parent.Key())
		default:
			node.Depends = append(node.Depends, parent.Key())
		}
	}

	for _, k := range slices.Sorted(maps.Keys(options.Config)) {
		node.Config.set(k, options.Config[k])
	}

	if options.Execution != nil {
		if err := options.Execution.validate(); err != nil {
			wf.fail(fmt.Errorf("node '%s': %w", key, err))
		}

		node.Execution = options.Execution.asManifest()
	}

	if options.Transfer != nil {
		if err := options.Transfer.validate(); err != nil {
			wf.fail(fmt.Errorf("node '%s': %w", key, err))
		}

		node.Transfer = options.Transfer.asManifest()
	}

	if options.Retry != nil {
		if err := options.Retry.validate(); err != nil {
			wf.fail(fmt.Errorf("node '%s': %w", key, err))
		}

		node.Retry = options.Retry.asManifest()
	}

	switch options.Mode {
	case "", ModeOnce:
	case ModeStream:
		node.Mode = string(ModeStream)
	default:
		wf.fail(fmt.Errorf("node '%s': mode '%s' is not a mode, use ModeOnce or ModeStream", key, options.Mode))
	}

	io, err := ioFor[In, Out](edge)
	if err != nil {
		wf.fail(fmt.Errorf("node '%s': %w", key, err))
	}
	node.IO = io

	wf.nodes = append(wf.nodes, node)
	wf.handlers[key] = func(ctx *runtime.Ctx, inputs *runtime.Inputs) (any, error) {
		arg, err := runtime.Resolve(in, inputs)
		if err != nil {
			return nil, err
		}

		return call(ctx, arg)
	}

	return ref
}

// nilHandle builds the error reported for a dependency on an unassigned handle.
func nilHandle(key string) error {
	return fmt.Errorf(
		"node '%s' depends on a nil handle; pass the handle Node returned, Depends(...) or Root, wf.External(...) for a node in another project, or wf.Trigger(...)",
		key,
	)
}

// isNil reports whether v is a nil interface or a typed nil pointer, which a
// handle declared but never assigned is.
func isNil(v any) bool {
	if v == nil {
		return true
	}

	rv := reflect.ValueOf(v)

	return rv.Kind() == reflect.Pointer && rv.IsNil()
}

// ioFor derives the node's io block from the handler's types: the expectations
// it states of its parents, and what it produces.
func ioFor[In, Out any](edge runtime.EdgeSpec) (*IOManifest, error) {
	io := &IOManifest{}

	// An annotated input on a one-parent edge is what the node expects of
	// that parent. The bag and any expect nothing, and say nothing.
	if (edge.Kind == runtime.EdgeOne || edge.Kind == runtime.EdgeLazy) && len(edge.Parents) == 1 {
		spec, err := InputSpec(reflect.TypeFor[In]())
		if err != nil {
			return nil, err
		}
		if spec != nil {
			io.Inputs = append(io.Inputs, ioInput{key: edge.Parents[0].Key(), port: spec})
		}
	}

	// A type declared on an external node or a trigger is this project's
	// expectation of something it cannot see, however the node reads it.
	for _, parent := range edge.Parents {
		if !(parent.External() || parent.Trigger()) || parent.OutputType() == nil {
			continue
		}
		if slices.ContainsFunc(io.Inputs, func(in ioInput) bool { return in.key == parent.Key() }) {
			continue
		}
		spec, err := InputSpec(parent.OutputType())
		if err != nil {
			return nil, err
		}
		if spec != nil {
			io.Inputs = append(io.Inputs, ioInput{key: parent.Key(), port: spec})
		}
	}

	output, err := OutputSpec(outputType[Out]())
	if err != nil {
		return nil, err
	}
	io.Output = &output

	return io, nil
}

// outputType returns the reflected type of Out, or nil when Out is an interface
// type and therefore states no expectation.
func outputType[Out any]() reflect.Type {
	t := reflect.TypeFor[Out]()
	if t.Kind() == reflect.Interface {
		return nil
	}
	return t
}

// External declares a dependency on a node another project defines. Out is
// what this project expects it to produce, which the builder checks against
// what that project's SDK declared; any expects nothing.
func (wf *Workflow) External[Out any](key string) *runtime.NodeRef[Out] {
	if err := checkKey(key); err != nil {
		wf.fail(err)
	}

	return runtime.NewNodeRef[Out](key, runtime.KindExternal)
}

// ExternalNode declares an external dependency with no stated expectation.
// Retained for code written before nodes carried output types.
func (wf *Workflow) ExternalNode(key string) *runtime.NodeRef[any] {
	return wf.External[any](key)
}

// Trigger declares an event source that starts dependent nodes. Those nodes
// receive the event, decoded into Out, as their input.
func (wf *Workflow) Trigger[Out any](key string) *runtime.NodeRef[Out] {
	if err := wf.claim(key); err != nil {
		wf.fail(err)
	}

	trigger := &TriggerManifest{Key: key}
	if t := outputType[Out](); t != nil {
		schema, err := SchemaOf(t)
		if err != nil {
			wf.fail(fmt.Errorf("trigger '%s': %w", key, err))
		}
		trigger.Schema = schema
	}
	wf.triggers = append(wf.triggers, trigger)

	return runtime.NewNodeRef[Out](key, runtime.KindTrigger)
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

// Manifest validates the declared topology and returns the
// dagflows-manifest.json body for this project.
func (wf *Workflow) Manifest() (*Manifest, error) {
	if len(wf.errs) > 0 {
		return nil, wf.errs[0]
	}

	if len(wf.nodes) == 0 {
		return nil, errors.New("this workflow declares no nodes, so there is nothing to build")
	}

	keys := wf.Keys()
	triggers := make([]string, 0, len(wf.triggers))
	for _, t := range wf.triggers {
		triggers = append(triggers, t.Key)
	}

	for _, node := range wf.nodes {
		for _, parent := range node.Depends {
			if !slices.Contains(keys, parent) {
				return nil, fmt.Errorf(
					"node '%s' depends on '%s', which this workflow does not define; its nodes are: %s",
					node.Key, parent, strings.Join(slices.Sorted(slices.Values(keys)), ", "),
				)
			}
		}
		for _, source := range node.TriggeredBy {
			if !slices.Contains(triggers, source) {
				return nil, fmt.Errorf(
					"node '%s' is triggered by '%s', which this workflow does not declare; declare it with wf.Trigger(...)",
					node.Key, source,
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
			OnWarning:          wf.OnWarning,
		}

		if wf.Retry != nil {
			out.Workflow.Retry = wf.Retry.asManifest()
		}
	}

	for _, t := range wf.triggers {
		copied := *t
		out.Triggers = append(out.Triggers, &copied)
	}

	out.Nodes = make([]*NodeManifest, 0, len(wf.nodes))

	for _, node := range wf.nodes {
		copied := *node
		out.Nodes = append(out.Nodes, &copied)
	}

	return out, nil
}

// claim validates a node key and reserves it, refusing a duplicate within
// this workflow.
func (wf *Workflow) claim(key string) error {
	if err := checkKey(key); err != nil {
		return err
	}

	if wf.keys[key] {
		return fmt.Errorf("duplicate node key '%s' in this project", key)
	}

	wf.keys[key] = true

	return nil
}

// checkKey validates a node key against the identifier pattern the platform accepts.
func checkKey(key string) error {
	if !nodeKeyPattern.MatchString(key) {
		return fmt.Errorf("node key '%s' invalid, want a letter then letters, digits, _ or -, max 64 chars", key)
	}

	return nil
}

// functionName derives the node key identifier from the handler function reflection metadata.
func functionName(fn any) (string, error) {
	if fn == nil {
		return "", errors.New("a node needs a handler")
	}

	value := reflect.ValueOf(fn)
	if value.Kind() != reflect.Func || value.IsNil() {
		return "", errors.New("a node needs a handler")
	}

	full := goruntime.FuncForPC(value.Pointer()).Name()
	name := full[strings.LastIndex(full, "/")+1:]
	name = strings.TrimSuffix(name, "-fm")
	name = name[strings.LastIndex(name, ".")+1:]

	if name == "" || strings.HasPrefix(name, "func") && strings.Trim(name[4:], "0123456789") == "" {
		return "", fmt.Errorf("cannot derive a node key from %s; name the function, or set Key", full)
	}

	return name, nil
}
