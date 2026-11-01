package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Verdict is the pokemon-legality /v1/check response (docs/protocol.md there).
type Verdict struct {
	Valid      bool     `json:"valid"`
	Parsed     bool     `json:"parsed"`
	Generation int      `json:"generation"`
	Species    int      `json:"species"`
	Form       int      `json:"form"`
	Level      int      `json:"level"`
	Nickname   string   `json:"nickname"`
	OT         string   `json:"ot"`
	Shiny      bool     `json:"shiny"`
	Gender     int      `json:"gender"`
	Nature     int      `json:"nature"`
	Ability    int      `json:"ability"`
	HeldItem   int      `json:"held_item"`
	Moves      []int    `json:"moves"`
	Issues     []string `json:"issues"`
	Error      string   `json:"error,omitempty"`
}

// Checker returns a legality verdict for one raw Pokemon file.
type Checker interface {
	Check(ctx context.Context, data []byte, format string) (*Verdict, error)
}

// HTTPChecker calls the pokemon-legality service. A failure to reach it is an error, never a pass:
// nothing is stored unless the service said valid.
type HTTPChecker struct {
	URL, Key string
	Client   *http.Client
}

func (c *HTTPChecker) Check(ctx context.Context, data []byte, format string) (*Verdict, error) {
	body, _ := json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString(data), "format": format})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/v1/check", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Key != "" {
		req.Header.Set("X-Internal-Key", c.Key)
	}
	cl := c.Client
	if cl == nil {
		cl = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, fmt.Errorf("legality service: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("legality service: status %d: %s", resp.StatusCode, b)
	}
	var v Verdict
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("legality service: %w", err)
	}
	if v.Error != "" && !v.Valid {
		return &v, errors.New(v.Error)
	}
	return &v, nil
}
