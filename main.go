package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mackerelio/go-osstat/cpu"
	"github.com/mackerelio/go-osstat/memory"

	"github.com/loadforge/agent/api"
	"github.com/loadforge/agent/client"
	"github.com/loadforge/agent/config"
	"github.com/loadforge/agent/metrics"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	hostname, err := os.Hostname()
	if err != nil {
		hostname = cfg.AgentName
	}

	masterClient := client.NewMasterClient(cfg.MasterURL)

	agentID, err := resolveAgentID(cfg, masterClient, hostname)
	if err != nil {
		log.Fatalf("registration failed: %v", err)
	}
	cfg.AgentID = agentID
	log.Printf("registered successfully (url=%s id=%s)", cfg.AgentURL, cfg.AgentID)

	// On startup, check if there were in-flight runs from a previous process.
	// If so, notify master they failed (the agent lost all state on restart).
	recoverStaleRuns(cfg, masterClient)

	store := api.NewRunStore()
	collector := metrics.NewCollector()

	reporter := func(runID string, snap metrics.Snapshot) {
		point := client.MetricPointRequest{
			Time:       snap.Time.Format(time.RFC3339),
			RPS:        snap.RPS,
			LatencyP50: snap.LatencyP50,
			LatencyP90: snap.LatencyP90,
			LatencyP99: snap.LatencyP99,
			ErrorRate:  snap.ErrorRate,
		}
		log.Printf("[Metrics] run=%s rps=%.1f p50=%.1fms p99=%.1fms errRate=%.3f",
			runID, snap.RPS, snap.LatencyP50, snap.LatencyP99, snap.ErrorRate)
		if err := masterClient.SubmitMetrics(runID, cfg.AgentID, []client.MetricPointRequest{point}); err != nil {
			log.Printf("[Metrics] submit failed for run %s: %v", runID, err)
		}
	}

	completer := func(runID string) {
		log.Printf("[Run %s] all phases done — notifying master", runID)
		if err := masterClient.NotifyComplete(runID, cfg.AgentID, cfg.AgentToken); err != nil {
			log.Printf("[Run %s] notify complete failed: %v", runID, err)
		} else {
			log.Printf("[Run %s] master acknowledged completion", runID)
		}
	}

	failer := func(runID string, reason string) {
		log.Printf("[Run %s] fatal failure — notifying master: %s", runID, reason)
		if err := masterClient.NotifyFailed(runID, cfg.AgentID, cfg.AgentToken, reason); err != nil {
			log.Printf("[Run %s] notify failed error: %v", runID, err)
		} else {
			log.Printf("[Run %s] master acknowledged failure", runID)
		}
	}

	srv := api.NewServer(
		fmt.Sprintf(":%s", cfg.AgentPort),
		store,
		collector,
		reporter,
		completer,
		failer,
	)

	go func() {
		if err := srv.ListenAndServe(); err != nil {
			log.Printf("HTTP server stopped: %v", err)
		}
	}()

	go heartbeatLoop(cfg, masterClient, store)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("server shutdown: %v", err)
	}
}

func resolveAgentID(cfg *config.Config, masterClient *client.MasterClient, hostname string) (string, error) {
	log.Printf("registering with master at %s", cfg.MasterURL)

	id, err := masterClient.Register(client.AgentRegisterRequest{
		Token:    cfg.AgentToken,
		Hostname: hostname,
		Name:     cfg.AgentName,
		URL:      cfg.AgentURL,
	})
	if err != nil {
		if saved, ferr := config.LoadAgentID(); ferr == nil && saved != "" {
			log.Printf("warning: registration failed (%v), using persisted agent id %s", err, saved)
			return saved, nil
		}
		return "", err
	}

	if perr := config.PersistAgentID(id); perr != nil {
		log.Printf("warning: could not persist agent id: %v", perr)
	}

	return id, nil
}

func heartbeatLoop(cfg *config.Config, masterClient *client.MasterClient, store *api.RunStore) {
	const interval = 10 * time.Second

	// Take an initial CPU snapshot so the first delta is available immediately.
	prevCPU, _ := cpu.Get()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		cpuUsage, newSnapshot := getCPUPercent(prevCPU)
		prevCPU = newSnapshot
		ramUsage := getRAMPercent()
		currentVUs := store.TotalActiveVUs()

		log.Printf("[Heartbeat] cpu=%.1f%% ram=%.1f%% vus=%d", cpuUsage, ramUsage, currentVUs)

		if err := masterClient.Heartbeat(client.AgentHeartbeatRequest{
			AgentToken: cfg.AgentToken,
			CPUUsage:   cpuUsage,
			RAMUsage:   ramUsage,
			CurrentVUs: currentVUs,
		}); err != nil {
			log.Printf("[Heartbeat] failed: %v", err)
		}
	}
}

// getCPUPercent computes CPU usage % as a delta between two snapshots.
// Returns the percentage and the current snapshot for the next call.
func getCPUPercent(prev *cpu.Stats) (float32, *cpu.Stats) {
	curr, err := cpu.Get()
	if err != nil || prev == nil {
		return 0, curr
	}

	totalDelta := float64(curr.Total - prev.Total)
	if totalDelta <= 0 {
		return 0, curr
	}

	idleDelta := float64(curr.Idle - prev.Idle)
	used := (1 - idleDelta/totalDelta) * 100
	return float32(used), curr
}

func getRAMPercent() float32 {
	mem, err := memory.Get()
	if err != nil || mem.Total == 0 {
		return 0
	}
	used := float64(mem.Used) / float64(mem.Total) * 100
	return float32(used)
}

// recoverStaleRuns checks for runs that were active when the agent last exited.
// Since all in-memory state is lost on restart, these runs are dead — notify master.
func recoverStaleRuns(cfg *config.Config, masterClient *client.MasterClient) {
	staleRunIDs := config.LoadActiveRuns()
	if len(staleRunIDs) == 0 {
		return
	}

	log.Printf("[Recovery] found %d stale run(s) from previous process: %v", len(staleRunIDs), staleRunIDs)

	for _, runID := range staleRunIDs {
		err := masterClient.NotifyFailed(runID, cfg.AgentID, cfg.AgentToken, "agent restarted — in-flight run lost")
		if err != nil {
			log.Printf("[Recovery] failed to notify master about stale run %s: %v", runID, err)
		} else {
			log.Printf("[Recovery] notified master that run %s failed due to restart", runID)
		}
	}

	config.ClearActiveRuns()
}
