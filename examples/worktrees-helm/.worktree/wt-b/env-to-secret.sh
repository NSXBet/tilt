#!/usr/bin/env bash
# Emits one Secret manifest from .env. Called by the Tiltfile with
# k8s_yaml(local(['bash', 'env-to-secret.sh']), scope='main'), so the Secret
# is instantiated once (main run) and shared by every worktree clone.
set -euo pipefail

env_file="$(dirname "$0")/.env"

echo "apiVersion: v1"
echo "kind: Secret"
echo "metadata:"
echo "  name: wt-helm-env"
echo "type: Opaque"
echo "data:"
while IFS='=' read -r key value; do
  case "$key" in ''|\#*) continue ;; esac
  b64="$(printf %s "$value" | base64)"
  printf '  %s: %s\n' "$key" "$b64"
done < "$env_file"
