FROM docker.io/golang:1.24-alpine3.21 as build

WORKDIR /workspace
COPY . .
RUN apk upgrade --no-cache
RUN apk add tzdata make git
RUN GO_BUILD_PACKAGES=./cmd/instaslice-operator make

FROM docker.io/alpine:3.21
RUN apk upgrade --no-cache
RUN apk add tzdata
WORKDIR /
COPY --from=build /workspace/instaslice-operator /usr/bin
USER 65532:65532

ARG NAME=instaslice-operator
ARG DESCRIPTION="The Instaslice operator."

# Licenses
COPY LICENSE /licenses/LICENSE
