package server

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const magic = "MCAST1"

// FileEntry describes a file to transfer.
type FileEntry struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// Manifest lists the files the server will stream.
type Manifest struct {
	Files []FileEntry `json:"files"`
}

// WriteManifest writes a manifest with a small header.
func WriteManifest(w io.Writer, m Manifest) error {
	bw := bufio.NewWriter(w)
	if _, err := bw.WriteString(magic); err != nil {
		return err
	}
	payload, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if err := binary.Write(bw, binary.BigEndian, uint32(len(payload))); err != nil {
		return err
	}
	if _, err := bw.Write(payload); err != nil {
		return err
	}
	return bw.Flush()
}

// ReadManifest reads a manifest written by WriteManifest.
func ReadManifest(r io.Reader) (Manifest, error) {
	br := bufio.NewReader(r)
	hdr := make([]byte, len(magic))
	if _, err := io.ReadFull(br, hdr); err != nil {
		return Manifest{}, err
	}
	if string(hdr) != magic {
		return Manifest{}, fmt.Errorf("bad manifest magic")
	}
	var n uint32
	if err := binary.Read(br, binary.BigEndian, &n); err != nil {
		return Manifest{}, err
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(br, payload); err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(payload, &m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// CopyExactly copies size bytes from r to w.
func CopyExactly(w io.Writer, r io.Reader, size int64) error {
	if _, err := io.CopyN(w, r, size); err != nil {
		return err
	}
	return nil
}
