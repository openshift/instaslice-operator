#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

INSTASLICE_ROOT=$(dirname "${BASH_SOURCE}")/..
cd "${INSTASLICE_ROOT}"

find_files() {
  find . -not \( \
    \( \
    -wholename './output' \
    -o -wholename './_output' \
    -o -wholename './release' \
    -o -wholename './target' \
    -o -wholename './.git' \
    -o -wholename '*/third_party/*' \
    -o -wholename '*/Godeps/*' \
    -o -wholename '*/vendor/*' \
    \) -prune \
    \) -name '*.go'
}

GOFMT="gofmt -s"
bad_files=$(find_files | xargs $GOFMT -l)
if [[ -n "${bad_files}" ]]; then
  echo "!!! '$GOFMT' needs to be run on the following files: "
  echo "${bad_files}"
  exit 1
fi
