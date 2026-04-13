#!/usr/bin/env bash
# import-base.sh - Low-level base image importer using only curl + standard tools.
# Downloads OCI/Docker registry blobs and creates Docksmith manifests.
# Supports alpine:3.18 and python:slim without requiring docker/skopeo.

set -euo pipefail

STATE_DIR="$HOME/.docksmith"
IMAGES_DIR="$STATE_DIR/images"
LAYERS_DIR="$STATE_DIR/layers"
CACHE_DIR="$STATE_DIR/cache"
mkdir -p "$IMAGES_DIR" "$LAYERS_DIR" "$CACHE_DIR"

WORK_DIR=$(mktemp -d)
trap 'rm -rf "$WORK_DIR"' EXIT

# Pull a Docker Hub image using the v2 registry API
pull_dockerhub_image() {
    local repo="$1"   # e.g. "library/python" or "library/alpine"
    local ref="$2"    # e.g. "slim" or "3.18"
    local docksmith_name="$3"  # e.g. "python"
    local docksmith_tag="$4"   # e.g. "slim"

    echo "Pulling $docksmith_name:$docksmith_tag from Docker Hub..."

    # Step 1: Get auth token
    local token
    token=$(curl -fsSL "https://auth.docker.io/token?service=registry.docker.io&scope=repository:${repo}:pull" \
        | python3 -c "import json,sys; print(json.load(sys.stdin)['token'])")

    # Step 2: Get manifest (prefer amd64 linux)
    local manifest_url="https://registry-1.docker.io/v2/${repo}/manifests/${ref}"
    local manifest_response
    manifest_response=$(curl -fsSL \
        -H "Authorization: Bearer $token" \
        -H "Accept: application/vnd.docker.distribution.manifest.v2+json,application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.oci.image.index.v1+json" \
        "$manifest_url")

    # Handle manifest list (multi-arch) by selecting amd64
    local media_type
    media_type=$(echo "$manifest_response" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('mediaType','') or d.get('schemaVersion',''))" 2>/dev/null || echo "")

    if echo "$manifest_response" | python3 -c "import json,sys; d=json.load(sys.stdin); sys.exit(0 if 'manifests' in d else 1)" 2>/dev/null; then
        # It's a manifest list — pick amd64 linux
        local image_digest
        image_digest=$(echo "$manifest_response" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for m in d['manifests']:
    p = m.get('platform', {})
    if p.get('architecture') == 'amd64' and p.get('os') == 'linux':
        print(m['digest'])
        break
")
        manifest_response=$(curl -fsSL \
            -H "Authorization: Bearer $token" \
            -H "Accept: application/vnd.docker.distribution.manifest.v2+json,application/vnd.oci.image.manifest.v1+json" \
            "https://registry-1.docker.io/v2/${repo}/manifests/${image_digest}")
    fi

    # Step 3: Parse layers
    local config_digest
    config_digest=$(echo "$manifest_response" | python3 -c "import json,sys; print(json.load(sys.stdin)['config']['digest'])")

    # Pull config blob
    local config_json
    config_json=$(curl -fsSL \
        -H "Authorization: Bearer $token" \
        "https://registry-1.docker.io/v2/${repo}/blobs/${config_digest}")

    # Step 4: Download each layer blob
    local layer_entries=""
    while IFS= read -r layer_info; do
        local layer_digest layer_size
        layer_digest=$(echo "$layer_info" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d['digest'])")
        layer_size=$(echo "$layer_info" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d['size'])")

        echo "  Downloading layer ${layer_digest:7:12}... (${layer_size} bytes)"

        local dest_path="$LAYERS_DIR/$layer_digest"
        if [[ -f "$dest_path" ]]; then
            echo "  Layer already present, skipping."
        else
            curl -fsSL \
                -H "Authorization: Bearer $token" \
                "https://registry-1.docker.io/v2/${repo}/blobs/${layer_digest}" \
                -o "$dest_path"
        fi

        # Verify sha256
        local actual_digest
        actual_digest="sha256:$(sha256sum "$dest_path" | awk '{print $1}')"
        if [[ "$actual_digest" != "$layer_digest" ]]; then
            echo "  WARNING: digest mismatch for layer $layer_digest"
            echo "    expected: $layer_digest"
            echo "    got:      $actual_digest"
        fi

        local on_disk_size
        on_disk_size=$(stat -c%s "$dest_path")
        layer_entries="$layer_entries{\"digest\":\"$layer_digest\",\"size\":$on_disk_size,\"createdBy\":\"base layer\"},"
    done < <(echo "$manifest_response" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for l in d.get('layers', d.get('fsLayers', [])):
    print(json.dumps(l))
")

    layer_entries="${layer_entries%,}"

    # Step 5: Build Docksmith manifest
    local created
    created=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

    local safe_name="${docksmith_name//:/-}"
    local manifest_file="$IMAGES_DIR/${safe_name}-${docksmith_tag}.json"

    python3 - <<PYEOF
import json, hashlib

config = json.loads(r"""$config_json""")
container_cfg = config.get('config', config.get('Config', {}))

env = container_cfg.get('Env', []) or []
cmd = container_cfg.get('Cmd', []) or []
workdir = container_cfg.get('WorkingDir', '') or ''

layers_str = r"""$layer_entries"""
import re
entries = []
# Parse the shell-concatenated JSON objects
pattern = r'\{"digest":"(sha256:[a-f0-9]+)","size":(\d+),"createdBy":"([^"]+)"\}'
for m in re.finditer(pattern, layers_str):
    entries.append({
        "digest": m.group(1),
        "size": int(m.group(2)),
        "createdBy": m.group(3)
    })

manifest = {
    "name": "$docksmith_name",
    "tag": "$docksmith_tag",
    "digest": "",
    "created": "$created",
    "config": {
        "Env": env,
        "Cmd": cmd,
        "WorkingDir": workdir
    },
    "layers": entries
}

canonical = json.dumps(manifest, separators=(',', ':')).encode()
digest = 'sha256:' + hashlib.sha256(canonical).hexdigest()
manifest['digest'] = digest

with open('$manifest_file', 'w') as f:
    json.dump(manifest, f, indent=2)
print(f"Imported $docksmith_name:$docksmith_tag -> {digest[:19]}...")
PYEOF
}

echo "=== Docksmith Base Image Import ==="
echo "Downloading base images (internet required — one time only)..."
echo ""

# Import python:slim (Debian-based, has python3 built in)
pull_dockerhub_image "library/python" "slim" "python" "slim"

echo ""
echo "=== Import complete! ==="
echo "Base images are now in $IMAGES_DIR"
echo "All subsequent builds and runs work offline."
