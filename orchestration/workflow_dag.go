package orchestration

import (
	"fmt"
	"sync"
)

// WorkflowDAG represents a directed acyclic graph for workflow execution
type WorkflowDAG struct {
	nodes map[string]*DAGNode
	mu    sync.RWMutex
}

// DAGNode represents a node in the workflow DAG
type DAGNode struct {
	ID           string
	Dependencies []string
	Dependents   []string
	Status       NodeStatus
}

// NodeStatus represents the execution status of a DAG node
type NodeStatus int

const (
	NodePending NodeStatus = iota
	NodeReady
	NodeRunning
	NodeCompleted
	NodeFailed
	NodeSkipped
)

// NewWorkflowDAG creates a new workflow DAG
func NewWorkflowDAG() *WorkflowDAG {
	return &WorkflowDAG{
		nodes: make(map[string]*DAGNode),
	}
}

// AddNode adds a node to the DAG
func (d *WorkflowDAG) AddNode(id string, dependencies []string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Check if node already exists
	if existingNode, exists := d.nodes[id]; exists {
		// Update existing node's dependencies
		existingNode.Dependencies = dependencies
	} else {
		// Create new node
		node := &DAGNode{
			ID:           id,
			Dependencies: dependencies,
			Dependents:   []string{},
			Status:       NodePending,
		}
		d.nodes[id] = node
	}

	// Rebuild all dependents relationships
	d.rebuildDependents()
}

// rebuildDependents rebuilds the dependents list for all nodes
func (d *WorkflowDAG) rebuildDependents() {
	// Clear all dependents
	for _, node := range d.nodes {
		node.Dependents = []string{}
	}

	// Rebuild dependents based on dependencies
	for nodeID, node := range d.nodes {
		for _, dep := range node.Dependencies {
			if depNode, exists := d.nodes[dep]; exists {
				// Check if already in dependents to avoid duplicates
				found := false
				for _, existing := range depNode.Dependents {
					if existing == nodeID {
						found = true
						break
					}
				}
				if !found {
					depNode.Dependents = append(depNode.Dependents, nodeID)
				}
			}
		}
	}
}

// Validate checks if the DAG is valid (no cycles)
func (d *WorkflowDAG) Validate() error {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// Check for cycles using DFS
	visited := make(map[string]bool)
	recStack := make(map[string]bool)

	for nodeID := range d.nodes {
		if !visited[nodeID] {
			if d.hasCycleDFS(nodeID, visited, recStack) {
				return fmt.Errorf("workflow contains circular dependencies")
			}
		}
	}

	// Check all dependencies exist
	for nodeID, node := range d.nodes {
		for _, dep := range node.Dependencies {
			if _, exists := d.nodes[dep]; !exists {
				return fmt.Errorf("node %s depends on non-existent node %s", nodeID, dep)
			}
		}
	}

	return nil
}

// hasCycleDFS performs depth-first search to detect cycles
func (d *WorkflowDAG) hasCycleDFS(nodeID string, visited, recStack map[string]bool) bool {
	visited[nodeID] = true
	recStack[nodeID] = true

	node := d.nodes[nodeID]
	for _, dependent := range node.Dependents {
		if !visited[dependent] {
			if d.hasCycleDFS(dependent, visited, recStack) {
				return true
			}
		} else if recStack[dependent] {
			return true // Cycle detected
		}
	}

	recStack[nodeID] = false
	return false
}

// GetReadyNodes returns nodes that are ready to execute
func (d *WorkflowDAG) GetReadyNodes() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var ready []string

	for nodeID, node := range d.nodes {
		if node.Status == NodePending && d.allDependenciesComplete(nodeID) {
			ready = append(ready, nodeID)
		}
	}

	return ready
}

// allDependenciesComplete checks if all dependencies of a node are completed
func (d *WorkflowDAG) allDependenciesComplete(nodeID string) bool {
	node := d.nodes[nodeID]

	for _, dep := range node.Dependencies {
		depNode := d.nodes[dep]
		if depNode.Status != NodeCompleted && depNode.Status != NodeSkipped {
			return false
		}
	}

	return true
}

// MarkNodeRunning marks a node as running
func (d *WorkflowDAG) MarkNodeRunning(nodeID string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if node, exists := d.nodes[nodeID]; exists {
		node.Status = NodeRunning
	}
}

// MarkNodeCompleted marks a node as completed
func (d *WorkflowDAG) MarkNodeCompleted(nodeID string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if node, exists := d.nodes[nodeID]; exists {
		node.Status = NodeCompleted
	}
}

// MarkNodeFailed marks a node as failed
func (d *WorkflowDAG) MarkNodeFailed(nodeID string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if node, exists := d.nodes[nodeID]; exists {
		node.Status = NodeFailed

		// Optionally mark dependents as skipped
		d.markDependentsSkipped(nodeID)
	}
}

// markDependentsSkipped marks all dependents of a failed node as skipped
func (d *WorkflowDAG) markDependentsSkipped(nodeID string) {
	node := d.nodes[nodeID]

	for _, dependent := range node.Dependents {
		if depNode := d.nodes[dependent]; depNode != nil && depNode.Status == NodePending {
			depNode.Status = NodeSkipped
			// Recursively skip their dependents
			d.markDependentsSkipped(dependent)
		}
	}
}

// IsComplete checks if all nodes are in a terminal state
func (d *WorkflowDAG) IsComplete() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()

	for _, node := range d.nodes {
		if node.Status == NodePending || node.Status == NodeRunning || node.Status == NodeReady {
			return false
		}
	}

	return true
}

// HasRunningNodes checks if there are any running nodes
func (d *WorkflowDAG) HasRunningNodes() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()

	for _, node := range d.nodes {
		if node.Status == NodeRunning {
			return true
		}
	}

	return false
}

// GetNode returns a specific node
func (d *WorkflowDAG) GetNode(nodeID string) *DAGNode {
	d.mu.RLock()
	defer d.mu.RUnlock()

	return d.nodes[nodeID]
}

// GetTopologicalOrder returns nodes in topological order
func (d *WorkflowDAG) GetTopologicalOrder() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// Count incoming edges for each node
	inDegree := make(map[string]int)
	for nodeID, node := range d.nodes {
		inDegree[nodeID] = len(node.Dependencies)
	}

	// Find nodes with no dependencies
	var queue []string
	for nodeID, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, nodeID)
		}
	}

	var result []string

	// Process queue
	for len(queue) > 0 {
		// Pop from queue
		current := queue[0]
		queue = queue[1:]
		result = append(result, current)

		// Reduce in-degree for dependents
		if node := d.nodes[current]; node != nil {
			for _, dependent := range node.Dependents {
				inDegree[dependent]--
				if inDegree[dependent] == 0 {
					queue = append(queue, dependent)
				}
			}
		}
	}

	return result
}

// GetExecutionLevels returns nodes grouped by execution level (can run in parallel)
func (d *WorkflowDAG) GetExecutionLevels() [][]string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	levels := [][]string{}
	processed := make(map[string]bool)

	for {
		// Find nodes that can execute at this level
		var levelNodes []string

		for nodeID, node := range d.nodes {
			if processed[nodeID] {
				continue
			}

			// Check if all dependencies are processed
			canExecute := true
			for _, dep := range node.Dependencies {
				if !processed[dep] {
					canExecute = false
					break
				}
			}

			if canExecute {
				levelNodes = append(levelNodes, nodeID)
			}
		}

		if len(levelNodes) == 0 {
			break
		}

		// Mark as processed
		for _, nodeID := range levelNodes {
			processed[nodeID] = true
		}

		levels = append(levels, levelNodes)
	}

	return levels
}

// GetStatistics returns DAG statistics
func (d *WorkflowDAG) GetStatistics() DAGStatistics {
	d.mu.RLock()
	defer d.mu.RUnlock()

	stats := DAGStatistics{
		TotalNodes: len(d.nodes),
	}

	for _, node := range d.nodes {
		switch node.Status {
		case NodePending:
			stats.PendingNodes++
		case NodeRunning:
			stats.RunningNodes++
		case NodeCompleted:
			stats.CompletedNodes++
		case NodeFailed:
			stats.FailedNodes++
		case NodeSkipped:
			stats.SkippedNodes++
		}

		// Track max dependencies
		if len(node.Dependencies) > stats.MaxDependencies {
			stats.MaxDependencies = len(node.Dependencies)
		}

		// Track max dependents (fan-out)
		if len(node.Dependents) > stats.MaxDependents {
			stats.MaxDependents = len(node.Dependents)
		}
	}

	// Calculate parallelism potential
	levels := d.GetExecutionLevels()
	for _, level := range levels {
		if len(level) > stats.MaxParallelism {
			stats.MaxParallelism = len(level)
		}
	}

	stats.Depth = len(levels)

	return stats
}

// DAGStatistics provides statistics about the DAG
type DAGStatistics struct {
	TotalNodes      int
	PendingNodes    int
	RunningNodes    int
	CompletedNodes  int
	FailedNodes     int
	SkippedNodes    int
	MaxDependencies int // Maximum number of dependencies for any node
	MaxDependents   int // Maximum number of dependents for any node
	MaxParallelism  int // Maximum number of nodes that can run in parallel
	Depth           int // Number of execution levels
}

// Reset resets all node statuses to pending
func (d *WorkflowDAG) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, node := range d.nodes {
		node.Status = NodePending
	}
}

// Clone creates a deep copy of the DAG
func (d *WorkflowDAG) Clone() *WorkflowDAG {
	d.mu.RLock()
	defer d.mu.RUnlock()

	newDAG := NewWorkflowDAG()

	for nodeID, node := range d.nodes {
		newNode := &DAGNode{
			ID:           node.ID,
			Dependencies: make([]string, len(node.Dependencies)),
			Dependents:   make([]string, len(node.Dependents)),
			Status:       node.Status,
		}

		copy(newNode.Dependencies, node.Dependencies)
		copy(newNode.Dependents, node.Dependents)

		newDAG.nodes[nodeID] = newNode
	}

	return newDAG
}
