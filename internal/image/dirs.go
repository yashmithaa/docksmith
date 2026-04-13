package image

import (
	"os"
	"path/filepath"
)

// StateDir returns the root ~/.docksmith directory.
func StateDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		panic("cannot determine home directory: " + err.Error())
	}
	return filepath.Join(home, ".docksmith")
}

// ImagesDir returns ~/.docksmith/images/
func ImagesDir() string {
	return filepath.Join(StateDir(), "images")
}

// LayersDir returns ~/.docksmith/layers/
func LayersDir() string {
	return filepath.Join(StateDir(), "layers")
}

// CacheDir returns ~/.docksmith/cache/
func CacheDir() string {
	return filepath.Join(StateDir(), "cache")
}

// EnsureDirs creates the required state directories if they don't exist.
func EnsureDirs() error {
	for _, d := range []string{ImagesDir(), LayersDir(), CacheDir()} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return err
		}
	}
	return nil
}
