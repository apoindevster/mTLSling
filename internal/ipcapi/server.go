package ipcapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"multi-cast-transfer/internal/senderdaemon"
)

type fileListRequest struct {
	Files []string `json:"files"`
}

func NewHandler(d *senderdaemon.Daemon) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/state", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, d.Snapshot())
	})

	mux.HandleFunc("/v1/files", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req fileListRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("decode request: %v", err), http.StatusBadRequest)
			return
		}
		d.SetSenderFiles(req.Files)
		writeJSON(w, http.StatusOK, d.Snapshot())
	})

	mux.HandleFunc("/v1/settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, d.Settings())
		case http.MethodPut:
			var settings senderdaemon.Settings
			if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
				http.Error(w, fmt.Sprintf("decode settings: %v", err), http.StatusBadRequest)
				return
			}
			if err := d.UpdateSettings(settings); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, http.StatusOK, d.Snapshot())
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/v1/sender/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := d.StartSender(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, d.Snapshot())
	})

	mux.HandleFunc("/v1/sender/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		d.StopSender()
		writeJSON(w, http.StatusOK, d.Snapshot())
	})

	mux.HandleFunc("/v1/listener/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := d.StartListener(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, d.Snapshot())
	})

	mux.HandleFunc("/v1/listener/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		d.StopListener()
		writeJSON(w, http.StatusOK, d.Snapshot())
	})

	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
