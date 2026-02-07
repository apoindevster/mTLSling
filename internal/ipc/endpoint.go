package ipc

const DefaultEndpoint = "/tmp/multi-cast-transfer.sock"

func NormalizeEndpoint(endpoint string) string {
	if endpoint == "" {
		return DefaultEndpoint
	}
	return endpoint
}
