FROM registry.redhat.io/ubi9/ubi-minimal:9.6 as builder
ARG RELATED_IMAGE_FILE=related_images.json
ARG CSV_FILE=bundle-ocp/manifests/das-operator.clusterserviceversion.yaml
ARG OPERATOR_IMAGE_ORIGINAL=quay.io/redhat-user-workloads/dynamicacceleratorsl-tenant/das-operator-next:latest
ARG WEBHOOK_IMAGE_ORIGINAL=quay.io/redhat-user-workloads/dynamicacceleratorsl-tenant/das-webhook-next:latest
ARG SCHEDULER_IMAGE_ORIGINAL=quay.io/redhat-user-workloads/dynamicacceleratorsl-tenant/das-scheduler-next:latest
ARG DAEMONSET_IMAGE_ORIGINAL=quay.io/redhat-user-workloads/dynamicacceleratorsl-tenant/das-daemonset-next:latest

RUN microdnf install -y jq

COPY ${CSV_FILE} /manifests/das-operator.clusterserviceversion.yaml
COPY ${RELATED_IMAGE_FILE} /${RELATED_IMAGE_FILE}

RUN OPERATOR_IMAGE=$(jq -r '.[] | select(.name == "das-operator-next") | .image' /${RELATED_IMAGE_FILE}) && sed -i "s|${OPERATOR_IMAGE_ORIGINAL}|${OPERATOR_IMAGE}|g" /manifests/das-operator.clusterserviceversion.yaml
RUN WEBHOOK_IMAGE=$(jq -r '.[] | select(.name == "das-webhook-next") | .image' /${RELATED_IMAGE_FILE}) && sed -i "s|${WEBHOOK_IMAGE_ORIGINAL}|${WEBHOOK_IMAGE}|g" /manifests/das-operator.clusterserviceversion.yaml
RUN SCHEDULER_IMAGE=$(jq -r '.[] | select(.name == "das-scheduler-next") | .image' /${RELATED_IMAGE_FILE}) && sed -i "s|${SCHEDULER_IMAGE_ORIGINAL}|${SCHEDULER_IMAGE}|g" /manifests/das-operator.clusterserviceversion.yaml
RUN DAEMONSET_IMAGE=$(jq -r '.[] | select(.name == "das-daemonset-next") | .image' /${RELATED_IMAGE_FILE}) && sed -i "s|${DAEMONSET_IMAGE_ORIGINAL}|${DAEMONSET_IMAGE}|g" /manifests/das-operator.clusterserviceversion.yaml

RUN cat /manifests/das-operator.clusterserviceversion.yaml

FROM scratch

# Core bundle labels.
LABEL operators.operatorframework.io.bundle.mediatype.v1=registry+v1
LABEL operators.operatorframework.io.bundle.manifests.v1=manifests/
LABEL operators.operatorframework.io.bundle.metadata.v1=metadata/
LABEL operators.operatorframework.io.bundle.package.v1=das-operator
LABEL operators.operatorframework.io.bundle.channels.v1=alpha
LABEL operators.operatorframework.io.metrics.builder=operator-sdk-v1.37.0
LABEL operators.operatorframework.io.metrics.mediatype.v1=metrics+v1
LABEL operators.operatorframework.io.metrics.project_layout=go.kubebuilder.io/v4

# Copy files to locations specified by labels.

COPY deploy/ /manifests/
# todo fix the metadata
COPY bundle-ocp/metadata /metadata/
COPY --from=builder manifests/das-operator.clusterserviceversion.yaml /manifests/${CSV_FILE}

ARG NAME=das-operator-bundle
ARG DESCRIPTION="The das operator bundle."

LABEL com.redhat.component=$NAME
LABEL description=$DESCRIPTION
LABEL io.k8s.description=$DESCRIPTION
LABEL io.k8s.display-name=$NAME
LABEL name=$NAME
LABEL summary=$DESCRIPTION
LABEL distribution-scope=public
LABEL release="1"
LABEL url="https://access.redhat.com/"
LABEL vendor="Red Hat, Inc."
LABEL version="1"
LABEL maintainer="Red Hat"

# Licenses

COPY LICENSE /licenses/LICENSE
