package image

import (
	"archive/tar"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// epoch is used to zero all file timestamps for reproducibility.
var epoch = time.Unix(0, 0).UTC()

// CreateLayerTar creates a deterministic tar archive of the given srcDir
// (or a list of explicit file entries) and writes it into ~/.docksmith/layers/.
// It returns the digest ("sha256:...") and size in bytes.
// files is a map of archive-relative path -> absolute source path.
// Entries are sorted lexicographically for reproducibility.
func CreateLayerTar(files map[string]string) (string, int64, error) {
	if err := EnsureDirs(); err != nil {
		return "", 0, err
	}

	// Sort paths for determinism
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	// Write to a temp file first, then rename to digest-based name
	tmpFile, err := os.CreateTemp(LayersDir(), "layer-*.tar.tmp")
	if err != nil {
		return "", 0, err
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	h := sha256.New()
	w := io.MultiWriter(tmpFile, h)
	tw := tar.NewWriter(w)

	for _, archivePath := range paths {
		srcPath := files[archivePath]
		info, err := os.Lstat(srcPath)
		if err != nil {
			_ = tw.Close()
			_ = tmpFile.Close()
			return "", 0, err
		}

		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			_ = tw.Close()
			_ = tmpFile.Close()
			return "", 0, err
		}

		// Normalize: clean path, zero timestamps
		hdr.Name = strings.TrimPrefix(archivePath, "/")
		if info.IsDir() && !strings.HasSuffix(hdr.Name, "/") {
			hdr.Name += "/"
		}
		hdr.ModTime = epoch
		hdr.AccessTime = epoch
		hdr.ChangeTime = epoch
		hdr.Uid = 0
		hdr.Gid = 0
		hdr.Uname = ""
		hdr.Gname = ""

		if err := tw.WriteHeader(hdr); err != nil {
			_ = tw.Close()
			_ = tmpFile.Close()
			return "", 0, err
		}

		if !info.IsDir() {
			f, err := os.Open(srcPath)
			if err != nil {
				_ = tw.Close()
				_ = tmpFile.Close()
				return "", 0, err
			}
			if _, err := io.Copy(tw, f); err != nil {
				_ = f.Close()
				_ = tw.Close()
				_ = tmpFile.Close()
				return "", 0, err
			}
			_ = f.Close()
		}
	}

	if err := tw.Close(); err != nil {
		_ = tmpFile.Close()
		return "", 0, err
	}
	if err := tmpFile.Close(); err != nil {
		return "", 0, err
	}

	digest := fmt.Sprintf("sha256:%x", h.Sum(nil))

	// Rename to digest-based name
	destPath := filepath.Join(LayersDir(), digest)
	if err := os.Rename(tmpPath, destPath); err != nil {
		// If already exists (idempotent), that's fine
		if !os.IsExist(err) {
			return "", 0, err
		}
	}

	info, err := os.Stat(destPath)
	if err != nil {
		return "", 0, err
	}

	return digest, info.Size(), nil
}

// ExtractLayerTar extracts a layer tar archive into destDir.
// Later layers overwrite earlier ones at the same path.
func ExtractLayerTar(digest, destDir string) error {
	layerPath := filepath.Join(LayersDir(), digest)
	f, err := os.Open(layerPath)
	if err != nil {
		return fmt.Errorf("layer %s not found: %w", digest, err)
	}
	defer f.Close()

	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(destDir, filepath.Clean(hdr.Name))

		// Security: prevent path traversal
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) &&
			target != filepath.Clean(destDir) {
			return fmt.Errorf("tar path traversal detected: %s", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, hdr.FileInfo().Mode()); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hdr.FileInfo().Mode())
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return err
			}
			_ = out.Close()
		case tar.TypeSymlink:
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			linkTarget := filepath.Join(destDir, filepath.Clean(hdr.Linkname))
			_ = os.Remove(target)
			if err := os.Link(linkTarget, target); err != nil {
				return err
			}
		}
	}
	return nil
}

// AssembleFilesystem extracts all layers in order into destDir.
func AssembleFilesystem(layers []LayerEntry, destDir string) error {
	for _, l := range layers {
		if err := ExtractLayerTar(l.Digest, destDir); err != nil {
			return fmt.Errorf("extracting layer %s: %w", l.Digest, err)
		}
	}
	return nil
}

// CollectFiles walks srcDir recursively and returns a sorted list of absolute paths.
func CollectFiles(srcDir string) ([]string, error) {
	var files []string
	err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)
	return files, err
}
