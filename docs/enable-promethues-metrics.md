# Enabling Prometheus for Instaslice Monitoring

## Overview
This document outlines the steps to enable Prometheus monitoring for Instaslice, including setting up Prometheus, configuring metrics collection, and verifying the setup. The following guide follows a **happy path** approach, ensuring a smooth deployment process. Controller failure fault tolerances and additional reliability mechanisms will be added in future work. **Unit tests have been added**, and **end-to-end (E2E) tests for metrics will be incorporated in future work.**

## Available Custom Instaslice Metrics
Instaslice exposes the following Prometheus metrics:

| Metric Name | Description |
|-------------|-------------|
| `instaslice_pod_processed_slices` | Tracks the number of pods that have been processed with their slice allocation. |
| `instaslice_compatible_profiles` | Displays the profiles compatible with the remaining GPU slices on a node and their counts. |
| `instaslice_total_processed_gpu_slices` | Counts the total processed GPU slices since the Instaslice controller started. |

## Steps to Deploy Prometheus

1. **Install Prometheus using Helm:**
   ```sh
   helm install prometheus -f deploy/prometheus.yaml prometheus-community/kube-prometheus-stack --namespace=instaslice-monitoring --create-namespace
   ```
2. **Apply Prometheus role and role binding:**
   ```sh
   kubectl apply -f deploy/prometheus-role.yaml
   kubectl apply -f deploy/prometheus-rolebinding.yaml
   ```
3. **Apply the Instaslice ServiceMonitor:**
   ```sh
   kubectl apply -f deploy/instaslice-servicemonitor.yaml
   ```

## Deploy Instaslice and Configure Metrics Service

1. **Apply the Instaslice Metrics Service YAML:**
   ```sh
   kubectl apply -f deploy/instaslice-metrics-service.yaml
   ```
2. **Check the readiness of Prometheus pods:**
   ```sh
   kubectl get pods -n instaslice-monitoring --watch
   ```
3. **Retrieve the token to access the service:**
   ```sh
   TOKEN=$(kubectl exec -it prometheus-prometheus-kube-prometheus-prometheus-0 -n instaslice-monitoring -- cat /var/run/secrets/kubernetes.io/serviceaccount/token)
   ```
4. **Forward the Instaslice Metrics Service port:**
   ```sh
   kubectl port-forward svc/instaslice-metrics 8443:8443 -n das-operator
   ```
5. **Verify the exposed metrics using curl:**
   ```sh
   curl -k -H "Authorization: Bearer $TOKEN" https://localhost:8443/metrics
   ```
6. **Forward the Prometheus UI port for monitoring:**
   ```sh
   kubectl port-forward svc/prometheus-kube-prometheus-prometheus -n instaslice-monitoring 9090:9090
   ```

## Future Work
- **Controller Failure Handling:** Additional fault tolerance mechanisms will be integrated to ensure resilience in case of failures.
- **E2E Testing:** While unit tests are already in place, future efforts will focus on adding **end-to-end (E2E) tests** for Instaslice metrics.

By following the above steps, Instaslice metrics can be successfully monitored using Prometheus. Future improvements will focus on increasing robustness and automated testing coverage.

