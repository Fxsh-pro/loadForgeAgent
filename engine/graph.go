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
	NodeTypeGenerate NodeType = "GENERATE"
	NodeTypeTerminal NodeType = "TERMINAL"
)

type GenerateType string

const (
	GenerateTypeUUID         GenerateType = "UUID"
	GenerateTypeEmail        GenerateType = "EMAIL"
	GenerateTypeTimestamp    GenerateType = "TIMESTAMP"
	GenerateTypeRandomInt    GenerateType = "RANDOM_INT"
	GenerateTypeRandomString GenerateType = "RANDOM_STRING"
)

type GenerateRule struct {
	Name   string       `json:"name"`
	Type   GenerateType `json:"type"`
	Min    *int64       `json:"min,omitempty"`
	Max    *int64       `json:"max,omitempty"`
	Length *int         `json:"length,omitempty"`
}

type CheckOp string

const (
	CheckOpEQ          CheckOp = "EQ"
	CheckOpNE          CheckOp = "NE"
	CheckOpLT          CheckOp = "LT"
	CheckOpLE          CheckOp = "LE"
	CheckOpGT          CheckOp = "GT"
	CheckOpGE          CheckOp = "GE"
	CheckOpContains    CheckOp = "CONTAINS"
	CheckOpNotContains CheckOp = "NOT_CONTAINS"
	CheckOpExists      CheckOp = "EXISTS"
)

type CheckRule struct {
	Variable string  `json:"variable"`
	Op       CheckOp `json:"op"`
	Value    string  `json:"value,omitempty"`
}

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
	Generate    []GenerateRule          `json:"generate"`
	Checks      []CheckRule             `json:"checks"`
	ThinkTimeMs int64                   `json:"thinkTimeMs"`
}

type EdgeCondition string

const (
	EdgeConditionAny  EdgeCondition = "ANY"
	EdgeConditionPass EdgeCondition = "PASS"
	EdgeConditionFail EdgeCondition = "FAIL"
)

type ScenarioEdge struct {
	From      int64         `json:"from"`
	To        int64         `json:"to"`
	Weight    float64       `json:"weight"`
	Condition EdgeCondition `json:"condition,omitempty"`
}

type ScenarioGraph struct {
	StartNodeID     int64                   `json:"startNodeId"`
	TerminalNodeIDs []int64                 `json:"terminalNodeIds"`
	Nodes           map[string]ScenarioNode `json:"nodes"`
	Edges           []ScenarioEdge          `json:"edges"`
}

// NextNode picks the next node ID using weighted random selection.
// checkPassed is nil for non-check nodes (uses ANY edges).
// For check nodes, conditional edges (PASS/FAIL) are matched first;
// if none match, ANY edges are used as fallback.
// Returns -1 if there are no outgoing edges (treat as terminal).
func (g *ScenarioGraph) NextNode(currentID int64, checkPassed *bool) (int64, error) {
	var all []ScenarioEdge
	for _, e := range g.Edges {
		if e.From == currentID {
			all = append(all, e)
		}
	}
	if len(all) == 0 {
		return -1, nil
	}

	candidates := g.filterEdges(all, checkPassed)
	if len(candidates) == 0 {
		return -1, nil
	}

	return weightedPick(candidates), nil
}

// filterEdges selects edges matching the current check result.
// Conditional (PASS/FAIL) edges take precedence; ANY edges are the fallback.
func (g *ScenarioGraph) filterEdges(edges []ScenarioEdge, checkPassed *bool) []ScenarioEdge {
	if checkPassed != nil {
		want := EdgeConditionPass
		if !*checkPassed {
			want = EdgeConditionFail
		}
		var conditional []ScenarioEdge
		for _, e := range edges {
			if e.Condition == want {
				conditional = append(conditional, e)
			}
		}
		if len(conditional) > 0 {
			return conditional
		}
	}
	// Fall back to ANY edges (empty condition treated as ANY)
	var any []ScenarioEdge
	for _, e := range edges {
		if e.Condition == EdgeConditionAny || e.Condition == "" {
			any = append(any, e)
		}
	}
	return any
}

func weightedPick(edges []ScenarioEdge) int64 {
	r := rand.Float64()
	cumulative := 0.0
	for _, e := range edges {
		cumulative += e.Weight
		if r <= cumulative {
			return e.To
		}
	}
	return edges[len(edges)-1].To
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
