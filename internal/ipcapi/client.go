package ipcapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"multi-cast-transfer/internal/ipc"
	"multi-cast-transfer/internal/senderdaemon"
)

type Client struct {
	http *http.Client
	base string
}

func NewClient(endpoint string) *Client {
	transport := &http.Transport{DialContext: ipc.DialContext(endpoint)}
	return &Client{
		http: &http.Client{Transport: transport, Timeout: 3 * time.Second},
		base: "http://ipc",
	}
}

func (c *Client) State() (senderdaemon.Snapshot, error) {
	var snap senderdaemon.Snapshot
	err := c.doJSON(http.MethodGet, "/v1/state", nil, &snap)
	return snap, err
}

func (c *Client) Settings() (senderdaemon.Settings, error) {
	var settings senderdaemon.Settings
	err := c.doJSON(http.MethodGet, "/v1/settings", nil, &settings)
	return settings, err
}

func (c *Client) SetFiles(files []string) (senderdaemon.Snapshot, error) {
	var snap senderdaemon.Snapshot
	err := c.doJSON(http.MethodPut, "/v1/files", fileListRequest{Files: files}, &snap)
	return snap, err
}

func (c *Client) UpdateSettings(settings senderdaemon.Settings) (senderdaemon.Snapshot, error) {
	var snap senderdaemon.Snapshot
	err := c.doJSON(http.MethodPut, "/v1/settings", settings, &snap)
	return snap, err
}

func (c *Client) StartSender() (senderdaemon.Snapshot, error) {
	var snap senderdaemon.Snapshot
	err := c.doJSON(http.MethodPost, "/v1/sender/start", nil, &snap)
	return snap, err
}

func (c *Client) StopSender() (senderdaemon.Snapshot, error) {
	var snap senderdaemon.Snapshot
	err := c.doJSON(http.MethodPost, "/v1/sender/stop", nil, &snap)
	return snap, err
}

func (c *Client) StartListener() (senderdaemon.Snapshot, error) {
	var snap senderdaemon.Snapshot
	err := c.doJSON(http.MethodPost, "/v1/listener/start", nil, &snap)
	return snap, err
}

func (c *Client) StopListener() (senderdaemon.Snapshot, error) {
	var snap senderdaemon.Snapshot
	err := c.doJSON(http.MethodPost, "/v1/listener/stop", nil, &snap)
	return snap, err
}

func (c *Client) doJSON(method, path string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(resp.Body)
		msg := strings.TrimSpace(string(data))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("%s %s failed: %s", method, path, msg)
	}

	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return err
	}
	return nil
}
