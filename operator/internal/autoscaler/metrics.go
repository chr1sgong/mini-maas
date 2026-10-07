package autoscaler

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type PromClient struct {
	base string
	hc   *http.Client
}

func NewPromClient(base string) *PromClient {
	return &PromClient{
		base: base,
		hc: &http.Client{
			Timeout: 4 * time.Second,
		},
	}
}

type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Value [2]json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
	Error string `json:"error"`
}

func (c *PromClient) QueryScaler(ctx context.Context, promql string) (value float64, hasData bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v1/query?query="+url.QueryEscape(promql), nil)
	if err != nil {
		return 0, false, err
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()

	var presp promResponse
	if err := json.NewDecoder(resp.Body).Decode(&presp); err != nil {
		return 0, false, fmt.Errorf("decode prometheus response: %w", err)
	}

	if presp.Status != "success" {
		return 0, false, fmt.Errorf("prometheus query failed: %s", presp.Error)
	}

	switch len(presp.Data.Result) {
	case 0:
		return 0, false, nil
	case 1:
	default:
		return 0, false, fmt.Errorf("expected at most 1 series, got %d (query: %s)", len(presp.Data.Result), promql)
	}

	var raw string
	if err := json.Unmarshal(presp.Data.Result[0].Value[1], &raw); err != nil {
		return 0, false, fmt.Errorf("parse sample value: %w", err)
	}

	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false, fmt.Errorf("parse sample value %q, %w", raw, err)
	}
	if math.IsNaN(v) {
		return 0, false, nil
	}

	return v, true, nil
}

func WaitingQuery(model string) string {
	return fmt.Sprintf(`sum(avg_over_time(vllm:num_requests_waiting{model_name=%q}[30s]))`, model)
}

func RunningQuery(model string) string {
	return fmt.Sprintf(`sum(vllm:num_requests_running{model_name=%q})`, model)
}

func ActualQuery(model string) string {
	return fmt.Sprintf(`count(vllm:num_requests_running{model_name=%q})`, model)
}
