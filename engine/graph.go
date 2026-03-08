package engine

import (
	"fmt"
	"math/rand"

	"github.com/loadforge/agent/extractor"
)

type NodeType string

const (
	NodeTypeStart    NodeType = "START"
	NodeTypeHTTP     NodeType = "HTTP"
	NodeTypeDelay    NodeType = "DELAY"
	NodeTypeCheck    NodeType = "CHECK"
	NodeTypeTerminal NodeType = "TERMINAL"
)

type NodeConfig struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type ScenarioNode struct {
	ID          int64                   `json:"id"`
	Type        NodeType                `json:"type"`
	Name        string                  `json:"name"`
	Config      NodeConfig              `json:"config"`
	Extract     []extractor.ExtractRule `json:"extract"`
	ThinkTimeMs int64                   `json:"thinkTimeMs"`
}

type ScenarioEdge struct {
	From   int64   `json:"from"`
	To     int64   `json:"to"`
	Weight float64 `json:"weight"`
}

type ScenarioGraph struct {
	StartNodeID     int64                   `json:"startNodeId"`
	TerminalNodeIDs []int64                 `json:"terminalNodeIds"`
	Nodes           map[string]ScenarioNode `json:"nodes"`
	Edges           []ScenarioEdge          `json:"edges"`
}

// NextNode picks the next node ID from the current node using weighted random selection.
// Returns -1 if there are no outgoing edges (treat as terminal).
func (g *ScenarioGraph) NextNode(currentID int64) (int64, error) {
	var candidates []ScenarioEdge
	for _, e := range g.Edges {
		if e.From == currentID {
			candidates = append(candidates, e)
		}
	}

	if len(candidates) == 0 {
		return -1, nil
	}

	r := rand.Float64()
	cumulative := 0.0
	for _, e := range candidates {
		cumulative += e.Weight
		if r <= cumulative {
			return e.To, nil
		}
	}

	// Fallback: return last candidate (handles floating-point imprecision)
	return candidates[len(candidates)-1].To, nil
}

// GetNode retrieves a node by its ID.
func (g *ScenarioGraph) GetNode(id int64) (ScenarioNode, error) {
	key := fmt.Sprintf("%d", id)
	node, ok := g.Nodes[key]
	if !ok {
		return ScenarioNode{}, fmt.Errorf("node %d not found in graph", id)
	}
	return node, nil
}

// IsTerminal returns true if the node ID is in the terminal list.
func (g *ScenarioGraph) IsTerminal(id int64) bool {
	for _, tid := range g.TerminalNodeIDs {
		if tid == id {
			return true
		}
	}
	return false
}
