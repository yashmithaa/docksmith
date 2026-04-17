package builder

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"docksmith/internal/cache"
	"docksmith/internal/image"
	"docksmith/internal/parser"
	"docksmith/internal/runtime"
)

func Build(name, tag, contextDir string, noCache bool) error {
	if err := image.EnsureDirs(); err != nil {
		return err
	}

	absContext, err := filepath.Abs(contextDir)
	if err != nil {
		return fmt.Errorf("resolving context: %w", err)
	}
	info, err := os.Stat(absContext)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("build context %s is not a directory", contextDir)
	}

	instructions, err := parser.ParseFile(absContext)
	if err != nil {
		return err
	}

	totalSteps := len(instructions)

	var (
		layers          []image.LayerEntry
		envState        = make(map[string]string)
		workDir         string
		cmdConfig       []string
		prevDigest      string
		cacheCascade    bool
		buildStart      = time.Now()
		existingCreated time.Time
	)

	if existing, err := image.Load(name, tag); err == nil {
		if t, err := time.Parse(time.RFC3339Nano, existing.Created); err == nil {
			existingCreated = t
		}
	}

	stepIdx := 0

	for _, instr := range instructions {
		stepIdx++

		switch instr.Type {
		case parser.FROM:
			fmt.Printf("Step %d/%d : FROM %s:%s\n", stepIdx, totalSteps, instr.FromImage, instr.FromTag)

			baseManifest, err := image.Load(instr.FromImage, instr.FromTag)
			if err != nil {
				return fmt.Errorf("step %d: %w", stepIdx, err)
			}

			layers = make([]image.LayerEntry, len(baseManifest.Layers))
			copy(layers, baseManifest.Layers)

			for _, e := range baseManifest.Config.Env {
				parts := strings.SplitN(e, "=", 2)
				if len(parts) == 2 {
					envState[parts[0]] = parts[1]
				}
			}
			if baseManifest.Config.WorkingDir != "" {
				workDir = baseManifest.Config.WorkingDir
			}
			cmdConfig = baseManifest.Config.Cmd
			prevDigest = baseManifest.Digest

		case parser.COPY:
			stepStart := time.Now()

			sourceFiles, err := expandGlobs(absContext, instr.CopySrc)
			if err != nil {
				return fmt.Errorf("step %d COPY: %w", stepIdx, err)
			}

			sourceHashes := make(map[string]string)
			for _, srcAbs := range sourceFiles {
				info, err := os.Lstat(srcAbs)
				if err != nil {
					return fmt.Errorf("hashing %s: %w", srcAbs, err)
				}
				if info.IsDir() {
					err := filepath.Walk(srcAbs, func(path string, fi os.FileInfo, err error) error {
						if err != nil {
							return err
						}
						if fi.IsDir() {
							return nil
						}
						rel, err := filepath.Rel(absContext, path)
						if err != nil {
							rel = path
						}
						h, err := cache.HashFile(path)
						if err != nil {
							return err
						}
						sourceHashes[rel] = h
						return nil
					})
					if err != nil {
						return fmt.Errorf("hashing dir %s: %w", srcAbs, err)
					}
					continue
				}
				rel, err := filepath.Rel(absContext, srcAbs)
				if err != nil {
					rel = srcAbs
				}
				h, err := cache.HashFile(srcAbs)
				if err != nil {
					return fmt.Errorf("hashing %s: %w", srcAbs, err)
				}
				sourceHashes[rel] = h
			}

			instrText := fmt.Sprintf("COPY %s %s", instr.CopySrc, instr.CopyDest)
			cacheKey := cache.ComputeKey(cache.CacheInput{
				PrevLayerDigest:  prevDigest,
				InstructionText:  instrText,
				WorkDir:          workDir,
				EnvState:         cloneEnv(envState),
				SourceFileHashes: sourceHashes,
			})

			var layerDigest string
			var layerSize int64
			var hitStr string

			if !noCache && !cacheCascade {
				digest, hit, err := cache.Lookup(cacheKey)
				if err != nil {
					return err
				}
				if hit {
					layerDigest = digest
					info, err := os.Stat(filepath.Join(image.LayersDir(), layerDigest))
					if err != nil {
						return err
					}
					layerSize = info.Size()
					hitStr = "[CACHE HIT]"
				}
			}

			if hitStr == "" {
				hitStr = "[CACHE MISS]"
				cacheCascade = true

				tarFiles, err := buildCopyTarMap(sourceFiles, absContext, instr.CopyDest, workDir)
				if err != nil {
					return fmt.Errorf("step %d COPY: %w", stepIdx, err)
				}

				digest, size, err := image.CreateLayerTar(tarFiles)
				if err != nil {
					return fmt.Errorf("creating layer: %w", err)
				}
				layerDigest = digest
				layerSize = size

				if !noCache {
					if err := cache.Store(cacheKey, layerDigest); err != nil {
						return err
					}
				}
			}

			elapsed := time.Since(stepStart)
			fmt.Printf("Step %d/%d : COPY %s %s %s %.2fs\n",
				stepIdx, totalSteps, instr.CopySrc, instr.CopyDest, hitStr, elapsed.Seconds())

			layers = append(layers, image.LayerEntry{
				Digest:    layerDigest,
				Size:      layerSize,
				CreatedBy: instrText,
			})
			prevDigest = layerDigest

		case parser.RUN:
			stepStart := time.Now()

			instrText := fmt.Sprintf("RUN %s", instr.RunCmd)
			cacheKey := cache.ComputeKey(cache.CacheInput{
				PrevLayerDigest: prevDigest,
				InstructionText: instrText,
				WorkDir:         workDir,
				EnvState:        cloneEnv(envState),
			})

			var layerDigest string
			var layerSize int64
			var hitStr string

			if !noCache && !cacheCascade {
				digest, hit, err := cache.Lookup(cacheKey)
				if err != nil {
					return err
				}
				if hit {
					layerDigest = digest
					info, err := os.Stat(filepath.Join(image.LayersDir(), layerDigest))
					if err != nil {
						return err
					}
					layerSize = info.Size()
					hitStr = "[CACHE HIT]"
				}
			}

			if hitStr == "" {
				hitStr = "[CACHE MISS]"
				cacheCascade = true

				digest, size, err := executeRun(layers, instr.RunCmd, envState, workDir)
				if err != nil {
					return fmt.Errorf("step %d RUN: %w", stepIdx, err)
				}
				layerDigest = digest
				layerSize = size

				if !noCache {
					if err := cache.Store(cacheKey, layerDigest); err != nil {
						return err
					}
				}
			}

			elapsed := time.Since(stepStart)
			fmt.Printf("Step %d/%d : RUN %s %s %.2fs\n",
				stepIdx, totalSteps, instr.RunCmd, hitStr, elapsed.Seconds())

			layers = append(layers, image.LayerEntry{
				Digest:    layerDigest,
				Size:      layerSize,
				CreatedBy: instrText,
			})
			prevDigest = layerDigest

		case parser.WORKDIR:
			fmt.Printf("Step %d/%d : WORKDIR %s\n", stepIdx, totalSteps, instr.WorkDir)
			workDir = instr.WorkDir

		case parser.ENV:
			fmt.Printf("Step %d/%d : ENV %s=%s\n", stepIdx, totalSteps, instr.EnvKey, instr.EnvValue)
			envState[instr.EnvKey] = instr.EnvValue

		case parser.CMD:
			fmt.Printf("Step %d/%d : CMD %v\n", stepIdx, totalSteps, instr.CmdArgs)
			cmdConfig = instr.CmdArgs
		}
	}

	cfg := image.Config{
		Env:        cache.NormalizeEnvMap(envState),
		Cmd:        cmdConfig,
		WorkingDir: workDir,
	}

	m := &image.Manifest{
		Name:   name,
		Tag:    tag,
		Config: cfg,
		Layers: layers,
	}

	var createdAt time.Time
	if !cacheCascade && !existingCreated.IsZero() {
		createdAt = existingCreated
	}

	totalElapsed := time.Since(buildStart)

	if err := m.Save(createdAt); err != nil {
		return fmt.Errorf("saving manifest: %w", err)
	}

	shortDigest := m.Digest
	if len(shortDigest) > 15 {
		shortDigest = shortDigest[:15]
	}
	fmt.Printf("Successfully built %s %s:%s (%.2fs)\n", shortDigest, name, tag, totalElapsed.Seconds())
	return nil
}

func executeRun(layers []image.LayerEntry, cmd string, envState map[string]string, workDir string) (string, int64, error) {
	baseDir, err := os.MkdirTemp("", "docksmith-base-*")
	if err != nil {
		return "", 0, err
	}
	defer os.RemoveAll(baseDir)

	if err := image.AssembleFilesystem(layers, baseDir); err != nil {
		return "", 0, err
	}

	runDir, err := os.MkdirTemp("", "docksmith-run-*")
	if err != nil {
		return "", 0, err
	}
	defer os.RemoveAll(runDir)

	if err := copyDir(baseDir, runDir); err != nil {
		return "", 0, fmt.Errorf("copying base filesystem: %w", err)
	}

	if workDir != "" {
		if err := os.MkdirAll(filepath.Join(runDir, workDir), 0755); err != nil {
			return "", 0, err
		}
	}

	env := envMapToSlice(envState)
	shellCmd := []string{"/bin/sh", "-c", cmd}
	if err := runtime.IsolateAndRun(runDir, shellCmd, env, workDir); err != nil {
		return "", 0, fmt.Errorf("command failed: %w", err)
	}

	deltaFiles, err := computeDelta(baseDir, runDir)
	if err != nil {
		return "", 0, err
	}

	digest, size, err := image.CreateLayerTar(deltaFiles)
	if err != nil {
		return "", 0, err
	}

	return digest, size, nil
}

func computeDelta(baseDir, newDir string) (map[string]string, error) {
	delta := make(map[string]string)

	err := filepath.Walk(newDir, func(newPath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(newDir, newPath)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(rel, "proc") || strings.HasPrefix(rel, ".pivot_old") {
			return nil
		}

		basePath := filepath.Join(baseDir, rel)
		baseInfo, baseErr := os.Lstat(basePath)

		if baseErr != nil {
			delta["/"+rel] = newPath
			return nil
		}

		if info.IsDir() && baseInfo.IsDir() {
			return nil
		}

		if info.IsDir() != baseInfo.IsDir() {
			delta["/"+rel] = newPath
			return nil
		}

		if !info.IsDir() {
			// Skip non-regular files (symlinks, devices, etc.)
			if !info.Mode().IsRegular() {
				return nil
			}
			if !baseInfo.Mode().IsRegular() {
				return nil
			}
			newHash, err := cache.HashFile(newPath)
			if err != nil {
				return err
			}
			baseHash, err := cache.HashFile(basePath)
			if err != nil {
				return err
			}
			if newHash != baseHash {
				delta["/"+rel] = newPath
			}
		}

		return nil
	})

	return delta, err
}

func buildCopyTarMap(sourceFiles []string, contextDir, dest, workDir string) (map[string]string, error) {
	tarFiles := make(map[string]string)

	if !filepath.IsAbs(dest) {
		if workDir != "" {
			dest = filepath.Join(workDir, dest)
		} else {
			dest = "/" + dest
		}
	}

	for _, srcAbs := range sourceFiles {
		info, err := os.Lstat(srcAbs)
		if err != nil {
			return nil, err
		}

		if info.IsDir() {
			err := filepath.Walk(srcAbs, func(path string, fi os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				entryRel, err := filepath.Rel(srcAbs, path)
				if err != nil {
					return err
				}
				if entryRel == "." {
					return nil
				}
				archivePath := filepath.ToSlash(filepath.Join(dest, entryRel))
				if !strings.HasPrefix(archivePath, "/") {
					archivePath = "/" + archivePath
				}
				tarFiles[archivePath] = path
				return nil
			})
			if err != nil {
				return nil, err
			}
		} else {
			rel, err := filepath.Rel(contextDir, srcAbs)
			if err != nil {
				return nil, err
			}

			var archivePath string
			if strings.HasSuffix(dest, "/") {
				archivePath = dest + filepath.Base(rel)
			} else {
				archivePath = filepath.Join(dest, filepath.Base(rel))
			}
			archivePath = filepath.ToSlash(archivePath)
			if !strings.HasPrefix(archivePath, "/") {
				archivePath = "/" + archivePath
			}
			tarFiles[archivePath] = srcAbs
		}
	}

	return tarFiles, nil
}

func expandGlobs(contextDir, pattern string) ([]string, error) {
	if pattern == "." {
		return []string{contextDir}, nil
	}

	absPattern := filepath.Join(contextDir, pattern)

	if strings.Contains(pattern, "**") {
		matches, err := doubleStarGlob(contextDir, pattern)
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("no files matched pattern: %s", pattern)
		}
		sort.Strings(matches)
		return matches, nil
	}

	matches, err := filepath.Glob(absPattern)
	if err != nil {
		return nil, err
	}

	if len(matches) == 0 {
		return nil, fmt.Errorf("no files matched pattern: %s", pattern)
	}

	sort.Strings(matches)
	return matches, nil
}

func doubleStarGlob(contextDir, pattern string) ([]string, error) {
	var matches []string
	err := filepath.Walk(contextDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(contextDir, path)
		if err != nil {
			return err
		}
		matched, _ := matchDoubleGlob(pattern, rel)
		if matched {
			matches = append(matches, path)
		}
		return nil
	})
	return matches, err
}

func matchDoubleGlob(pattern, name string) (bool, error) {
	parts := strings.Split(pattern, "**")
	if len(parts) == 1 {
		return filepath.Match(pattern, name)
	}
	for _, part := range parts {
		part = strings.TrimPrefix(strings.TrimSuffix(part, "/"), "/")
		if part != "" && !strings.Contains(name, part) {
			return false, nil
		}
	}
	return true, nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(rel, "proc") {
			return nil
		}

		destPath := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}

		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			// Ensure parent directory exists before creating symlink
			if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
				return err
			}
			os.Remove(destPath)
			return os.Symlink(linkTarget, destPath)
		}

		if d.IsDir() {
			return os.MkdirAll(destPath, info.Mode())
		}

		return copyFile(path, destPath, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if linkTarget, err := os.Readlink(src); err == nil {
		os.Remove(dst)
		return os.Symlink(linkTarget, dst)
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func cloneEnv(env map[string]string) map[string]string {
	c := make(map[string]string, len(env))
	for k, v := range env {
		c[k] = v
	}
	return c
}

func envMapToSlice(env map[string]string) []string {
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
