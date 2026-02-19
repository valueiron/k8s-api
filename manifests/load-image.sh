#!/usr/bin/env bash
# load-image.sh — Export the k8s-api Docker image from the local daemon and
# import it into containerd on each Kubernetes node over SSH.
#
# Requires: docker, ssh, scp on the local machine.
#           sudo + containerd's ctr on each node (root NOT required).
#
# Usage:
#   ./load-image.sh [SSH_USER@]NODE1 [[SSH_USER@]NODE2 ...]
#
# Examples:
#   ./load-image.sh matt@172.22.150.249 matt@172.22.150.250 matt@172.22.150.251
#   ./load-image.sh ubuntu@worker1 ubuntu@worker2
#
# After loading, apply the manifests:
#   kubectl apply -f .           (plain kubectl)
#   kubectl apply -k .           (kustomize)
#
# Note: The image is copied via scp and then imported with `sudo ctr`.
# This avoids the stdin conflict that occurs when piping image data while
# sudo also needs stdin to read a password.

set -euo pipefail

IMAGE="${IMAGE:-k8s-api:latest}"
CTR_NAMESPACE="k8s.io"              # containerd namespace Kubernetes uses
REMOTE_TMP="/tmp/k8s-api-image.tar" # temp path on each node (cleaned up after)
LOCAL_TMP="$(mktemp /tmp/k8s-api-XXXXXX.tar)"

if [[ $# -eq 0 ]]; then
  echo "Usage: $0 [user@]NODE1 [[user@]NODE2 ...]" >&2
  exit 1
fi

cleanup() { rm -f "$LOCAL_TMP"; }
trap cleanup EXIT

echo "▸ Saving image '${IMAGE}'..."
docker save "$IMAGE" -o "$LOCAL_TMP"
echo "  Size: $(du -sh "$LOCAL_TMP" | cut -f1)"

for NODE in "$@"; do
  echo ""
  echo "▸ Copying to ${NODE}:${REMOTE_TMP}..."
  scp -q "$LOCAL_TMP" "${NODE}:${REMOTE_TMP}"

  echo "  Importing into containerd (k8s.io namespace)..."
  # sudo is required to access /run/containerd/containerd.sock.
  # Running from a file (not stdin) means sudo can prompt for a password
  # normally on the terminal if needed.
  ssh "$NODE" "sudo ctr -n '${CTR_NAMESPACE}' images import '${REMOTE_TMP}' && rm -f '${REMOTE_TMP}'"

  echo "  ✓ Done: ${NODE}"
done

echo ""
echo "All nodes loaded. You can now apply the manifests:"
echo "  kubectl apply -f $(dirname "$0")"
