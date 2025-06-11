#!/usr/bin/env bash

# run-locally.sh is some scaffolding around running a local instance of the
# network operator with the installer.
# See HACKING.md

set -o errexit
set -o nounset

if [[ -n "${HACK_MINIMIZE:-}" ]]; then
	echo "HACK_MINIMIZE set! This is only for development, and the installer will likely not succeed."
	echo "You will still be left with a reasonably functional cluster."
	echo ""
fi

function run_vs_existing_cluster {
	echo "Attaching to already running cluster..."
	wait_for_cluster
}

function setup_operator_env() {
	# library-go controller needs to write stuff here
	if [[ ! -w "/var/run/secrets" ]]; then
		echo "Need /var/run/secrets to be writable, please execute"
		echo sudo /usr/bin/env bash -c "'mkdir -p /var/run/secrets && chown ${USER} /var/run/secrets'"
	fi

	mkdir -p /var/run/secrets/kubernetes.io/serviceaccount/
	echo -n "das-operator" >/var/run/secrets/kubernetes.io/serviceaccount/namespace
}

function print_usage() {
	>&2 echo "Usage: $0 [options]

$0 runs against an existing cluster using the specific '-k' option.

The following environment variables are honored:
 - KUBECONFIG: the kubeconfig pointing to the existing cluster 
"
}

EXPORT_ENV_ONLY=false
WAIT_FOR_MANIFEST_UPDATES="${WAIT_FOR_MANIFEST_UPDATES:-}"
KUBECONFIG="${KUBECONFIG:-}"

while getopts "e?m:k:n:w" opt; do
	case $opt in
	e) EXPORT_ENV_ONLY=true ;;
	k) KUBECONFIG="${OPTARG}" ;;
	w)
		WAIT_FOR_MANIFEST_UPDATES=1
		;;
	*)
		print_usage
		exit 2
		;;
	esac
done

if [[ -z "$(which kubectl 2>/dev/null || exit 0)" ]]; then
	echo "error: could not find 'kubectl' in PATH" >&2
	exit 1
fi

setup_operator_env

./instaslice-operator operator --kubeconfig "${KUBECONFIG}"
