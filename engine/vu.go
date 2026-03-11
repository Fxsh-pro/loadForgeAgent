package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	mathrand "math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/loadforge/agent/extractor"
	"github.com/loadforge/agent/metrics"
)

type VU struct {
	id        int
	graph     *ScenarioGraph
	collector *metrics.Collector
	client    *http.Client
}

func newVU(id int, graph *ScenarioGraph, collector *metrics.Collector) *VU {
	return &VU{
		id:        id,
		graph:     graph,
		collector: collector,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// run loops through the scenario graph until the context is cancelled.
func (v *VU) run(ctx context.Context) {
	vuCtx := make(map[string]string)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := v.walkGraph(ctx, vuCtx); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[VU %d] graph walk error: %v", v.id, err)
		}
	}
}

// walkGraph executes one full pass through the scenario graph from start to terminal.
func (v *VU) walkGraph(ctx context.Context, vuCtx map[string]string) error {
	currentID := v.graph.StartNodeID

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		node, err := v.graph.GetNode(currentID)
		if err != nil {
			return fmt.Errorf("get node %d: %w", currentID, err)
		}

		if err := v.executeNode(ctx, node, vuCtx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("[VU %d] node %d (%s) error: %v", v.id, node.ID, node.Type, err)
		}

		if node.Type == NodeTypeTerminal || v.graph.IsTerminal(currentID) {
			return nil
		}

		nextID, err := v.graph.NextNode(currentID)
		if err != nil {
			return fmt.Errorf("next node from %d: %w", currentID, err)
		}
		if nextID == -1 {
			return nil
		}

		currentID = nextID
	}
}

func (v *VU) executeNode(ctx context.Context, node ScenarioNode, vuCtx map[string]string) error {
	switch node.Type {
	case NodeTypeStart:
		// no-op

	case NodeTypeHTTP:
		return v.executeHTTP(ctx, node, vuCtx)

	case NodeTypeDelay:
		if node.ThinkTimeMs > 0 {
			select {
			case <-time.After(time.Duration(node.ThinkTimeMs) * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

	case NodeTypeCheck:
		// no-op for MVP

	case NodeTypeGenerate:
		v.executeGenerate(node, vuCtx)

	case NodeTypeTerminal:
		// handled by the loop

	default:
		log.Printf("[VU %d] unknown node type: %s", v.id, node.Type)
	}

	return nil
}

func (v *VU) executeHTTP(ctx context.Context, node ScenarioNode, vuCtx map[string]string) error {
	cfg := node.Config

	url := interpolate(cfg.URL, vuCtx)
	body := interpolate(cfg.Body, vuCtx)

	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, cfg.Method, url, bodyReader)
	if err != nil {
		v.collector.Record(0, false)
		return fmt.Errorf("build request: %w", err)
	}

	for k, val := range cfg.Headers {
		req.Header.Set(k, interpolate(val, vuCtx))
	}

	start := time.Now()
	resp, err := v.client.Do(req)
	latencyMs := float64(time.Since(start).Milliseconds())

	if err != nil {
		v.collector.Record(latencyMs, false)
		return fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		v.collector.Record(latencyMs, false)
		return fmt.Errorf("read body: %w", err)
	}

	success := resp.StatusCode >= 200 && resp.StatusCode < 300
	v.collector.Record(latencyMs, success)
	//result := gjson.GetBytes(respBody, rule.Path)
	//log.Printf("Response: %s", respBody)
	rules := toExtractRules(node.Extract)
	extractor.Apply(rules, respBody, resp.Header, vuCtx)

	return nil
}

// interpolate replaces ${key} placeholders in s with values from ctx.
func interpolate(s string, ctx map[string]string) string {
	for k, v := range ctx {
		s = strings.ReplaceAll(s, "${"+k+"}", v)
	}
	return s
}

// toExtractRules converts the engine's ExtractRule slice to the extractor package type.
func toExtractRules(rules []extractor.ExtractRule) []extractor.ExtractRule {
	return rules
}

// executeGenerate populates the VU context with generated values for each rule.
func (v *VU) executeGenerate(node ScenarioNode, vuCtx map[string]string) {
	for _, rule := range node.Generate {
		vuCtx[rule.Name] = generateValue(rule)
	}
}

func generateValue(rule GenerateRule) string {
	switch rule.Type {
	case GenerateTypeUUID:
		return newUUID()
	case GenerateTypeEmail:
		return fmt.Sprintf("user_%s@gmail.com", randString(8))
	case GenerateTypeTimestamp:
		return fmt.Sprintf("%d", time.Now().UnixMilli())
	case GenerateTypeRandomInt:
		min := int64(0)
		max := int64(1_000_000)
		if rule.Min != nil {
			min = *rule.Min
		}
		if rule.Max != nil {
			max = *rule.Max
		}
		return fmt.Sprintf("%d", min+mathrand.Int63n(max-min+1))
	case GenerateTypeRandomString:
		length := 16
		if rule.Length != nil {
			length = *rule.Length
		}
		return randString(length)
	default:
		return ""
	}
}

const randChars = "abcdefghijklmnopqrstuvwxyz0123456789"

func randString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = randChars[mathrand.Intn(len(randChars))]
	}
	return string(b)
}

// newUUID generates a random UUID v4 without external dependencies.
func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:]),
	)
}
