# Emulator Mode in InstaSlice for OpenShift 4.18

Finding MIGable GPUs with cloud providers is expensive and challenging, especially for development activities. InstaSlice's emulator mode allows users to create hypothetical GPU capacity in the cluster by setting GPU profiles in the InstaSlice CR. This showcases InstaSlice's unique placement capabilities and enables development without needing physical GPUs in the cluster.

## Steps to Use Emulator Mode in OpenShift 4.18

We use Kustomize to enable emulator mode.

### 1. Ensure the Openshift Cert Manager Operator is Installed

Since OpenShift 4.18 does not yet have the Openshift Cert Manager Operator available in its operator catalog, we need to add the 4.17 catalog manually and install that Operator from there.

#### Add OpenShift 4.17 Catalog

1. **Create a namespace for the Cert Manager Operator**:

   ```bash
   oc create namespace cert-manager-operator
   ```

2. **Add the 4.17 catalog source**:

   ```yaml
   apiVersion: operators.coreos.com/v1alpha1
   kind: CatalogSource
   metadata:
     name: rh-operators-417
     namespace: openshift-marketplace
   spec:
     sourceType: grpc
     image: registry.redhat.io/redhat/redhat-operator-index:v4.17
     displayName: Red Hat 4.17 Catalog
     publisher: Red Hat
     updateStrategy:
       registryPoll:
         interval: 1m
   ```

   Apply this YAML file:

   ```bash
   oc apply -f <path_to_yaml_file>
   ```

3. **Create a subscription for the Cert Manager Operator**:

   ```yaml
   apiVersion: operators.coreos.com/v1
   kind: OperatorGroup
   metadata:
     name: openshift-cert-manager-operator
     namespace: cert-manager-operator
   spec:
     targetNamespaces:
     - "cert-manager-operator"
   ---
   apiVersion: operators.coreos.com/v1alpha1
   kind: Subscription
   metadata:
     name: openshift-cert-manager-operator
     namespace: cert-manager-operator
   spec:
     channel: stable-v1
     name: openshift-cert-manager-operator
     source: rh-operators-417
     sourceNamespace: openshift-marketplace
     installPlanApproval: Automatic
     startingCSV: cert-manager-operator.v1.14.0
   ```

   Apply this YAML file:

   ```bash
   oc apply -f <path_to_yaml_file>
   ```

4. **Verify that the Cert Manager Operator is working**:

   You should see similar outputs:

   ```bash
   $ oc get pods -n cert-manager-operator
   NAME                                                        READY   STATUS    RESTARTS   AGE
   cert-manager-operator-controller-manager-77746bc67c-skl7k   2/2     Running   0          63m
   ```

   ```bash
   $ oc get pods -n cert-manager
   NAME                                       READY   STATUS    RESTARTS   AGE
   cert-manager-5d59ff6b86-67s2l              1/1     Running   0          62m
   cert-manager-cainjector-6c54b88c7d-dkkbr   1/1     Running   0          63m
   ```

### 2. Deploy InstaSlice in Emulator Mode

To deploy the InstaSlice operator in emulated mode on OpenShift:

1. **Deploy the operator in emulator mode**:

   Use the following command to deploy the operator:

   ```bash
   make ocp-deploy-emulated
   ```

2. **Add GPU capacity to the cluster**:

   Apply the fake GPU capacity configuration:

   ```bash
   oc apply -f test/e2e/resources/instaslice-fake-capacity.yaml
   ```

3. **Verify that the InstaSlice object exists**:

   ```bash
   oc describe instaslice
   ```

4. **Submit an emulator pod**:

   Use the following command to submit an emulator pod:

   ```bash
   oc apply -f samples/emulator-pod.yaml
   ```

### 3. Check Allocation and Prepared Sections of the InstaSlice Object

- Allocation Section shows the placement decisions made by InstaSlice using the `firstfit` algorithm.
- Prepared Section is a mock, as no real GPU exists. CI and GI for any pod submissions are always 0.

### 4. Undeploy the Operator

To undeploy the operator:

```bash
make ocp-undeploy-emulated
```


