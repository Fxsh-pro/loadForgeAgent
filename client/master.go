package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type MasterClient struct {
	masterURL  string
	httpClient *http.Client
}

func NewMasterClient(masterURL string) *MasterClient {
	return &MasterClient{
		masterURL: masterURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

type AgentRegisterRequest struct {
	Token    string `json:"token"`
	Hostname string `json:"hostname"`
	Name     string `json:"name"`
	URL      string `json:"url"`
}

type AgentRegisterResponse struct {
	ID string `json:"id"`
}

type AgentHeartbeatRequest struct {
	AgentToken string  `json:"agentToken"`
	CPUUsage   float32 `json:"cpuUsage"`
	RAMUsage   float32 `json:"ramUsage"`
	CurrentVUs int     `json:"currentVus"`
}

type MetricPointRequest struct {
	Time       string  `json:"time"`
	RPS        float64 `json:"rps"`
	LatencyP50 float64 `json:"latencyP50"`
	LatencyP90 float64 `json:"latencyP90"`
	LatencyP99 float64 `json:"latencyP99"`
	ErrorRate  float64 `json:"errorRate"`
}

type SubmitMetricsRequest struct {
	Metrics []MetricPointRequest `json:"metrics"`
}

// Register sends registration request and returns the agent UUID assigned by the master.
func (c *MasterClient) Register(req AgentRegisterRequest) (string, error) {
	url := fmt.Sprintf("%s/api/agents/register", c.masterURL)

	data, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}

	resp, err := c.httpClient.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("POST %s returned %d", url, resp.StatusCode)
	}

	var body AgentRegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decode register response: %w", err)
	}
	if body.ID == "" {
		return "", fmt.Errorf("master returned empty agent id")
	}

	return body.ID, nil
}

func (c *MasterClient) Heartbeat(req AgentHeartbeatRequest) error {
	return c.postJSON(fmt.Sprintf("%s/api/agents/heartbeat", c.masterURL), req)
}

func (c *MasterClient) SubmitMetrics(runID string, agentID string, metrics []MetricPointRequest) error {
	body := SubmitMetricsRequest{Metrics: metrics}
	url := fmt.Sprintf("%s/api/runs/%s/metrics?agentId=%s", c.masterURL, runID, agentID)
	return c.postJSON(url, body)
}

type agentRunCompleteRequest struct {
	AgentToken string `json:"agentToken"`
}

type agentRunFailRequest struct {
	AgentToken string `json:"agentToken"`
	Reason     string `json:"reason"`
}

// NotifyComplete tells the master this agent has finished all phases of the run normally.
func (c *MasterClient) NotifyComplete(runID string, agentID string, agentToken string) error {
	url := fmt.Sprintf("%s/api/runs/%s/complete?agentId=%s", c.masterURL, runID, agentID)
	return c.postJSON(url, agentRunCompleteRequest{AgentToken: agentToken})
}

// NotifyFailed tells the master this agent encountered a fatal error during the run.
// The master will mark the run and this agent's slot as FAILED with the given reason.
func (c *MasterClient) NotifyFailed(runID string, agentID string, agentToken string, reason string) error {
	url := fmt.Sprintf("%s/api/runs/%s/fail?agentId=%s", c.masterURL, runID, agentID)
	return c.postJSON(url, agentRunFailRequest{AgentToken: agentToken, Reason: reason})
}

func (c *MasterClient) postJSON(url string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	resp, err := c.httpClient.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("POST %s returned %d", url, resp.StatusCode)
	}

	return nil
}
