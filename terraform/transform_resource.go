package terraform

import (
	"fmt"

	"github.com/hashicorp/terraform/config"
	"github.com/hashicorp/terraform/dag"
)

// ResourceCountTransformer is a GraphTransformer that expands the count
// out for a specific resource.
type ResourceCountTransformer struct {
	Resource *config.Resource
	Destroy  bool
}

func (t *ResourceCountTransformer) Transform(g *Graph) error {
	// Expand the resource count
	count, err := t.Resource.Count()
	if err != nil {
		return err
	}

	// Don't allow the count to be negative
	if count < 0 {
		return fmt.Errorf("negative count: %d", count)
	}

	// For each count, build and add the node
	nodes := make([]dag.Vertex, count)
	for i := 0; i < count; i++ {
		// Set the index. If our count is 1 we special case it so that
		// we handle the "resource.0" and "resource" boundary properly.
		index := i
		if count == 1 {
			index = -1
		}

		// Save the node for later so we can do connections. Make the
		// proper node depending on if we're just a destroy node or if
		// were a regular node.
		var node dag.Vertex = &graphNodeExpandedResource{
			Index:    index,
			Resource: t.Resource,
		}
		if t.Destroy {
			node = &graphNodeExpandedResourceDestroy{
				graphNodeExpandedResource: node.(*graphNodeExpandedResource),
			}
		}

		// Add the node now
		nodes[i] = node
		g.Add(nodes[i])
	}

	// Make the dependency connections
	for _, n := range nodes {
		// Connect the dependents. We ignore the return value for missing
		// dependents since that should've been caught at a higher level.
		g.ConnectDependent(n)
	}

	return nil
}

type graphNodeExpandedResource struct {
	Index    int
	Resource *config.Resource
}

func (n *graphNodeExpandedResource) Name() string {
	if n.Index == -1 {
		return n.Resource.Id()
	}

	return fmt.Sprintf("%s #%d", n.Resource.Id(), n.Index)
}

// GraphNodeDependable impl.
func (n *graphNodeExpandedResource) DependableName() []string {
	return []string{
		n.Resource.Id(),
		n.stateId(),
	}
}

// GraphNodeDependent impl.
func (n *graphNodeExpandedResource) DependentOn() []string {
	config := &GraphNodeConfigResource{Resource: n.Resource}
	return config.DependentOn()
}

// GraphNodeProviderConsumer
func (n *graphNodeExpandedResource) ProvidedBy() []string {
	return []string{resourceProvider(n.Resource.Type)}
}

// GraphNodeEvalable impl.
func (n *graphNodeExpandedResource) EvalTree() EvalNode {
	var diff *InstanceDiff
	var provider ResourceProvider
	var resourceConfig *ResourceConfig
	var state *InstanceState

	// Build the resource. If we aren't part of a multi-resource, then
	// we still consider ourselves as count index zero.
	index := n.Index
	if index < 0 {
		index = 0
	}
	resource := &Resource{
		Name:       n.Resource.Name,
		Type:       n.Resource.Type,
		CountIndex: index,
	}

	seq := &EvalSequence{Nodes: make([]EvalNode, 0, 5)}

	// Validate the resource
	vseq := &EvalSequence{Nodes: make([]EvalNode, 0, 5)}
	vseq.Nodes = append(vseq.Nodes, &EvalGetProvider{
		Name:   n.ProvidedBy()[0],
		Output: &provider,
	})
	vseq.Nodes = append(vseq.Nodes, &EvalInterpolate{
		Config:   n.Resource.RawConfig,
		Resource: resource,
		Output:   &resourceConfig,
	})
	vseq.Nodes = append(vseq.Nodes, &EvalValidateResource{
		Provider:     &provider,
		Config:       &resourceConfig,
		ResourceName: n.Resource.Name,
		ResourceType: n.Resource.Type,
	})

	// Validate all the provisioners
	for _, p := range n.Resource.Provisioners {
		var provisioner ResourceProvisioner
		vseq.Nodes = append(vseq.Nodes, &EvalGetProvisioner{
			Name:   p.Type,
			Output: &provisioner,
		}, &EvalInterpolate{
			Config:   p.RawConfig,
			Resource: resource,
			Output:   &resourceConfig,
		}, &EvalValidateProvisioner{
			Provisioner: &provisioner,
			Config:      &resourceConfig,
		})
	}

	// Add the validation operations
	seq.Nodes = append(seq.Nodes, &EvalOpFilter{
		Ops:  []walkOperation{walkValidate},
		Node: vseq,
	})

	// Build instance info
	info := n.instanceInfo()
	seq.Nodes = append(seq.Nodes, &EvalInstanceInfo{Info: info})

	// Refresh the resource
	seq.Nodes = append(seq.Nodes, &EvalOpFilter{
		Ops: []walkOperation{walkRefresh},
		Node: &EvalSequence{
			Nodes: []EvalNode{
				&EvalGetProvider{
					Name:   n.ProvidedBy()[0],
					Output: &provider,
				},
				&EvalReadState{
					Name:   n.stateId(),
					Output: &state,
				},
				&EvalRefresh{
					Info:     info,
					Provider: &provider,
					State:    &state,
					Output:   &state,
				},
				&EvalWriteState{
					Name:         n.stateId(),
					ResourceType: n.Resource.Type,
					Dependencies: n.DependentOn(),
					State:        &state,
				},
			},
		},
	})

	// Diff the resource
	seq.Nodes = append(seq.Nodes, &EvalOpFilter{
		Ops: []walkOperation{walkPlan},
		Node: &EvalSequence{
			Nodes: []EvalNode{
				&EvalInterpolate{
					Config:   n.Resource.RawConfig,
					Resource: resource,
					Output:   &resourceConfig,
				},
				&EvalGetProvider{
					Name:   n.ProvidedBy()[0],
					Output: &provider,
				},
				&EvalReadState{
					Name:   n.stateId(),
					Output: &state,
				},
				&EvalDiff{
					Info:        info,
					Config:      &resourceConfig,
					Provider:    &provider,
					State:       &state,
					Output:      &diff,
					OutputState: &state,
				},
				&EvalWriteState{
					Name:         n.stateId(),
					ResourceType: n.Resource.Type,
					Dependencies: n.DependentOn(),
					State:        &state,
				},
				&EvalDiffTainted{
					Diff: &diff,
					Name: n.stateId(),
				},
				&EvalWriteDiff{
					Name: n.stateId(),
					Diff: &diff,
				},
			},
		},
	})

	// Diff the resource for destruction
	seq.Nodes = append(seq.Nodes, &EvalOpFilter{
		Ops: []walkOperation{walkPlanDestroy},
		Node: &EvalSequence{
			Nodes: []EvalNode{
				&EvalReadState{
					Name:   n.stateId(),
					Output: &state,
				},
				&EvalDiffDestroy{
					Info:   info,
					State:  &state,
					Output: &diff,
				},
				&EvalWriteDiff{
					Name: n.stateId(),
					Diff: &diff,
				},
			},
		},
	})

	// Apply
	var diffApply *InstanceDiff
	var err error
	var createNew, tainted bool
	var createBeforeDestroyEnabled bool
	seq.Nodes = append(seq.Nodes, &EvalOpFilter{
		Ops: []walkOperation{walkApply},
		Node: &EvalSequence{
			Nodes: []EvalNode{
				// Get the saved diff for apply
				&EvalReadDiff{
					Name: n.stateId(),
					Diff: &diffApply,
				},

				// We don't want to do any destroys
				&EvalIf{
					If: func(ctx EvalContext) (bool, error) {
						if diffApply == nil {
							return true, EvalEarlyExitError{}
						}

						if diffApply.Destroy && len(diffApply.Attributes) == 0 {
							return true, EvalEarlyExitError{}
						}

						diffApply.Destroy = false
						return true, nil
					},
					Then: EvalNoop{},
				},

				&EvalIf{
					If: func(ctx EvalContext) (bool, error) {
						destroy := false
						if diffApply != nil {
							destroy = diffApply.Destroy || diffApply.RequiresNew()
						}

						createBeforeDestroyEnabled =
							n.Resource.Lifecycle.CreateBeforeDestroy &&
								destroy

						return createBeforeDestroyEnabled, nil
					},
					Then: &EvalDeposeState{
						Name: n.stateId(),
					},
				},

				&EvalInterpolate{
					Config:   n.Resource.RawConfig,
					Resource: resource,
					Output:   &resourceConfig,
				},
				&EvalGetProvider{
					Name:   n.ProvidedBy()[0],
					Output: &provider,
				},
				&EvalReadState{
					Name:   n.stateId(),
					Output: &state,
				},

				&EvalDiff{
					Info:     info,
					Config:   &resourceConfig,
					Provider: &provider,
					State:    &state,
					Output:   &diffApply,
				},

				// Get the saved diff
				&EvalReadDiff{
					Name: n.stateId(),
					Diff: &diff,
				},

				// Compare the diffs
				&EvalCompareDiff{
					Info: info,
					One:  &diff,
					Two:  &diffApply,
				},

				&EvalGetProvider{
					Name:   n.ProvidedBy()[0],
					Output: &provider,
				},
				&EvalReadState{
					Name:   n.stateId(),
					Output: &state,
				},
				&EvalApply{
					Info:      info,
					State:     &state,
					Diff:      &diffApply,
					Provider:  &provider,
					Output:    &state,
					Error:     &err,
					CreateNew: &createNew,
				},
				&EvalWriteState{
					Name:         n.stateId(),
					ResourceType: n.Resource.Type,
					Dependencies: n.DependentOn(),
					State:        &state,
				},
				&EvalApplyProvisioners{
					Info:           info,
					State:          &state,
					Resource:       n.Resource,
					InterpResource: resource,
					CreateNew:      &createNew,
					Tainted:        &tainted,
					Error:          &err,
				},
				&EvalIf{
					If: func(ctx EvalContext) (bool, error) {
						if createBeforeDestroyEnabled {
							tainted = err != nil
						}

						failure := tainted || err != nil
						return createBeforeDestroyEnabled && failure, nil
					},
					Then: &EvalUndeposeState{
						Name: n.stateId(),
					},
				},

				// We clear the diff out here so that future nodes
				// don't see a diff that is already complete. There
				// is no longer a diff!
				&EvalWriteDiff{
					Name: n.stateId(),
					Diff: nil,
				},

				&EvalWriteState{
					Name:                n.stateId(),
					ResourceType:        n.Resource.Type,
					Dependencies:        n.DependentOn(),
					State:               &state,
					Tainted:             &tainted,
					TaintedIndex:        -1,
					TaintedClearPrimary: !n.Resource.Lifecycle.CreateBeforeDestroy,
				},
				&EvalApplyPost{
					Info:  info,
					State: &state,
					Error: &err,
				},
				&EvalUpdateStateHook{},
			},
		},
	})

	return seq
}

// instanceInfo is used for EvalTree.
func (n *graphNodeExpandedResource) instanceInfo() *InstanceInfo {
	return &InstanceInfo{Id: n.stateId(), Type: n.Resource.Type}
}

// stateId is the name used for the state key
func (n *graphNodeExpandedResource) stateId() string {
	if n.Index == -1 {
		return n.Resource.Id()
	}

	return fmt.Sprintf("%s.%d", n.Resource.Id(), n.Index)
}

// GraphNodeStateRepresentative impl.
func (n *graphNodeExpandedResource) StateId() []string {
	return []string{n.stateId()}
}

// graphNodeExpandedResourceDestroy represents an expanded resource that
// is to be destroyed.
type graphNodeExpandedResourceDestroy struct {
	*graphNodeExpandedResource
}

func (n *graphNodeExpandedResourceDestroy) Name() string {
	return fmt.Sprintf("%s (destroy)", n.graphNodeExpandedResource.Name())
}

// GraphNodeEvalable impl.
func (n *graphNodeExpandedResourceDestroy) EvalTree() EvalNode {
	info := n.instanceInfo()

	var diffApply *InstanceDiff
	var provider ResourceProvider
	var state *InstanceState
	var err error
	return &EvalOpFilter{
		Ops: []walkOperation{walkApply},
		Node: &EvalSequence{
			Nodes: []EvalNode{
				// Get the saved diff for apply
				&EvalReadDiff{
					Name: n.stateId(),
					Diff: &diffApply,
				},

				// Filter the diff so we only get the destroy
				&EvalFilterDiff{
					Diff:    &diffApply,
					Output:  &diffApply,
					Destroy: true,
				},

				// If we're not destroying, then compare diffs
				&EvalIf{
					If: func(ctx EvalContext) (bool, error) {
						if diffApply != nil && diffApply.Destroy {
							return true, nil
						}

						return true, EvalEarlyExitError{}
					},
					Then: EvalNoop{},
				},

				&EvalGetProvider{
					Name:   n.ProvidedBy()[0],
					Output: &provider,
				},
				&EvalIf{
					If: func(ctx EvalContext) (bool, error) {
						return n.Resource.Lifecycle.CreateBeforeDestroy, nil
					},
					Then: &EvalReadStateTainted{
						Name:         n.stateId(),
						Output:       &state,
						TaintedIndex: -1,
					},
					Else: &EvalReadState{
						Name:   n.stateId(),
						Output: &state,
					},
				},
				&EvalRequireState{
					State: &state,
				},
				&EvalApply{
					Info:     info,
					State:    &state,
					Diff:     &diffApply,
					Provider: &provider,
					Output:   &state,
					Error:    &err,
				},
				&EvalWriteState{
					Name:         n.stateId(),
					ResourceType: n.Resource.Type,
					Dependencies: n.DependentOn(),
					State:        &state,
				},
				&EvalApplyPost{
					Info:  info,
					State: &state,
					Error: &err,
				},
			},
		},
	}
}
