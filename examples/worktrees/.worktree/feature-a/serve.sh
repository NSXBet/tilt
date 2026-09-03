#!/bin/bash
# Kept in the checkout because worktree runs resolve ./serve.sh here.
set -euo pipefail
exec "$(dirname "$0")/../../serve.sh" "$@"
