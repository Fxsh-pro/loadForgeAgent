package engine

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/loadforge/agent/metrics"
)

type LoadProfilePhase struct {
	DurationSeconds int64 `json:"durationSeconds"`
	TargetVUs       int   `json:"targetVus"`
}

type StartTaskRequest struct {
	RunID  string             `json:"runId"`
	Graph  ScenarioGraph      `json:"graph"`
	Phases []LoadProfilePhase `json:"phases"`
}

type Runner struct {
	runID     string
	graph     *ScenarioGraph
	phases    []LoadProfilePhase
	collector *metrics.Collector

	mu        sync.Mutex
	vuCancels []context.CancelFunc
	activeVUs atomic.Int32
	targetVUs atomic.Int32

	stopCh chan struct{}
	doneCh chan struct{}
}

func NewRunner(req StartTaskRequest, collector *metrics.Collector) *Runner {
	graph := req.Graph
	return &Runner{
		runID:     req.RunID,
		graph:     &graph,
		phases:    req.Phases,
		collector: collector,
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

// ActiveVUs returns the current number of running virtual users.
func (r *Runner) ActiveVUs() int {
	return int(r.activeVUs.Load())
}

// Start begins phase execution in a background goroutine.
func (r *Runner) Start() {
	go r.runPhases()
}

// Stop signals the runner to halt immediately.
func (r *Runner) Stop() {
	select {
	case <-r.stopCh:
	default:
		close(r.stopCh)
	}
	<-r.doneCh
}

// Scale adjusts the active VU count to targetVUs immediately.
func (r *Runner) Scale(targetVUs int) {
	r.targetVUs.Store(int32(targetVUs))
	r.applyScale(targetVUs)
}

// Done returns a channel that is closed when the run finishes.
func (r *Runner) Done() <-chan struct{} {
	return r.doneCh
}

func (r *Runner) runPhases() {
	defer func() {
		r.scaleToZero()
		log.Printf("[Run %s] all VUs stopped", r.runID)
		close(r.doneCh)
	}()

	log.Printf("[Run %s] starting — %d phase(s)", r.runID, len(r.phases))

	for i, phase := range r.phases {
		select {
		case <-r.stopCh:
			log.Printf("[Run %s] stop signal received during phase %d — aborting", r.runID, i+1)
			return
		default:
		}

		log.Printf("[Run %s] phase %d/%d — targetVUs=%d duration=%ds",
			r.runID, i+1, len(r.phases), phase.TargetVUs, phase.DurationSeconds)

		r.targetVUs.Store(int32(phase.TargetVUs))
		r.applyScale(phase.TargetVUs)

		log.Printf("[Run %s] phase %d active VUs: %d", r.runID, i+1, r.ActiveVUs())

		deadline := time.After(time.Duration(phase.DurationSeconds) * time.Second)

		select {
		case <-deadline:
			log.Printf("[Run %s] phase %d/%d complete", r.runID, i+1, len(r.phases))
		case <-r.stopCh:
			log.Printf("[Run %s] stop signal received during phase %d — aborting", r.runID, i+1)
			return
		}
	}

	log.Printf("[Run %s] all phases completed normally", r.runID)
}

func (r *Runner) applyScale(target int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	current := len(r.vuCancels)

	if target > current {
		r.spawnVUs(target - current)
	} else if target < current {
		r.shrinkVUs(current - target)
	}
}

// spawnVUs starts n new VU goroutines. Must be called with mu held.
func (r *Runner) spawnVUs(n int) {
	for i := 0; i < n; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		r.vuCancels = append(r.vuCancels, cancel)

		vuID := len(r.vuCancels)
		vu := newVU(vuID, r.graph, r.collector)

		r.activeVUs.Add(1)
		go func() {
			defer r.activeVUs.Add(-1)
			vu.run(ctx)
		}()
	}
}

// shrinkVUs cancels n VU goroutines from the tail. Must be called with mu held.
func (r *Runner) shrinkVUs(n int) {
	current := len(r.vuCancels)
	if n > current {
		n = current
	}
	for i := 0; i < n; i++ {
		idx := current - 1 - i
		r.vuCancels[idx]()
		r.vuCancels[idx] = nil
	}
	r.vuCancels = r.vuCancels[:current-n]
}

func (r *Runner) scaleToZero() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cancel := range r.vuCancels {
		if cancel != nil {
			cancel()
		}
	}
	r.vuCancels = nil
}

// RunID returns the run identifier.
func (r *Runner) RunID() string {
	return r.runID
}

// Validate checks that the graph is well-formed enough to run.
func ValidateGraph(g *ScenarioGraph) error {
	if _, err := g.GetNode(g.StartNodeID); err != nil {
		return fmt.Errorf("startNodeId %d not found: %w", g.StartNodeID, err)
	}
	return nil
}
