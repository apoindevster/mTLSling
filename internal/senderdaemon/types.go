package senderdaemon

import "time"

type SenderSettings struct {
	Enabled                  bool   `json:"enabled"`
	CertPath                 string `json:"cert_path"`
	KeyPath                  string `json:"key_path"`
	CAPath                   string `json:"ca_path"`
	Addr                     string `json:"addr"`
	Multicast                string `json:"multicast"`
	ServerName               string `json:"server_name"`
	ID                       string `json:"id"`
	InsecureSkipVerify       bool   `json:"insecure_skip_verify"`
	BroadcastIntervalSeconds int    `json:"broadcast_interval_seconds"`
}

type ListenerSettings struct {
	Enabled            bool   `json:"enabled"`
	CertPath           string `json:"cert_path"`
	KeyPath            string `json:"key_path"`
	CAPath             string `json:"ca_path"`
	Multicast          string `json:"multicast"`
	OutDir             string `json:"out_dir"`
	ServerNameOverride string `json:"server_name_override"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`
}

type Settings struct {
	Sender   SenderSettings   `json:"sender"`
	Listener ListenerSettings `json:"listener"`
}

type FileProgressSnapshot struct {
	Name        string `json:"name"`
	Transferred int64  `json:"transferred"`
	Total       int64  `json:"total"`
}

type ConnectionSnapshot struct {
	ID     string                 `json:"id"`
	Remote string                 `json:"remote"`
	State  string                 `json:"state"`
	Files  []FileProgressSnapshot `json:"files"`
}

type SenderState struct {
	Running     bool                 `json:"running"`
	Files       []string             `json:"files"`
	Connections []ConnectionSnapshot `json:"connections"`
	LastError   string               `json:"last_error"`
}

type ListenerEvent struct {
	Timestamp time.Time `json:"timestamp"`
	AdvertID  string    `json:"advert_id"`
	Callback  string    `json:"callback"`
	Status    string    `json:"status"`
	Detail    string    `json:"detail"`
}

type ListenerState struct {
	Running   bool            `json:"running"`
	LastError string          `json:"last_error"`
	Recent    []ListenerEvent `json:"recent"`
}

type Snapshot struct {
	Settings Settings      `json:"settings"`
	Sender   SenderState   `json:"sender"`
	Listener ListenerState `json:"listener"`
}
