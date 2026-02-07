//go:build windows

package ipc

import (
	"context"
	"errors"
	"net"
)

func Listen(endpoint string) (net.Listener, error) {
	return nil, errors.New("ipc listen not implemented for windows yet")
}

func DialContext(endpoint string) func(context.Context, string, string) (net.Conn, error) {
	return func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("ipc dial not implemented for windows yet")
	}
}
