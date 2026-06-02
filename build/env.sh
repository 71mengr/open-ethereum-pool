#!/bin/sh

set -e

if [ ! -f "build/env.sh" ]; then
    echo "$0 must be run from the root of the repository."
    exit 2
fi

# Create fake Go workspace if it doesn't exist yet.
workspace="$PWD/build/_workspace"
root="$PWD"
ethdir="$workspace/src/github.com/sammy007"
if [ ! -L "$ethdir/open-ethereum-pool" ]; then
    mkdir -p "$ethdir"
    cd "$ethdir"
    ln -s ../../../../../. open-ethereum-pool
    cd "$root"
fi

# Set up the environment to use the workspace.
# Also add Godeps workspace so we build using canned dependencies.
GOPATH="$workspace"
GOBIN="$PWD/build/bin"
export GOPATH GOBIN

# RandomX support - preserve CGO flags if set
if [ -n "$CGO_ENABLED" ]; then
    export CGO_ENABLED
fi
if [ -n "$CGO_CFLAGS" ]; then
    export CGO_CFLAGS
fi
if [ -n "$CGO_LDFLAGS" ]; then
    export CGO_LDFLAGS
fi

# Run the command inside the workspace.
cd "$ethdir/open-ethereum-pool"
PWD="$ethdir/open-ethereum-pool"

# Launch the arguments with the configured environment.
exec "$@"
