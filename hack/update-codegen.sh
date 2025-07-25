#!/usr/bin/env bash
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_ROOT=$(unset CDPATH && cd $(dirname "${BASH_SOURCE[0]}")/.. && pwd)

source "${SCRIPT_ROOT}/vendor/k8s.io/code-generator/kube_codegen.sh"

kube::codegen::gen_helpers \
  --boilerplate "${SCRIPT_ROOT}/hack/boilerplate.go.txt" \
  "${SCRIPT_ROOT}/pkg/apis"

# NOTE(jchaloup): --applyconfig-openapi-schema needs to point to a valid openapi.json file
# Steps to generate it:
# 1. copy paste pkg/apis/leaderworkersetoperator/v1/types_leaderworkersetoperator.go under github.com/openshift/api/ under operator/v1 directory (and update the file to remove the cyclic dependency)
# 2. "make update-openapi" under github.com/openshift/api/ repository
# 3. point --applyconfig-openapi-schema to the regenerated openapi/openapi.json (or copy the file into this repository)
# 4. "make generate-clients" under this repository

kube::codegen::gen_client \
  --output-dir "${SCRIPT_ROOT}/pkg/generated" \
  --output-pkg "github.com/openshift/instaslice-operator/pkg/generated" \
  --applyconfig-externals "github.com/openshift/api/operator/v1.OperatorSpec:github.com/openshift/client-go/operator/applyconfigurations/operator/v1,github.com/openshift/api/operator/v1.OperatorStatus:github.com/openshift/client-go/operator/applyconfigurations/operator/v1,github.com/openshift/api/operator/v1.OperatorCondition:github.com/openshift/client-go/operator/applyconfigurations/operator/v1,github.com/openshift/api/operator/v1.GenerationStatus:github.com/openshift/client-go/operator/applyconfigurations/operator/v1" \
  --applyconfig-openapi-schema openapi.json \
  --boilerplate "${SCRIPT_ROOT}/hack/boilerplate.go.txt" \
  --with-applyconfig \
  --with-watch \
  "${SCRIPT_ROOT}/pkg/apis"
