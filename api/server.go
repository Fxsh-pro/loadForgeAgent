package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/loadforge/agent/engine"
	"github.com/loadforge/agent/metrics"
)

type scaleRequest struct {
	TargetVUs int `json:"targetVus"`
}

type RunStore struct {
	mu      sync.RWMutex
	runners map[string]*engine.Runner
}

func NewRunStore() *RunStore {
	return &RunStore{runners: make(map[string]*engine.Runner)}
}

func (s *RunStore) Set(runID string, r *engine.Runner) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runners[runID] = r
}

func (s *RunStore) Get(runID string) (*engine.Runner, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.runners[runID]
	return r, ok
}

func (s *RunStore) Delete(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.runners, runID)
}

// TotalActiveVUs returns the sum of active VUs across all running tasks.
func (s *RunStore) TotalActiveVUs() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := 0
	for _, r := range s.runners {
		total += r.ActiveVUs()
	}
	return total
}

// MetricsReporter submits a periodic metrics snapshot for a run.
type MetricsReporter func(runID string, snap metrics.Snapshot)

// RunCompleter is called once when a run finishes all its phases naturally.
type RunCompleter func(runID string)

type Server struct {
	store     *RunStore
	reporter  MetricsReporter
	completer RunCompleter
	collector *metrics.Collector
	httpSrv   *http.Server
}

func NewServer(addr string, store *RunStore, collector *metrics.Collector, reporter MetricsReporter, completer RunCompleter) *Server {
	s := &Server{
		store:     store,
		collector: collector,
		reporter:  reporter,
		completer: completer,
	}

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.Logger)

	r.Post("/api/tasks", s.handleStart)
	r.Post("/api/tasks/{runId}/scale", s.handleScale)
	r.Post("/api/tasks/{runId}/stop", s.handleStop)

	s.httpSrv = &http.Server{
		Addr:    addr,
		Handler: r,
	}

	return s
}

func (s *Server) ListenAndServe() error {
	log.Printf("[Server] listening on %s", s.httpSrv.Addr)
	return s.httpSrv.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	var req engine.StartTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := engine.ValidateGraph(&req.Graph); err != nil {
		http.Error(w, "invalid graph: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}

	if _, exists := s.store.Get(req.RunID); exists {
		http.Error(w, "run already active", http.StatusConflict)
		return
	}

	log.Printf("[Run %s] received start task (%d phases)", req.RunID, len(req.Phases))

	runner := engine.NewRunner(req, s.collector)
	s.store.Set(req.RunID, runner)
	runner.Start()

	go s.metricsLoop(runner)

	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleScale(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runId")

	runner, ok := s.store.Get(runID)
	if !ok {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}

	var req scaleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("[Run %s] scale → %d VUs", runID, req.TargetVUs)
	runner.Scale(req.TargetVUs)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runId")

	runner, ok := s.store.Get(runID)
	if !ok {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}

	log.Printf("[Run %s] received stop command", runID)
	go func() {
		runner.Stop()
		log.Printf("[Run %s] stopped by master command", runID)
		s.store.Delete(runID)
	}()

	w.WriteHeader(http.StatusAccepted)
}

// metricsLoop flushes metrics every 5 seconds until the run completes.
// When the runner's Done channel closes (phases finished naturally), it also
// calls the completer to notify the master.
func (s *Server) metricsLoop(runner *engine.Runner) {
	const interval = 5 * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-runner.Done():
			// Final metrics flush
			snap := s.collector.Flush(interval.Seconds())
			if s.reporter != nil {
				s.reporter(runner.RunID(), snap)
			}
			s.store.Delete(runner.RunID())
			log.Printf("[Run %s] finished — all phases complete", runner.RunID())
			// Notify master that this agent is done
			if s.completer != nil {
				s.completer(runner.RunID())
			}
			return

		case <-ticker.C:
			snap := s.collector.Flush(interval.Seconds())
			if s.reporter != nil {
				s.reporter(runner.RunID(), snap)
			}
		}
	}
}
