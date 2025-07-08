#!/bin/bash

# sanitize-branch.sh
# Sanitizes a branch name to be used as a Docker container tag
# Usage: ./hack/sanitize-branch.sh "branch-name"
# or: echo "branch-name" | ./hack/sanitize-branch.sh

set -euo pipefail

# Get input from argument or stdin
if [ $# -eq 1 ]; then
    BRANCH_NAME="$1"
elif [ $# -eq 0 ]; then
    # Read from stdin if no arguments provided
    read -r BRANCH_NAME
else
    echo "Usage: $0 [branch-name]" >&2
    echo "  or: echo 'branch-name' | $0" >&2
    exit 1
fi

# Sanitize branch name for Docker tag compatibility:
# 1. Replace any character that's not alphanumeric, dot, underscore, or hyphen with hyphen
# 2. Convert to lowercase
# 3. Remove leading/trailing hyphens and dots (Docker tags can't start/end with these)
# 4. Replace consecutive hyphens with single hyphen
# 5. Truncate to 128 characters max (Docker tag limit)

SANITIZED=$(echo "$BRANCH_NAME" | \
    sed 's/[^a-zA-Z0-9._-]/-/g' | \
    tr '[:upper:]' '[:lower:]' | \
    sed 's/^[-.]*//' | \
    sed 's/[-]*$//' | \
    sed 's/--*/-/g' | \
    cut -c1-128)

# If the result is empty or starts with invalid characters, default to "main"
if [[ -z "$SANITIZED" ]] || [[ "$SANITIZED" =~ ^[.-] ]]; then
    SANITIZED="main"
fi

echo "$SANITIZED" 