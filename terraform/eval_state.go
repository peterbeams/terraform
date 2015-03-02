package terraform

import (
	"fmt"
)

// EvalReadState is an EvalNode implementation that reads the
// primary InstanceState for a specific resource out of the state.
type EvalReadState struct {
	Name   string
	Output **InstanceState
}

func (n *EvalReadState) Eval(ctx EvalContext) (interface{}, error) {
	return readInstanceFromState(ctx, n.Name, n.Output, func(rs *ResourceState) (*InstanceState, error) {
		return rs.Primary, nil
	})
}

// EvalReadStateTainted is an EvalNode implementation that reads a
// tainted InstanceState for a specific resource out of the state
type EvalReadStateTainted struct {
	Name   string
	Output **InstanceState

	// Tainted is a per-resource list, this index determines which item in the
	// list we are addressing
	TaintedIndex int
}

func (n *EvalReadStateTainted) Eval(ctx EvalContext) (interface{}, error) {
	return readInstanceFromState(ctx, n.Name, n.Output, func(rs *ResourceState) (*InstanceState, error) {
		// Get the index. If it is negative, then we get the last one
		idx := n.TaintedIndex
		if idx < 0 {
			idx = len(rs.Tainted) - 1
		}
		if idx >= 0 && idx < len(rs.Tainted) {
			return rs.Tainted[idx], nil
		} else {
			return nil, fmt.Errorf("bad tainted index: %d, for resource: %#v", idx, rs)
		}
	})
}

// EvalReadStateDeposed is an EvalNode implementation that reads the
// deposed InstanceState for a specific resource out of the state
type EvalReadStateDeposed struct {
	Name   string
	Output **InstanceState
}

func (n *EvalReadStateDeposed) Eval(ctx EvalContext) (interface{}, error) {
	return readInstanceFromState(ctx, n.Name, n.Output, func(rs *ResourceState) (*InstanceState, error) {
		return rs.Deposed, nil
	})
}

func readInstanceFromState(
	ctx EvalContext,
	resourceName string,
	output **InstanceState,
	f func(*ResourceState) (*InstanceState, error),
) (*InstanceState, error) {
	state, lock := ctx.State()

	// Get a read lock so we can access this instance
	lock.RLock()
	defer lock.RUnlock()

	// Look for the module state. If we don't have one, then it doesn't matter.
	mod := state.ModuleByPath(ctx.Path())
	if mod == nil {
		return nil, nil
	}

	// Look for the resource state. If we don't have one, then it is okay.
	rs := mod.Resources[resourceName]
	if rs == nil {
		return nil, nil
	}

	// Use the delegate function to get the instance state from the resource state
	is, err := f(rs)
	if err != nil {
		return nil, err
	}

	// Write the result to the output pointer
	if output != nil {
		*output = is
	}

	return is, nil
}

// EvalRequireState is an EvalNode implementation that early exits
// if the state doesn't have an ID.
type EvalRequireState struct {
	State **InstanceState
}

func (n *EvalRequireState) Eval(ctx EvalContext) (interface{}, error) {
	if n.State == nil {
		return nil, EvalEarlyExitError{}
	}

	state := *n.State
	if state == nil || state.ID == "" {
		return nil, EvalEarlyExitError{}
	}

	return nil, nil
}

// EvalUpdateStateHook is an EvalNode implementation that calls the
// PostStateUpdate hook with the current state.
type EvalUpdateStateHook struct{}

func (n *EvalUpdateStateHook) Eval(ctx EvalContext) (interface{}, error) {
	state, lock := ctx.State()

	// Get a read lock so it doesn't change while we're calling this
	lock.RLock()
	defer lock.RUnlock()

	// Call the hook
	err := ctx.Hook(func(h Hook) (HookAction, error) {
		return h.PostStateUpdate(state)
	})
	if err != nil {
		return nil, err
	}

	return nil, nil
}

// EvalWriteState is an EvalNode implementation that reads the
// InstanceState for a specific resource out of the state.
type EvalWriteState struct {
	Name                string
	ResourceType        string
	Dependencies        []string
	State               **InstanceState
	Tainted             *bool
	TaintedIndex        int
	TaintedClearPrimary bool
	Deposed             bool
}

// TODO: test
func (n *EvalWriteState) Eval(ctx EvalContext) (interface{}, error) {
	state, lock := ctx.State()
	if state == nil {
		return nil, fmt.Errorf("cannot write state to nil state")
	}

	// Get a write lock so we can access this instance
	lock.Lock()
	defer lock.Unlock()

	// Look for the module state. If we don't have one, create it.
	mod := state.ModuleByPath(ctx.Path())
	if mod == nil {
		mod = state.AddModule(ctx.Path())
	}

	// Look for the resource state.
	rs := mod.Resources[n.Name]
	if rs == nil {
		rs = &ResourceState{}
		rs.init()
		mod.Resources[n.Name] = rs
	}
	rs.Type = n.ResourceType
	rs.Dependencies = n.Dependencies

	if n.Tainted != nil && *n.Tainted {
		if n.TaintedIndex != -1 {
			rs.Tainted[n.TaintedIndex] = *n.State
		} else {
			rs.Tainted = append(rs.Tainted, *n.State)
		}

		if n.TaintedClearPrimary {
			rs.Primary = nil
		}
	} else if n.Deposed {
		rs.Deposed = *n.State
	} else {
		// Set the primary state
		rs.Primary = *n.State
	}

	return nil, nil
}

// EvalDeposeState is an EvalNode implementation that takes the primary
// out of a state and makes it Deposed. This is done at the beginning of
// create-before-destroy calls so that the create can create while preserving
// the old state of the to-be-destroyed resource.
type EvalDeposeState struct {
	Name string
}

// TODO: test
func (n *EvalDeposeState) Eval(ctx EvalContext) (interface{}, error) {
	state, lock := ctx.State()

	// Get a read lock so we can access this instance
	lock.RLock()
	defer lock.RUnlock()

	// Look for the module state. If we don't have one, then it doesn't matter.
	mod := state.ModuleByPath(ctx.Path())
	if mod == nil {
		return nil, nil
	}

	// Look for the resource state. If we don't have one, then it is okay.
	rs := mod.Resources[n.Name]
	if rs == nil {
		return nil, nil
	}

	// If we don't have a primary, we have nothing to depose
	if rs.Primary == nil {
		return nil, nil
	}

	// Depose
	rs.Deposed = rs.Primary
	rs.Primary = nil

	return nil, nil
}

// EvalUndeposeState is an EvalNode implementation that reads the
// InstanceState for a specific resource out of the state.
type EvalUndeposeState struct {
	Name string
}

// TODO: test
func (n *EvalUndeposeState) Eval(ctx EvalContext) (interface{}, error) {
	state, lock := ctx.State()

	// Get a read lock so we can access this instance
	lock.RLock()
	defer lock.RUnlock()

	// Look for the module state. If we don't have one, then it doesn't matter.
	mod := state.ModuleByPath(ctx.Path())
	if mod == nil {
		return nil, nil
	}

	// Look for the resource state. If we don't have one, then it is okay.
	rs := mod.Resources[n.Name]
	if rs == nil {
		return nil, nil
	}

	// If we don't have any desposed resource, then we don't have anything to do
	if rs.Deposed == nil {
		return nil, nil
	}

	// Undepose
	rs.Primary = rs.Deposed
	rs.Deposed = nil

	return nil, nil
}
