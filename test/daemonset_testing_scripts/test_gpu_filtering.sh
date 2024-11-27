#!/bin/bash

if [ "$#" -ne 2 ]; then
    echo "Usage: $0 <IMG> <IMG_DMST>"
    echo "Example: $0 quay.io/amalvank/instaslice-ctrl-dev quay.io/amalvank/instaslice-dmst-dev"
    exit 1
fi

IMG=$1
IMG_DMST=$2

# Assume all GPUs on a single node are MIG enabled
echo "Disabling MIG mode on GPU with ID 1..."
sudo nvidia-smi -i 1 -mig 0

echo "Deploying cluster..."
bash deploy/setup.sh

echo "Deploying instaslice..."
IMG=$IMG IMG_DMST=$IMG_DMST make deploy


echo "Waiting for all pods in the 'instaslice-system' namespace to be running..."
while true; do
    POD_STATUS=$(kubectl get pods -n instaslice-system --no-headers | awk '{print $3}' | grep -v Running | wc -w)
    if [ "$POD_STATUS" -eq 0 ]; then
        echo "All pods are running in the 'instaslice-system' namespace."
        break
    fi
    echo "Waiting for pods to be running..."
    sleep 5
done

echo "Checking entries in instaslice.spec.MigGPUUUID..."
MIG_UUID_COUNT=$(kubectl get instaslice -n instaslice-system -o jsonpath='{.items[*].spec.MigGPUUUID.*}' | wc -w)

if [ "$MIG_UUID_COUNT" -eq 1 ]; then
    echo "Validation successful: instaslice.spec.MIGUUID is equal to 1."
else
    echo "Validation failed: instaslice.spec.MIGUUID is not equal to 1. Count: $MIG_UUID_COUNT"
    exit 1
fi

echo "Script completed successfully."
