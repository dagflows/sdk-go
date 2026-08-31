package runtime

import (
	"fmt"
	"io"
	"reflect"
)

// Ref is a node handle of any output type, as Depends and the manifest take it
// where the declared output type does not matter.
type Ref interface {
	// Key returns the node key this handle refers to.
	Key() string
	// External reports whether this node represents an external project dependency.
	External() bool
	// Trigger reports whether this reference represents an event trigger source.
	Trigger() bool
	// OutputType returns the type the node declared it produces, nil when it declared none.
	OutputType() reflect.Type
}

// RefKind is the kind of workflow element a handle points at.
type RefKind int

const (
	KindNode RefKind = iota
	KindExternal
	KindTrigger
)

// NodeRef is a reference handle to a declared node, typed by the output that
// node produces. It is the single-parent edge of a child: wf.Node(fn, parent)
// hands fn the parent's output as its input, decoded into whatever fn declared.
type NodeRef[Out any] struct {
	key  string
	kind RefKind
}

// NewNodeRef returns a handle to the node with the given key and kind.
func NewNodeRef[Out any](key string, kind RefKind) *NodeRef[Out] {
	return &NodeRef[Out]{key: key, kind: kind}
}

func (r *NodeRef[Out]) Key() string    { return r.key }
func (r *NodeRef[Out]) External() bool { return r.kind == KindExternal }
func (r *NodeRef[Out]) Trigger() bool  { return r.kind == KindTrigger }

// OutputType returns Out, or nil when Out is any because the node declared
// nothing about its output.
func (r *NodeRef[Out]) OutputType() reflect.Type {
	t := reflect.TypeFor[Out]()
	if t == anyType {
		return nil
	}
	return t
}

func (r *NodeRef[Out]) String() string {
	switch r.kind {
	case KindTrigger:
		return fmt.Sprintf("<NodeRef '%s' trigger>", r.key)
	case KindExternal:
		return fmt.Sprintf("<NodeRef '%s' external>", r.key)
	}
	return fmt.Sprintf("<NodeRef '%s'>", r.key)
}

// EdgeKind is the shape in which a handler receives its parents.
type EdgeKind string

const (
	EdgeNone EdgeKind = "none" // No parents; the handler receives None.
	EdgeOne  EdgeKind = "one"  // One parent, decoded into In.
	EdgeLazy EdgeKind = "lazy" // One parent, as a typed handle.
	EdgeMany EdgeKind = "many" // Every parent, as the bag.
)

// EdgeSpec is the edge shape and parent list the manifest records.
type EdgeSpec struct {
	Kind    EdgeKind
	Parents []Ref
}

// Edge declares where a handler's In comes from and produces it at run time.
// The parent handle itself is the single-parent edge: *NodeRef[T] implements
// exactly Edge[T], so a handler whose In is not the parent's output type does
// not compile.
type Edge[In any] interface {
	describe() EdgeSpec
	resolve(*Inputs) (In, error)
}

// Describe reports an edge's shape for the manifest.
func Describe[In any](edge Edge[In]) EdgeSpec {
	return edge.describe()
}

// Resolve produces a handler's input from the run's inputs.
func Resolve[In any](edge Edge[In], inputs *Inputs) (In, error) {
	return edge.resolve(inputs)
}

// NodeRef is itself the single-parent edge: it implements Edge[Out].

func (r *NodeRef[Out]) describe() EdgeSpec {
	return EdgeSpec{Kind: EdgeOne, Parents: []Ref{r}}
}

func (r *NodeRef[Out]) resolve(inputs *Inputs) (Out, error) {
	handle, err := inputs.Get(r.key)
	if err != nil {
		var zero Out
		return zero, err
	}
	return decodeInput[Out](handle)
}

// many is the edge that resolves to the whole bag of parents.
type many struct{ refs []Ref }

// Depends is the edge that gathers every parent, the same word as depends=[...]
// elsewhere. It takes node handles rather than string keys, so a name that
// refers to no node is a compile error before anything is built, and an editor
// can complete the nodes that exist.
//
// The handler takes *Inputs and reaches each parent through Get, which types
// the result by the handle it was given. With no arguments it is the bag with
// nothing in it, for an untyped root node.
func Depends(parents ...Ref) Edge[*Inputs] {
	return many{refs: parents}
}

func (m many) describe() EdgeSpec {
	return EdgeSpec{Kind: EdgeMany, Parents: m.refs}
}

func (m many) resolve(inputs *Inputs) (*Inputs, error) {
	return inputs, nil
}

type root struct{}

// Root is the edge of a node with no parents, resolving to None.
var Root Edge[None] = root{}

func (root) describe() EdgeSpec {
	return EdgeSpec{Kind: EdgeNone}
}

func (root) resolve(*Inputs) (None, error) {
	return None{}, nil
}

type lazyEdge[T any] struct{ ref *NodeRef[T] }

// Lazy returns the edge that hands the handler the typed handle to its one
// parent instead of the decoded value, for a node that wants the size, the raw
// bytes, or to decide for itself when to materialise.
func Lazy[T any](parent *NodeRef[T]) Edge[*Input[T]] {
	return lazyEdge[T]{ref: parent}
}

func (l lazyEdge[T]) describe() EdgeSpec {
	return EdgeSpec{Kind: EdgeLazy, Parents: []Ref{l.ref}}
}

func (l lazyEdge[T]) resolve(inputs *Inputs) (*Input[T], error) {
	return Get(inputs, l.ref), nil
}

// Get returns one parent out of the bag, typed by its handle. A parent that is
// not there is reported by the handle's methods, listing the parents that are.
func Get[T any](inputs *Inputs, ref *NodeRef[T]) *Input[T] {
	handle, err := inputs.Get(ref.key)
	if err != nil {
		return &Input[T]{key: ref.key, missing: err}
	}
	return Typed[T](handle)
}

// decodeInput decodes one parent into the type a handler's In asks for.
func decodeInput[T any](handle *Input[any]) (T, error) {
	var zero T
	t := reflect.TypeFor[T]()

	switch {
	case t == anyType:
		value, err := handle.Value()
		if err != nil {
			return zero, err
		}
		return any(value).(T), nil

	case t == bytesType:
		body, err := handle.Bytes()
		if err != nil {
			return zero, err
		}
		defer body.Close()
		data, err := io.ReadAll(body)
		if err != nil {
			return zero, err
		}
		return any(data).(T), nil

	case isRowsType(t):
		return typedRows(handle.Iter(), t, handle.key).Interface().(T), nil

	default:
		raw, err := handle.Value()
		if err != nil {
			return zero, err
		}
		return convert[T](raw, handle.key)
	}
}
