# E2E tests using emulator mode in InstaSlice

E2E tests for the instaslice operator can be run on the KIND cluster as well as an Openshift cluster leveraging the emulator mechanism.
These tests help developers to verify and identify any regressions in the code while developing new features

## Steps to run e2e tests
E2E tests on a specific cluster can be run using the respective `make` commands i.e. targets defined in the `Makefile`
### KIND cluster

```console
make test-e2e-kind-emulated
```
Executing the above command
- Builds the controller and daemonset docker images from the latest code 
- Creates a KIND cluster
- Deploys the cert-manager and the related objects
- Installs the `Instaslice` CRD
- Deploys the instaslice controller
- Runs the e2e tests

### Cleanup
The following command destroys the kind cluster created for the execution of e2e tests and cleans up the test setup
```console
make cleanup-test-e2e-kind-emulated
```

### OCP Cluster
```console
make test-e2e-ocp-emulated
```
Executing the above command
- Builds the controller and daemonset docker images from the latest code
- Deploys the cert-manager and the related objects
- Installs the `Instaslice` CRD
- Deploys the required rbac configs to run instaslice controller 
- Deploys the instaslice controller
- Runs the e2e tests

> **Note:** As of today, by default, executing the e2e tests refer to `quay.io/amalvank/instaslicev2-controller:latest` and `quay.io/amalvank/instaslicev2-daemonset:latest` So, make sure to build and push the controller and the daemonset docker images to a **public** repository to run the e2e tests against the latest code.

For Example, following are the steps to build and push the docker images from a development branch before executing the e2e tests on an OCP cluster
```console
export IMG=quay.io/svanka/instaslicev2-controller:latest IMG_DMST=quay.io/svanka/instaslicev2-daemonset:latest
make docker-build
# Assuming the above tag refers to a public repository in quay.io, pushing the docker images
make docker-push
```
Execute the e2e tests referring to the recently pushed docker images
```console
export IMG=quay.io/svanka/instaslicev2-controller:latest IMG_DMST=quay.io/svanka/instaslicev2-daemonset:latest
make test-e2e-ocp-emulated
```
### Cleanup
The following command cleans up the test setup
```console
make cleanup-test-e2e-ocp-emulated
```