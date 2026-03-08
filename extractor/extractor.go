package extractor

import (
	"net/http"

	"github.com/tidwall/gjson"
)

type ExtractFrom string

const (
	ExtractFromBody   ExtractFrom = "BODY"
	ExtractFromHeader ExtractFrom = "HEADER"
)

type ExtractRule struct {
	Name string      `json:"name"`
	From ExtractFrom `json:"from"`
	Path string      `json:"path"`
}

// Apply runs all extract rules against the response body and headers,
// storing results in the provided context map.
func Apply(rules []ExtractRule, respBody []byte, respHeaders http.Header, ctx map[string]string) {
	for _, rule := range rules {
		switch rule.From {
		case ExtractFromBody:
			result := gjson.GetBytes(respBody, rule.Path)
			if result.Exists() {
				ctx[rule.Name] = result.String()
			}
		case ExtractFromHeader:
			val := respHeaders.Get(rule.Path)
			if val != "" {
				ctx[rule.Name] = val
			}
		}
	}
}
