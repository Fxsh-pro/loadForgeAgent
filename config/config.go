package config

import (
	"fmt"
	"os"
	"strings"
)

const tokenFile = ".agent_token"
const idFile = ".agent_id"
const activeRunsFile = ".active_runs"

type Config struct {
	MasterURL  string
	AgentToken string
	AgentName  string
	AgentHost  string
	AgentPort  string
	AgentURL   string
	// AgentID is the UUID assigned by the master after registration.
	// Empty on first boot; populated by main after Register() succeeds.
	AgentID string
}

func Load() (*Config, error) {
	masterURL := os.Getenv("MASTER_URL")
	if masterURL == "" {
		return nil, fmt.Errorf("MASTER_URL environment variable is required")
	}

	agentToken, err := resolveToken()
	if err != nil {
		return nil, err
	}

	agentName := os.Getenv("AGENT_NAME")
	if agentName == "" {
		agentName = "loadforge-agent"
	}

	agentHost := os.Getenv("AGENT_HOST")
	if agentHost == "" {
		agentHost = "localhost"
	}

	agentPort := os.Getenv("AGENT_PORT")
	if agentPort == "" {
		agentPort = "8081"
	}

	agentURL := fmt.Sprintf("http://%s:%s", agentHost, agentPort)

	return &Config{
		MasterURL:  masterURL,
		AgentToken: agentToken,
		AgentName:  agentName,
		AgentHost:  agentHost,
		AgentPort:  agentPort,
		AgentURL:   agentURL,
	}, nil
}

// resolveToken returns the agent token using this priority:
//  1. AGENT_TOKEN env var (always wins — useful for first boot / Docker)
//  2. .agent_token file on disk (used on restart when env var is absent)
//
// If a token is supplied via env var it is also persisted to disk so that
// subsequent restarts without the env var still work.
func resolveToken() (string, error) {
	envToken := os.Getenv("AGENT_TOKEN")

	if envToken != "" {
		if err := persistToken(envToken); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not persist token to %s: %v\n", tokenFile, err)
		}
		return envToken, nil
	}

	fileToken, err := loadTokenFromFile()
	if err == nil && fileToken != "" {
		fmt.Printf("loaded token from %s\n", tokenFile)
		return fileToken, nil
	}

	return "", fmt.Errorf(
		"no agent token found: set AGENT_TOKEN env var or ensure %s exists", tokenFile,
	)
}

func persistToken(token string) error {
	return os.WriteFile(tokenFile, []byte(token), 0600)
}

func loadTokenFromFile() (string, error) {
	data, err := os.ReadFile(tokenFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// PersistAgentID saves the master-assigned agent UUID to disk.
func PersistAgentID(id string) error {
	return os.WriteFile(idFile, []byte(id), 0600)
}

// LoadAgentID reads the previously persisted agent UUID from disk.
func LoadAgentID() (string, error) {
	data, err := os.ReadFile(idFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// SaveActiveRuns writes the set of currently active run IDs to disk.
func SaveActiveRuns(runIDs []string) error {
	content := strings.Join(runIDs, "\n")
	return os.WriteFile(activeRunsFile, []byte(content), 0600)
}

// LoadActiveRuns reads previously persisted active run IDs from disk.
// Returns an empty slice if the file does not exist.
func LoadActiveRuns() []string {
	data, err := os.ReadFile(activeRunsFile)
	if err != nil {
		return nil
	}
	var ids []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			ids = append(ids, line)
		}
	}
	return ids
}

// ClearActiveRuns removes the active runs file.
func ClearActiveRuns() {
	os.Remove(activeRunsFile)
}
