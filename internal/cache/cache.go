package cache

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"docksmith/internal/image"
)

// indexFile returns the path to the cache index JSON.
func indexFile() string {
	return filepath.Join(image.CacheDir(), "index.json")
}

// loadIndex reads the cache index from disk.
func loadIndex() (map[string]string, error) {
	data, err := os.ReadFile(indexFile())
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]string), nil
		}
		return nil, err
	}
	var idx map[string]string
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, err
	}
	return idx, nil
}

// saveIndex writes the cache index to disk.
func saveIndex(idx map[string]string) error {
	if err := image.EnsureDirs(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(indexFile(), data, 0644)
}

// CacheInput holds all inputs to the cache key for a single instruction.
type CacheInput struct {
	PrevLayerDigest string
	InstructionText string // full instruction line as written
	WorkDir         string
	EnvState        map[string]string // all accumulated env vars so far
	// For COPY only: map of sorted path -> sha256 hex of file bytes
	SourceFileHashes map[string]string
}

// ComputeKey returns a deterministic sha256 hex string for the given inputs.
func ComputeKey(inp CacheInput) string {
	h := sha256.New()

	writeLine := func(s string) {
		h.Write([]byte(s))
		h.Write([]byte("\n"))
	}

	writeLine(inp.PrevLayerDigest)
	writeLine(inp.InstructionText)
	writeLine(inp.WorkDir)

	// ENV state: sorted keys
	envKeys := make([]string, 0, len(inp.EnvState))
	for k := range inp.EnvState {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		writeLine(k + "=" + inp.EnvState[k])
	}

	// COPY source file hashes: sorted paths
	if len(inp.SourceFileHashes) > 0 {
		paths := make([]string, 0, len(inp.SourceFileHashes))
		for p := range inp.SourceFileHashes {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			writeLine(p + ":" + inp.SourceFileHashes[p])
		}
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}

// Lookup checks whether a cache key has a known layer digest,
// and that the layer file is actually on disk.
// Returns ("", false, nil) on miss, (digest, true, nil) on hit.
func Lookup(key string) (string, bool, error) {
	idx, err := loadIndex()
	if err != nil {
		return "", false, err
	}
	digest, ok := idx[key]
	if !ok {
		return "", false, nil
	}
	// Verify the layer file exists
	layerPath := filepath.Join(image.LayersDir(), digest)
	if _, err := os.Stat(layerPath); err != nil {
		// Layer missing from disk — treat as miss
		return "", false, nil
	}
	return digest, true, nil
}

// Store records a cache key -> layer digest mapping.
func Store(key, layerDigest string) error {
	idx, err := loadIndex()
	if err != nil {
		return err
	}
	idx[key] = layerDigest
	return saveIndex(idx)
}

// HashFile returns the sha256 hex digest of a file's raw bytes.
func HashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum), nil
}

// HashBytes returns the sha256 hex digest of raw bytes.
func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}

// NormalizeEnvMap returns a copy of env as a sorted KEY=value slice.
func NormalizeEnvMap(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(env))
	for _, k := range keys {
		result = append(result, k+"="+env[k])
	}
	return result
}

// EnvSliceToMap converts []string "KEY=value" to a map.
func EnvSliceToMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			m[parts[0]] = parts[1]
		}
	}
	return m
}
