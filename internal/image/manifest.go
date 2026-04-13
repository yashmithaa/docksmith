package image

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LayerEntry is a single layer reference in a manifest.
type LayerEntry struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	CreatedBy string `json:"createdBy"`
}

// Config holds the runtime configuration stored in the manifest.
type Config struct {
	Env        []string `json:"Env"`
	Cmd        []string `json:"Cmd"`
	WorkingDir string   `json:"WorkingDir"`
}

// Manifest is the JSON document stored in ~/.docksmith/images/.
type Manifest struct {
	Name    string       `json:"name"`
	Tag     string       `json:"tag"`
	Digest  string       `json:"digest"`
	Created string       `json:"created"`
	Config  Config       `json:"config"`
	Layers  []LayerEntry `json:"layers"`
}

// manifestFilename returns the filename for a given name:tag pair.
func manifestFilename(name, tag string) string {
	safe := strings.ReplaceAll(name+"-"+tag, "/", "_")
	return filepath.Join(ImagesDir(), safe+".json")
}

// Save writes the manifest to disk, computing the digest correctly:
// serialize with digest="", compute sha256, then write with the real digest.
// If createdAt is non-zero, it is used as the created timestamp (for cache-hit rebuilds).
func (m *Manifest) Save(createdAt time.Time) error {
	if err := EnsureDirs(); err != nil {
		return err
	}

	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	m.Created = createdAt.Format(time.RFC3339Nano)

	// Compute digest: serialize with digest="" then hash.
	tmp := *m
	tmp.Digest = ""
	canonical, err := json.Marshal(tmp)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(canonical)
	m.Digest = fmt.Sprintf("sha256:%x", sum)

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(manifestFilename(m.Name, m.Tag), data, 0644)
}

// Load reads a manifest from disk for the given name:tag.
func Load(name, tag string) (*Manifest, error) {
	data, err := os.ReadFile(manifestFilename(name, tag))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("image %s:%s not found in local store", name, tag)
		}
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("corrupt manifest for %s:%s: %w", name, tag, err)
	}
	return &m, nil
}

// List prints all images in the local store.
func List() error {
	if err := EnsureDirs(); err != nil {
		return err
	}
	entries, err := os.ReadDir(ImagesDir())
	if err != nil {
		return err
	}

	// Header
	fmt.Printf("%-20s %-12s %-14s %-30s\n", "NAME", "TAG", "ID", "CREATED")

	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(ImagesDir(), e.Name()))
		if err != nil {
			continue
		}
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		id := m.Digest
		if strings.HasPrefix(id, "sha256:") {
			id = id[7:]
		}
		if len(id) > 12 {
			id = id[:12]
		}
		fmt.Printf("%-20s %-12s %-14s %-30s\n", m.Name, m.Tag, id, m.Created)
	}
	return nil
}

// Remove deletes the manifest and all its layer files.
func Remove(name, tag string) error {
	m, err := Load(name, tag)
	if err != nil {
		return err
	}

	// Delete layer files — digest is stored as "sha256:..." which is the filename
	for _, l := range m.Layers {
		layerPath := filepath.Join(LayersDir(), l.Digest)
		if err := os.Remove(layerPath); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "warning: could not remove layer %s: %v\n", l.Digest, err)
		}
	}

	// Delete manifest
	if err := os.Remove(manifestFilename(name, tag)); err != nil {
		return fmt.Errorf("failed to remove manifest: %w", err)
	}
	fmt.Printf("Untagged: %s:%s\n", name, tag)
	return nil
}
