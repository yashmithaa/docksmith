#!/usr/bin/env bash
# setup.sh - Import base images into the Docksmith local store.
# Run this ONCE before any builds. Requires internet access only during setup.
# After setup, all operations work fully offline.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DOCKSMITH_BIN="$SCRIPT_DIR/docksmith"

echo "=== Docksmith Setup ==="
echo "Building docksmith binary..."
cd "$SCRIPT_DIR"
go build -o docksmith .
echo "Binary built: $DOCKSMITH_BIN"

echo ""
echo "=== Importing Base Images ==="

# We import a minimal Python slim image.
# We download the Docker image tarball, then convert it to Docksmith format.

STATE_DIR="$HOME/.docksmith"
IMAGES_DIR="$STATE_DIR/images"
LAYERS_DIR="$STATE_DIR/layers"
CACHE_DIR="$STATE_DIR/cache"

mkdir -p "$IMAGES_DIR" "$LAYERS_DIR" "$CACHE_DIR"

WORK_DIR=$(mktemp -d)
trap 'rm -rf "$WORK_DIR"' EXIT

# Function to import a Docker image tarball into Docksmith
import_image() {
    local image_name="$1"
    local image_tag="$2"
    local docker_ref="$3"

    echo "Importing $image_name:$image_tag from Docker Hub..."

    local tar_path="$WORK_DIR/${image_name//\//_}-${image_tag}.tar"

    # Pull and save the Docker image tarball
    if command -v docker &>/dev/null; then
        docker pull "$docker_ref"
        docker save "$docker_ref" -o "$tar_path"
    elif command -v skopeo &>/dev/null; then
        skopeo copy "docker://$docker_ref" "docker-archive:$tar_path"
    else
        echo "ERROR: Neither 'docker' nor 'skopeo' found. Install one to import base images."
        echo "  Ubuntu/Debian: sudo apt-get install docker.io   OR   sudo apt-get install skopeo"
        exit 1
    fi

    echo "Converting $image_name:$image_tag to Docksmith format..."
    import_docker_tar "$tar_path" "$image_name" "$image_tag"
    echo "  -> Imported $image_name:$image_tag successfully."
}

# import_docker_tar converts a Docker save tarball into Docksmith's format.
import_docker_tar() {
    local tar_path="$1"
    local image_name="$2"
    local image_tag="$3"

    local extract_dir="$WORK_DIR/extract-${image_name//\//_}-${image_tag}"
    mkdir -p "$extract_dir"
    tar xf "$tar_path" -C "$extract_dir"

    # Find manifest.json (Docker image format)
    local manifest_json="$extract_dir/manifest.json"
    if [[ ! -f "$manifest_json" ]]; then
        echo "ERROR: manifest.json not found in Docker tarball"
        exit 1
    fi

    # Parse the manifest to find layers
    # Docker format: manifest.json contains array of {Config, RepoTags, Layers}
    local layers_json
    layers_json=$(python3 -c "
import json, sys
with open('$manifest_json') as f:
    m = json.load(f)
entry = m[0]
print(json.dumps(entry.get('Layers', [])))
")

    # Import each layer
    local all_layer_entries=""
    local prev_size
    while IFS= read -r layer_path; do
        layer_path=$(echo "$layer_path" | tr -d '"')
        [[ -z "$layer_path" ]] && continue

        local full_layer_path="$extract_dir/$layer_path"
        if [[ ! -f "$full_layer_path" ]]; then
            # Try without subdir (newer Docker format)
            full_layer_path="$extract_dir/$(basename "$(dirname "$layer_path")")/layer.tar"
        fi
        [[ ! -f "$full_layer_path" ]] && continue

        # Compute digest of layer tar
        local digest
        digest="sha256:$(sha256sum "$full_layer_path" | awk '{print $1}')"
        local dest_path="$LAYERS_DIR/$digest"

        if [[ ! -f "$dest_path" ]]; then
            cp "$full_layer_path" "$dest_path"
        fi

        local size
        size=$(stat -c%s "$dest_path")
        all_layer_entries="$all_layer_entries {\"digest\":\"$digest\",\"size\":$size,\"createdBy\":\"base layer\"},"
    done < <(python3 -c "
import json
with open('$manifest_json') as f:
    m = json.load(f)
for l in m[0].get('Layers', []):
    print(l)
")

    # Remove trailing comma
    all_layer_entries="${all_layer_entries%,}"

    # Build manifest JSON with digest="" first
    local created
    created=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

    local safe_name="${image_name//:/-}"
    local manifest_file="$IMAGES_DIR/${safe_name}-${image_tag}.json"

    # Write manifest with digest="" then compute real digest
    LAYERS_RAW="$all_layer_entries" python3 - <<PYEOF
import json, hashlib, os

layers_raw = os.environ.get('LAYERS_RAW', '')

# Parse layers from shell-built JSON
import re
entries = []
pattern = r'\{"digest":"(sha256:[a-f0-9]+)","size":(\d+),"createdBy":"([^"]+)"\}'
for m in re.finditer(pattern, layers_raw):
    entries.append({"digest": m.group(1), "size": int(m.group(2)), "createdBy": m.group(3)})

manifest = {
    "name": "$image_name",
    "tag": "$image_tag",
    "digest": "",
    "created": "$created",
    "config": {
        "Env": [],
        "Cmd": [],
        "WorkingDir": ""
    },
    "layers": entries
}

# Parse config if available
try:
    with open('$manifest_json') as f:
        docker_manifest = json.load(f)
    config_file = docker_manifest[0].get('Config', '')
    config_path = os.path.join('$extract_dir', config_file)
    if os.path.exists(config_path):
        with open(config_path) as f:
            cfg = json.load(f)
        container_cfg = cfg.get('config', cfg.get('Config', {}))
        env = container_cfg.get('Env', []) or []
        cmd = container_cfg.get('Cmd', []) or []
        workdir = container_cfg.get('WorkingDir', '') or ''
        manifest['config']['Env'] = env
        manifest['config']['Cmd'] = cmd
        manifest['config']['WorkingDir'] = workdir
except Exception as e:
    pass

# Compute digest
canonical = json.dumps(manifest, separators=(',', ':')).encode()
digest = 'sha256:' + hashlib.sha256(canonical).hexdigest()
manifest['digest'] = digest

with open('$manifest_file', 'w') as f:
    json.dump(manifest, f, indent=2)

print(f"  Manifest written: {digest[:19]}...")
PYEOF
}

# Import the python:slim base image
import_image "python" "slim" "python:slim"

echo ""
echo "=== Setup Complete ==="
echo "Base images imported. You can now build offline:"
echo ""
echo "  cd $SCRIPT_DIR/sample-app"
echo "  $DOCKSMITH_BIN build -t myapp:latest ."
echo "  $DOCKSMITH_BIN images"
echo "  $DOCKSMITH_BIN run myapp:latest"
echo "  $DOCKSMITH_BIN run -e GREETING=Hi myapp:latest"
