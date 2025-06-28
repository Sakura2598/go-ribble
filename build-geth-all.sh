#!/bin/bash
set -e

# ==== CONFIG ====
VERSION="v1.0.0"
OUTPUT_DIR="builds/${VERSION}"
TARGETS=("geth" "abigen" "bootnode" "clef" "ethkey" "rlpdump" "devp2p")
PLATFORMS=("linux/amd64" "windows/amd64" "darwin/amd64")

# ==== GIT INFO ====
GIT_COMMIT=$(git rev-parse --short HEAD)
BUILD_DATE=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

# ==== ENSURE git safe.directory ====
REPO_PATH_UNIX="$(pwd)"
REPO_PATH_WSL="//wsl.localhost/Ubuntu${REPO_PATH_UNIX}"

add_safe_dir() {
  local dir="$1"
  if ! git config --global --get-all safe.directory | grep -Fxq "$dir"; then
    echo "🔒 Adding $dir to git safe.directory..."
    git config --global --add safe.directory "$dir"
  fi
}

add_safe_dir "$REPO_PATH_UNIX"
add_safe_dir "$REPO_PATH_WSL"

# ==== OUTPUT INFO ====
echo "🔧 Building Go Ethereum Tools"
echo "📦 Version: $VERSION"
echo "🔨 Commit:  $GIT_COMMIT"
echo "📅 Date:    $BUILD_DATE"
echo "📁 Output:  $OUTPUT_DIR"
echo

mkdir -p "$OUTPUT_DIR"

# ==== BUILD LOOP ====
for TARGET in "${TARGETS[@]}"; do
  for PLATFORM in "${PLATFORMS[@]}"; do
    IFS="/" read -r GOOS GOARCH <<< "$PLATFORM"
    EXT=""
    [[ "$GOOS" == "windows" ]] && EXT=".exe"

    echo "👉 Building $TARGET for $GOOS/$GOARCH..."

    # Set env and run build
    env \
      GOOS=$GOOS GOARCH=$GOARCH CGO_ENABLED=0 \
      GITCOMMIT=$GIT_COMMIT GITTAG=$VERSION BUILDDATE=$BUILD_DATE \
      make "$TARGET"

    # Move binary
    BIN_NAME="${TARGET}-${VERSION}-${GIT_COMMIT}-${GOOS}-${GOARCH}${EXT}"
    mv "build/bin/${TARGET}${EXT}" "${OUTPUT_DIR}/${BIN_NAME}"
  done
done

# ==== DONE ====
echo
echo "✅ All binaries built successfully!"
echo "📂 Output directory: ${OUTPUT_DIR}"
