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
## Prerequisites

To run e2e tests in OpenShift Container Platform (OCP), the [OpenShift Cert Manager Operator](https://docs.openshift.com/container-platform/4.17/security/cert_manager_operator/cert-manager-operator-install.html) must be preinstalled.

### Note:
If you are running a pre-release or in-development version of OCP, you will need to use a catalog from an earlier stable release. For example:

```yaml
apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: redhat-operators-v417
  namespace: openshift-marketplace
spec:
  displayName: Red Hat Operators v4.17
  image: registry.redhat.io/redhat/redhat-operator-index:v4.17
  priority: -100
  publisher: Red Hat
  sourceType: grpc
  updateStrategy:
    registryPoll:
      interval: 10m
```
## Running the e2e
```console
IMG=<controller_image> IMG_DMST=<daemonset_image> make docker-build docker-push test-e2e-ocp-emulated
```
Executing the above command
- Builds the controller and daemonset docker images from the latest code
- Installs the `Instaslice` CRD
- Deploys the required rbac configs to run instaslice controller
- Deploys the instaslice controller
- Runs the e2e tests

### Cleanup
The following command cleans up the test setup
```console
make cleanup-test-e2e-ocp-emulated
```