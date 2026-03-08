package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/loadforge/agent/api"
	"github.com/loadforge/agent/client"
	"github.com/loadforge/agent/config"
	"github.com/loadforge/agent/metrics"
)

func getCPUPercent() float32 {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 {
		return 0
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 5 {
		return 0
	}
	vals := make([]float64, len(fields)-1)
	for i, f := range fields[1:] {
		vals[i], _ = strconv.ParseFloat(f, 64)
	}
	idle := vals[3]
	total := 0.0
	for _, v := range vals {
		total += v
	}
	if total == 0 {
		return 0
	}
	return float32((1 - idle/total) * 100)
}

func getRAMPercent() float32 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	var total, available float64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		val, _ := strconv.ParseFloat(fields[1], 64)
		switch fields[0] {
		case "MemTotal:":
			total = val
		case "MemAvailable:":
			available = val
		}
	}
	if total == 0 {
		return 0
	}
	return float32((1 - available/total) * 100)
}

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

	srv := api.NewServer(
		fmt.Sprintf(":%s", cfg.AgentPort),
		store,
		collector,
		reporter,
		completer,
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

// resolveAgentID registers with the master (or reconnects) and returns the agent UUID.
// On first boot: calls Register, gets UUID, persists it to .agent_id.
// On restart: loads UUID from .agent_id, then re-registers (master upserts).
func resolveAgentID(cfg *config.Config, masterClient *client.MasterClient, hostname string) (string, error) {
	log.Printf("registering with master at %s", cfg.MasterURL)

	id, err := masterClient.Register(client.AgentRegisterRequest{
		Token:    cfg.AgentToken,
		Hostname: hostname,
		Name:     cfg.AgentName,
		URL:      cfg.AgentURL,
	})
	if err != nil {
		// Registration failed — try to fall back to persisted ID so heartbeat
		// can still work if the master is temporarily unreachable on startup.
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
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if err := masterClient.Heartbeat(client.AgentHeartbeatRequest{
			AgentToken: cfg.AgentToken,
			CPUUsage:   getCPUPercent(),
			RAMUsage:   getRAMPercent(),
			CurrentVUs: store.TotalActiveVUs(),
		}); err != nil {
			log.Printf("heartbeat failed: %v", err)
		}
	}
}
