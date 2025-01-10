# Specific instructions for Downstream builds

## How to manually build custom images for OpenShift

1. Set the image urls for your test repos, i.e
```console
$ export IMG=quay.io/<username>/instaslice-operator:test
$ export IMG_DMST=quay.io/<username>/instaslice-daemonset:test
$ export BUNDLE_IMG=quay.io/<username>/instaslice-bundle:test
```
1. Build operator and daemonset using OpenShift specifig containerfiles
```console
$ make container-build-ocp
```
1. Push images to you test repo
```console
$ make docker-push
```
1. Build bundle using OpenShift containerfile
```console
$ make bundle-build-ocp
```
1. Push bundle to test repo
``` console
$ make bundle-push
```
## Test the images on an OpenShift cluster using emulator mode

### Prerequisites
- You have a cluster available
- You have the RH Cert Manager operator installed through OperatorHub
- You have [operator-sdk](https://sdk.operatorframework.io/docs/installation/) installed 

1. Deploy test bundle
```console
$ oc new-project instaslice-system
$ operator-sdk run bundle $BUNDLE_IMAGE -n instaslice-system (FOR OCP 4.18: --security-context-config restricted)
```
1. Enable the emulator
Set EMULATOR_MODE=true in CSV

1. Create instaslice CR for fake resources
Edit test/e2e/resources/instaslice-fake-capacity.yaml to set metadata.name to the node the controller is running on
```console
$ oc apply -f test/e2e/resources/instaslice-fake-capacity.yaml
```
1. Label the node that was used in the name of the CR
```console
$ oc label node/<node_name> "nvidia.com/mig.capable"="true"
```
1. Execute e2e tests
```console
$ ginkgo -v --json-report=report.json --junit-report=report.xml --timeout 20m ./test/e2e
```

# Notes

Most of this is automated in make test-e2e-ocp-emulated but with upstream images.

Konflux CI with build the images to catch errors.  In the future, Konflux integration tests will test images for PRs as a set to verify images urls are correct.

If something changes with how the operator is built, Dockerfile.ocp may also needs to be updated.

Modify Dockerfile.ocp

If something changes that affects the bundle, the ocp bundle needs to be regenerated.

