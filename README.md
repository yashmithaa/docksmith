# Docksmith

A simplified Docker-like build and runtime system built from scratch in Go.

## What It Does

Docksmith implements:
1. **Build system** — reads a `Docksmithfile`, executes six instructions (`FROM`, `COPY`, `RUN`, `WORKDIR`, `ENV`, `CMD`), stores immutable delta layers as content-addressed tar files under `~/.docksmith/layers/`, and writes a JSON manifest to `~/.docksmith/images/`.
2. **Deterministic build cache** — every `COPY` and `RUN` step gets a cache key derived from: previous layer digest, instruction text, current `WORKDIR`, current `ENV` state, and (for `COPY`) source file hashes. A hit prints `[CACHE HIT]`; a miss prints `[CACHE MISS]` and cascades all downstream steps.
3. **Container runtime** — assembles the image filesystem by extracting layer tars in order, then isolates a process using Linux PID, mount, UTS, and IPC namespaces + `pivot_root`. The same isolation primitive is used for `RUN` during build and `docksmith run`.

## Requirements

- **Linux** (kernel ≥ 3.8, for namespace support). On macOS/Windows use WSL2 or a Linux VM.
- **Go 1.21+** to build.
- **`docker` or `skopeo`** — only needed once during initial setup to download base images.
- **Root privileges for `build` and `run`** — Docksmith uses Linux mount namespaces and `pivot_root`, so the container steps need to run with elevated privileges. On Arch, use `sudo env HOME=$HOME ...` so Docksmith still reads and writes `~/.docksmith` under your user account.
- No Docker/runc/containerd needed at runtime.

## Directory Layout

```
~/.docksmith/
  images/    # one JSON manifest per image
  layers/    # content-addressed tar files (sha256:... filenames)
  cache/     # index.json mapping cache keys -> layer digests
```

## Quick Start

### 1. Build the binary

```bash
go build -o docksmith .
```

### 2. Import base images (internet required once)

```bash
chmod +x setup.sh import-base.sh
./import-base.sh          # uses curl to pull python:slim from Docker Hub
# OR if you have Docker installed:
./setup.sh                # uses docker pull
```

### 3. Build the sample app

```bash
cd sample-app
sudo env HOME=$HOME ../docksmith build -t myapp:latest .
```

### 4. Run the demo sequence

```bash
# Cold build — all CACHE MISS
sudo env HOME=$HOME ../docksmith build -t myapp:latest .

# Warm build — all CACHE HIT (near-instant)
sudo env HOME=$HOME ../docksmith build -t myapp:latest .

# Edit a source file, rebuild — partial cache
echo "# changed" >> src/main.py
sudo env HOME=$HOME ../docksmith build -t myapp:latest .

# List images
../docksmith images

# Run the container
sudo env HOME=$HOME ../docksmith run myapp:latest

# Override an env var
sudo env HOME=$HOME ../docksmith run -e GREETING=Bonjour myapp:latest

# Write a file inside a container — verify it doesn't appear on host
sudo env HOME=$HOME ../docksmith run myapp:latest sh -c "echo secret > /tmp/hostleak.txt"
ls /tmp/hostleak.txt   # should NOT exist

# Remove the image
../docksmith rmi myapp:latest
```

## CLI Reference

| Command | Description |
|---|---|
| `docksmith build -t <name:tag> [--no-cache] <context>` | Build image from Docksmithfile in context dir |
| `docksmith images` | List all images (Name, Tag, ID, Created) |
| `docksmith rmi <name:tag>` | Remove image manifest and all its layers |
| `docksmith run [-e KEY=VALUE] <name:tag> [cmd...]` | Run container (blocks until exit) |

## Docksmithfile Syntax

```dockerfile
FROM python:slim

WORKDIR /app

ENV APP_NAME=MyApp
ENV VERSION=1.0

COPY src /app

RUN echo "build done" && ls /app

CMD ["python", "main.py"]
```

Only these six instructions are supported. Any unrecognised instruction fails with a clear error and line number.

## Cache Key Specification

For each `COPY` or `RUN` instruction, the cache key is the SHA-256 of:

```
<prev_layer_digest>\n
<instruction_text>\n
<workdir>\n
KEY1=val1\n     # all ENV vars, sorted by key
KEY2=val2\n
rel/path:filehash\n   # COPY only: source files sorted by relative path
```

- "Previous layer" = digest of last `COPY` or `RUN`; for the first layer-producing step, it is the base image's manifest digest.
- A miss cascades: once any step misses, all subsequent steps are also misses.
- `--no-cache` skips all lookups and writes (layers are still stored normally).

## Image Format

Every image is a JSON manifest at `~/.docksmith/images/<name>-<tag>.json`:

```json
{
  "name": "myapp",
  "tag": "latest",
  "digest": "sha256:<hash>",
  "created": "2024-01-15T10:30:00Z",
  "config": {
    "Env": ["APP_NAME=MyApp", "VERSION=1.0"],
    "Cmd": ["python", "main.py"],
    "WorkingDir": "/app"
  },
  "layers": [
    { "digest": "sha256:aaa...", "size": 2048, "createdBy": "base layer" },
    { "digest": "sha256:bbb...", "size": 4096, "createdBy": "COPY src /app" },
    { "digest": "sha256:ccc...", "size": 8192, "createdBy": "RUN echo ..." }
  ]
}
```

The manifest `digest` is computed as: `sha256(json_with_digest_field_empty_string)`.

## Isolation Mechanism

Docksmith uses Linux namespaces via `clone(2)` flags:
- `CLONE_NEWPID` — new PID namespace (container process is PID 1)
- `CLONE_NEWNS`  — new mount namespace
- `CLONE_NEWUTS` — new UTS namespace (hostname set to "docksmith")
- `CLONE_NEWIPC` — new IPC namespace

After forking into the new namespaces, the child:
1. Bind-mounts the assembled rootfs onto itself
2. Calls `pivot_root(2)` to make it the new `/`
3. Unmounts the old root
4. `exec`s the target command

The same code path (`runtime.IsolateAndRun`) is used for both `RUN` during build and `docksmith run`.



