package historian

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/grafana/grafana/pkg/infra/log"
	"github.com/grafana/grafana/pkg/setting"
)

const defaultClientTimeout = 30 * time.Second

type LokiConfig struct {
	ReadPathURL       *url.URL
	WritePathURL      *url.URL
	BasicAuthUser     string
	BasicAuthPassword string
	TenantID          string
	ExternalLabels    map[string]string
}

func NewLokiConfig(cfg setting.UnifiedAlertingStateHistorySettings) (LokiConfig, error) {
	read, write := cfg.LokiReadURL, cfg.LokiWriteURL
	if read == "" {
		read = cfg.LokiRemoteURL
	}
	if write == "" {
		write = cfg.LokiRemoteURL
	}

	readURL, err := url.Parse(read)
	if err != nil {
		return LokiConfig{}, fmt.Errorf("failed to parse loki remote read URL: %w", err)
	}
	writeURL, err := url.Parse(write)
	if err != nil {
		return LokiConfig{}, fmt.Errorf("failed to parse loki remote write URL: %w", err)
	}

	return LokiConfig{
		ReadPathURL:       readURL,
		WritePathURL:      writeURL,
		BasicAuthUser:     cfg.LokiBasicAuthUsername,
		BasicAuthPassword: cfg.LokiBasicAuthPassword,
		TenantID:          cfg.LokiTenantID,
	}, nil
}

type httpLokiClient struct {
	client http.Client
	cfg    LokiConfig
	log    log.Logger
}

func newLokiClient(cfg LokiConfig, logger log.Logger) *httpLokiClient {
	return &httpLokiClient{
		client: http.Client{
			Timeout: defaultClientTimeout,
		},
		cfg: cfg,
		log: logger.New("protocol", "http"),
	}
}

func (c *httpLokiClient) ping(ctx context.Context) error {
	uri := c.cfg.ReadPathURL.JoinPath("/loki/api/v1/labels")
	req, err := http.NewRequest(http.MethodGet, uri.String(), nil)
	if err != nil {
		return fmt.Errorf("error creating request: %w", err)
	}
	c.setAuthAndTenantHeaders(req)

	req = req.WithContext(ctx)
	res, err := c.client.Do(req)
	if res != nil {
		defer func() {
			if err := res.Body.Close(); err != nil {
				c.log.Warn("Failed to close response body", "err", err)
			}
		}()
	}
	if err != nil {
		return fmt.Errorf("error sending request: %w", err)
	}

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("ping request to loki endpoint returned a non-200 status code: %d", res.StatusCode)
	}
	c.log.Debug("Ping request to Loki endpoint succeeded", "status", res.StatusCode)
	return nil
}

type stream struct {
	Stream map[string]string `json:"stream"`
	Values []row             `json:"values"`
}

type row struct {
	At  time.Time
	Val string
}

func (r *row) MarshalJSON() ([]byte, error) {
	return json.Marshal([2]string{
		fmt.Sprintf("%d", r.At.UnixNano()), r.Val,
	})
}

func (c *httpLokiClient) push(ctx context.Context, s []stream) error {
	body := struct {
		Streams []stream `json:"streams"`
	}{Streams: s}
	enc, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to serialize Loki payload: %w", err)
	}

	uri := c.cfg.WritePathURL.JoinPath("/loki/api/v1/push")
	req, err := http.NewRequest(http.MethodPost, uri.String(), bytes.NewBuffer(enc))
	if err != nil {
		return fmt.Errorf("failed to create Loki request: %w", err)
	}

	c.setAuthAndTenantHeaders(req)
	req.Header.Add("content-type", "application/json")

	req = req.WithContext(ctx)
	resp, err := c.client.Do(req)
	if resp != nil {
		defer func() {
			if err := resp.Body.Close(); err != nil {
				c.log.Warn("Failed to close response body", "err", err)
			}
		}()
	}
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		byt, _ := io.ReadAll(resp.Body)
		if len(byt) > 0 {
			c.log.Error("Error response from Loki", "response", string(byt), "status", resp.StatusCode)
		} else {
			c.log.Error("Error response from Loki with an empty body", "status", resp.StatusCode)
		}
		return fmt.Errorf("received a non-200 response from loki, status: %d", resp.StatusCode)
	}
	return nil
}

func (c *httpLokiClient) setAuthAndTenantHeaders(req *http.Request) {
	if c.cfg.BasicAuthUser != "" || c.cfg.BasicAuthPassword != "" {
		req.SetBasicAuth(c.cfg.BasicAuthUser, c.cfg.BasicAuthPassword)
	}

	if c.cfg.TenantID != "" {
		req.Header.Add("X-Scope-OrgID", c.cfg.TenantID)
	}
}
