# InstaSlice Operator - Konflux Release Configuration Guide

## Overview

This guide documents the step-by-step process for adding new OpenShift Container Platform (OCP) release support to the InstaSlice Operator (Dynamic Accelerator Slicer) in the Konflux release infrastructure.

The InstaSlice operator uses File-Based Catalog (FBC) releases for different OCP versions. Each new OCP version requires changes in **two separate repositories**:

### Important Prerequisite

**Before configuring the release plans**, you must first create the Konflux application for the target OCP version in the instaslice-fbc repository (see Part A below). For example, for OCP 4.20, the application `dynamicacceleratorslicer-fbc-4-20` must exist in Konflux with:
- The application created in the `dynamicacceleratorsl-tenant` namespace
- The component `instaslice-fbc-4-20` configured and building successfully
- Tekton pipelines running without errors
- Container images being published to the workload registry

Once the application is created and building successfully, you can proceed to Part B to configure the release plans that reference this application.

### Part A: instaslice-fbc Repository
- New version directory with FBC catalog structure
- Tekton pipeline files for CI/CD automation
- Updated FBC generation script
- Generated catalog JSON files
- Creates the Konflux application (e.g., `dynamicacceleratorslicer-fbc-4-20`)

### Part B: konflux-release-data Repository
- Updates to ReleasePlanAdmission configurations
- New ReleasePlan resources for both stage and production
- Generated manifests in the auto-generated directory
- References the Konflux application created in Part A

## Prerequisites

### Required Tools
- Git
- Python 3 with pip
- tox (Python testing framework)
- kustomize v5.7.1 or later
- opm v1.47.0 or higher (Operator Package Manager)
- Access to the konflux-release-data repository
- Access to the instaslice-fbc repository

### Environment Setup

```bash
# Clone the repository (if not already done)
git clone https://github.com/openshift/instaslice-fbc.git
cd instaslice-fbc

# Create a new branch for your changes
git checkout -b add_ocp_<VERSION>_support
# Example: git checkout -b add_ocp_420_support
```

## Part A: InstaSlice FBC Repository Changes

This section covers creating the Konflux application and setting up the build pipelines for the new OCP version in the instaslice-fbc repository.

**Important:** Complete this part first to create the Konflux application (e.g., `dynamicacceleratorslicer-fbc-4-20`). This application must exist and be building successfully before you can configure the release plans in Part B.

After completing these steps, proceed to Part B for konflux-release-data repository changes.

### Step 1: Identify the OCP Version

Determine the OCP version number you're adding support for. For this guide, we'll use `4.20` as an example.

**Version Naming Convention:**
- OCP 4.18 → `418` or `4-18` (depending on context)
- OCP 4.19 → `419` or `4-19`
- OCP 4.20 → `420` or `4-20`

### Step 2: Create Version Directory Structure

Create a new directory for the OCP version based on the previous version.

```bash
# Copy the previous version directory structure
cp -r v4.19 v4.20

# The directory should contain:
# - catalog-template.yaml
# - Containerfile.catalog
# - catalog/instaslice-operator/ (directory)
```

### Step 3: Update catalog-template.yaml

Update the catalog template with the appropriate bundle images for the new OCP version.

**File:** `v4.20/catalog-template.yaml`

```yaml
Schema: olm.semver
GenerateMajorChannels: true
GenerateMinorChannels: false
Stable:
  Bundles:
  - Image: registry.redhat.io/dynamic-accelerator-slicer-tech-preview/instaslice-operator-bundle@sha256:<BUNDLE_SHA>
```

**Note:** The bundle image SHA256 should be obtained from the latest operator bundle release for OCP 4.20.

### Step 4: Verify Containerfile.catalog

The `Containerfile.catalog` file should already be correct from the copy, but verify it contains:

**File:** `v4.20/Containerfile.catalog`

```dockerfile
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest
COPY catalog /configs
RUN mkdir /configs/instaslice-operator
COPY catalog/instaslice-operator /configs/instaslice-operator
ENTRYPOINT ["/bin/sh", "-c", "exec /usr/bin/serve /configs"]
```

### Step 5: Create Tekton Pipeline Files

Create two Tekton pipeline files for the new OCP version in the `.tekton` directory.

#### 5a. Create Pull Request Pipeline

**File:** `.tekton/instaslice-fbc-4-20-pull-request.yaml`

Copy from `instaslice-fbc-4-19-pull-request.yaml` and update all version references:
- Replace `4-19` with `4-20` (in labels, names, application references)
- Replace `v4.19` with `v4.20` (in paths, CEL expressions)
- Update the CEL expression to trigger on `v4.20/***` changes

**Key changes:**
```yaml
metadata:
  annotations:
    pipelinesascode.tekton.dev/on-cel-expression: event == "pull_request" && target_branch == "main" && ( "v4.20/***".pathChanged() || ".tekton/instaslice-fbc-4-20-pull-request.yaml".pathChanged() || "fbc/v4.18/Containerfile.catalog".pathChanged() )
  labels:
    appstudio.openshift.io/application: dynamicacceleratorslicer-fbc-4-20
    appstudio.openshift.io/component: instaslice-fbc-4-20
  name: instaslice-fbc-4-20-on-pull-request
spec:
  params:
  - name: output-image
    value: quay.io/redhat-user-workloads/dynamicacceleratorsl-tenant/instaslice-fbc-4-20:on-pr-{{revision}}
  - name: dockerfile
    value: v4.20/Containerfile.catalog
  - name: path-context
    value: v4.20
  taskRunTemplate:
    serviceAccountName: build-pipeline-instaslice-fbc-4-20
```

#### 5b. Create Push Pipeline

**File:** `.tekton/instaslice-fbc-4-20-push.yaml`

Copy from `instaslice-fbc-4-19-push.yaml` and update all version references:
- Replace `4-19` with `4-20` (in labels, names, application references)
- Replace `v4.19` with `v4.20` (in paths, CEL expressions)
- Update the CEL expression to trigger on `v4.20/***` changes

**Key changes:**
```yaml
metadata:
  annotations:
    pipelinesascode.tekton.dev/on-cel-expression: event == "push" && target_branch == "main" && ( "v4.20/***".pathChanged() || ".tekton/instaslice-fbc-4-20-pull-request.yaml".pathChanged() || "fbc/v4.18/Containerfile.catalog".pathChanged() )
  labels:
    appstudio.openshift.io/application: dynamicacceleratorslicer-fbc-4-20
    appstudio.openshift.io/component: instaslice-fbc-4-20
  name: instaslice-fbc-4-20-on-push
spec:
  params:
  - name: output-image
    value: quay.io/redhat-user-workloads/dynamicacceleratorsl-tenant/instaslice-fbc-4-20:{{revision}}
  - name: dockerfile
    value: v4.20/Containerfile.catalog
  - name: path-context
    value: v4.20
  taskRunTemplate:
    serviceAccountName: build-pipeline-instaslice-fbc-4-20
```

### Step 6: Update generate-fbc.sh Script

Add the new OCP version to the FBC generation script.

**File:** `generate-fbc.sh`

```bash
#!/bin/sh

for OCP_VERSION in v4.16; do
    opm alpha render-template semver $OCP_VERSION/catalog-template.yaml > $OCP_VERSION/catalog/instaslice-operator/catalog.json;
done

for OCP_VERSION in v4.17 v4.18 v4.19 v4.20; do
    opm alpha render-template semver $OCP_VERSION/catalog-template.yaml --migrate-level=bundle-object-to-csv-metadata > $OCP_VERSION/catalog/instaslice-operator/catalog.json;
done
```

**Note:** OCP 4.20 uses the `--migrate-level=bundle-object-to-csv-metadata` flag like 4.17+.

### Step 7: Generate FBC Catalog

Generate the File-Based Catalog for the new version.

```bash
# Ensure you have opm version 1.47.0 or higher
opm version

# Generate the catalog
./generate-fbc.sh
```

**Expected output:**
- `v4.20/catalog/instaslice-operator/catalog.json` should be created/updated

### Step 8: Validate Changes

```bash
# Check that all files are created
ls -la v4.20/
ls -la .tekton/instaslice-fbc-4-20-*.yaml

# Verify catalog was generated
cat v4.20/catalog/instaslice-operator/catalog.json

# Run lint checks (if available)
./lint.sh
```

### Step 9: Commit and Push Changes

```bash
# Review changes
git status
git diff

# Stage all changes
git add v4.20/
git add .tekton/instaslice-fbc-4-20-pull-request.yaml
git add .tekton/instaslice-fbc-4-20-push.yaml
git add generate-fbc.sh

# Commit
git commit -m "$(cat <<'EOF'
Add OCP 4.20 support to InstaSlice FBC

This commit adds File-Based Catalog support for OpenShift Container
Platform 4.20 to the InstaSlice operator.

Changes:
- Created v4.20 directory with catalog structure
- Added catalog-template.yaml for OCP 4.20
- Created Tekton pipelines for pull requests and pushes
- Updated generate-fbc.sh to include v4.20
- Generated catalog.json using opm render-template

Related: Dynamic Accelerator Slicer OCP 4.20 release

EOF
)"

# Push to remote
git push origin add_ocp_<VERSION>_support
```

### Step 10: Create Pull Request

Create a pull request in the instaslice-fbc repository with your changes.

```bash
# Using GitHub CLI (if available)
gh pr create --title "Add OCP 4.20 FBC support" --body "This PR adds File-Based Catalog support for OpenShift Container Platform 4.20"

# Or manually through the GitHub web interface
```

### Step 11: Verify Konflux Application Creation

After your pull request is merged and the pipelines run successfully, verify that the Konflux application has been created:

- Check that `dynamicacceleratorslicer-fbc-4-20` application exists in the `dynamicacceleratorsl-tenant` namespace
- Verify that the component `instaslice-fbc-4-20` is configured and building successfully
- Ensure Tekton pipelines are running without errors
- Confirm container images are being published to `quay.io/redhat-user-workloads/dynamicacceleratorsl-tenant/instaslice-fbc-4-20`

Once the application is confirmed to be working, you can proceed to Part B to configure the release plans.

## Part B: konflux-release-data Repository Changes

This section covers the steps required in the konflux-release-data repository to configure the release plans.

**Important:** Before proceeding with Part B, ensure that the Konflux application for your target OCP version has been created and is building successfully (Part A must be completed first). The release plans created in this section will reference the application created in Part A.

After completing these steps, your new OCP version will be fully configured for both staging and production releases!

### Repository Location and Setup

```bash
# Clone the repository (if not already done)
git clone https://gitlab.cee.redhat.com/releng/konflux-release-data.git
cd konflux-release-data

# Create a new branch for your changes
git checkout -b das_<OCP_VERSION>
# Example: git checkout -b das_4.21

# Set up Python virtual environment
python3 -m venv .venv
source .venv/bin/activate
python3 -m pip install tox
```

### Step 1: Update ReleasePlanAdmission Files

Add the new OCP version to both production and staging ReleasePlanAdmission files.

**Files to modify:**
- `config/stone-prd-rh01.pg1f.p1/product/ReleasePlanAdmission/dynamicacceleratorsl/instaslice-fbc-prod.yaml`
- `config/stone-prd-rh01.pg1f.p1/product/ReleasePlanAdmission/dynamicacceleratorsl/instaslice-fbc-stage.yaml`

**Changes:**

For **production** (`instaslice-fbc-prod.yaml`):
```yaml
spec:
  applications:
    - dynamicacceleratorslicer-fbc-4-18
    - dynamicacceleratorslicer-fbc-4-19
    - dynamicacceleratorslicer-fbc-4-20  # Add this line
```

For **staging** (`instaslice-fbc-stage.yaml`):
```yaml
spec:
  applications:
    - dynamicacceleratorslicer-fbc-4-18
    - dynamicacceleratorslicer-fbc-4-19
    - dynamicacceleratorslicer-fbc-4-20  # Add this line
```

**Note:** The application name `dynamicacceleratorslicer-fbc-4-20` must match the application created in Part A.

### Step 2: Create ReleasePlan Resources

Create two new ReleasePlan files for the OCP version in the tenant configuration directory.

**Directory:** `tenants-config/cluster/stone-prd-rh01/tenants/dynamicacceleratorsl-tenant/`

#### 2a. Create Production ReleasePlan

**File:** `instaslice-fbc-420-prod-release-plan.yaml`

```yaml
---
apiVersion: appstudio.redhat.com/v1alpha1
kind: ReleasePlan
metadata:
  name: instaslice-fbc-420-prod-release-plan
  namespace: dynamicacceleratorsl-tenant
  labels:
    release.appstudio.openshift.io/auto-release: "false"
    release.appstudio.openshift.io/standing-attribution: "true"
    release.appstudio.openshift.io/releasePlanAdmission: "instaslice-fbc-prod"
spec:
  application: dynamicacceleratorslicer-fbc-4-20
  target: rhtap-releng-tenant
```

**Key Configuration:**
- `auto-release: "false"` - Production releases require manual triggering
- Application name follows pattern: `dynamicacceleratorslicer-fbc-4-<VERSION>`
- The application name must match the application created in Part A

#### 2b. Create Staging ReleasePlan

**File:** `instaslice-fbc-420-stage-release-plan.yaml`

```yaml
---
apiVersion: appstudio.redhat.com/v1alpha1
kind: ReleasePlan
metadata:
  name: instaslice-fbc-420-stage-release-plan
  namespace: dynamicacceleratorsl-tenant
  labels:
    release.appstudio.openshift.io/auto-release: "true"
    release.appstudio.openshift.io/standing-attribution: "true"
    release.appstudio.openshift.io/releasePlanAdmission: "instaslice-fbc-stage"
spec:
  application: dynamicacceleratorslicer-fbc-4-20
  target: rhtap-releng-tenant
```

**Key Configuration:**
- `auto-release: "true"` - Staging releases are automatic
- Application name must match production and the application created in Part A

### Step 3: Update Kustomization File

Add the new ReleasePlan files to the kustomization configuration.

**File:** `tenants-config/cluster/stone-prd-rh01/tenants/dynamicacceleratorsl-tenant/kustomization.yaml`

```yaml
---
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  # - <application-and-component-file-name>.yaml
  - instaslice-stage-release-plan.yaml
  - instaslice-prod-release-plan.yaml
  - instaslice-fbc-418-stage-release-plan.yaml
  - instaslice-fbc-419-stage-release-plan.yaml
  - instaslice-fbc-420-stage-release-plan.yaml      # Add this
  - instaslice-fbc-418-prod-release-plan.yaml
  - instaslice-fbc-419-prod-release-plan.yaml
  - instaslice-fbc-420-prod-release-plan.yaml       # Add this
```

**Important:** Maintain the ordering pattern - stage plans before production plans for each version.

### Step 4: Generate Manifests

Generate the Kubernetes manifests from the kustomization files.

```bash
cd tenants-config

# Install kustomize (if not already installed)
mkdir -p bin
./get-kustomize.sh bin

# Generate manifests for the dynamicacceleratorsl-tenant
./build-single.sh dynamicacceleratorsl-tenant bin/kustomize
```

**Expected Output:**
- New files created in `auto-generated/cluster/stone-prd-rh01/tenants/dynamicacceleratorsl-tenant/`:
  - `appstudio.redhat.com_v1alpha1_releaseplan_instaslice-fbc-420-prod-release-plan.yaml`
  - `appstudio.redhat.com_v1alpha1_releaseplan_instaslice-fbc-420-stage-release-plan.yaml`

### Step 5: Run Validation Tests

Run comprehensive validation to ensure all changes are correct.

```bash
# Return to repository root
cd ..

# Activate virtual environment (if not already active)
source .venv/bin/activate

# Run all validation tests
tox
```

**Validation Suite Includes:**
- `ruff-check` - Python code linting
- `ruff-format` - Code formatting checks
- `yamllint` - YAML file validation
- `shellcheck` - Shell script validation
- `test` - Schema validation and constraint checking
- `codeowners-lint` - CODEOWNERS file validation

**Expected Result:** All tests should pass with exit code 0

```
congratulations :) (XXX.XX seconds)
```

### Step 6: Verify Generated Manifests

Validate that manifests are properly generated and match source files.

```bash
cd tenants-config
./verify-manifests.sh ../.cache/bin/kustomize
```

**What This Checks:**
- Manifests in `auto-generated/` directory match source configurations
- No uncommitted changes in generated files
- Kustomize build succeeds for all tenants

**Note:** This script will fail if you haven't committed the auto-generated files. This is expected behavior - you should stage and commit both source and generated files together.

### Step 7: Stage and Commit Changes

Review and commit all changes to Git.

```bash
# Return to repository root
cd ..

# Check what has changed
git status

# Stage all modified and new files
git add config/stone-prd-rh01.pg1f.p1/product/ReleasePlanAdmission/dynamicacceleratorsl/
git add tenants-config/cluster/stone-prd-rh01/tenants/dynamicacceleratorsl-tenant/
git add tenants-config/auto-generated/cluster/stone-prd-rh01/tenants/dynamicacceleratorsl-tenant/

# Review staged changes
git diff --cached --stat

# Create commit
git commit -m "$(cat <<'EOF'
Add InstaSlice FBC release support for OCP 4.20

This commit adds File-Based Catalog (FBC) release configurations for
InstaSlice operator on OpenShift Container Platform 4.20.

Changes:
- Updated ReleasePlanAdmission for prod and stage environments
- Created ReleasePlan resources for OCP 4.20 (prod and stage)
- Generated manifests via kustomize build
- All validation tests passing

Related: Dynamic Accelerator Slicer team request

EOF
)"
```

### Step 8: Push and Create Merge Request

```bash
# Push branch to remote
git push origin das_<OCP_VERSION>

# Create merge request using GitLab CLI (if available)
# Or manually create MR through GitLab web interface
```

## File Checklist

### Part A: instaslice-fbc Repository

For each new OCP version, ensure these files are modified/created:

- [ ] `v<VERSION>/` - Directory created (e.g., `v4.20/`)
- [ ] `v<VERSION>/catalog-template.yaml` - Created with bundle image reference
- [ ] `v<VERSION>/Containerfile.catalog` - Created (copied from previous version)
- [ ] `v<VERSION>/catalog/instaslice-operator/` - Directory created
- [ ] `v<VERSION>/catalog/instaslice-operator/catalog.json` - Generated by generate-fbc.sh
- [ ] `.tekton/instaslice-fbc-<VERSION_DASH>-pull-request.yaml` - Created (e.g., `instaslice-fbc-4-20-pull-request.yaml`)
- [ ] `.tekton/instaslice-fbc-<VERSION_DASH>-push.yaml` - Created (e.g., `instaslice-fbc-4-20-push.yaml`)
- [ ] `generate-fbc.sh` - Modified to include new version
- [ ] Konflux application `dynamicacceleratorslicer-fbc-<VERSION_DASH>` created and building successfully

### Part B: konflux-release-data Repository

For each new OCP version, ensure these files are modified/created:

- [ ] `config/stone-prd-rh01.pg1f.p1/product/ReleasePlanAdmission/dynamicacceleratorsl/instaslice-fbc-prod.yaml` - Modified
- [ ] `config/stone-prd-rh01.pg1f.p1/product/ReleasePlanAdmission/dynamicacceleratorsl/instaslice-fbc-stage.yaml` - Modified
- [ ] `tenants-config/cluster/stone-prd-rh01/tenants/dynamicacceleratorsl-tenant/instaslice-fbc-<VERSION>-prod-release-plan.yaml` - Created
- [ ] `tenants-config/cluster/stone-prd-rh01/tenants/dynamicacceleratorsl-tenant/instaslice-fbc-<VERSION>-stage-release-plan.yaml` - Created
- [ ] `tenants-config/cluster/stone-prd-rh01/tenants/dynamicacceleratorsl-tenant/kustomization.yaml` - Modified
- [ ] `tenants-config/auto-generated/cluster/stone-prd-rh01/tenants/dynamicacceleratorsl-tenant/appstudio.redhat.com_v1alpha1_releaseplan_instaslice-fbc-<VERSION>-prod-release-plan.yaml` - Generated
- [ ] `tenants-config/auto-generated/cluster/stone-prd-rh01/tenants/dynamicacceleratorsl-tenant/appstudio.redhat.com_v1alpha1_releaseplan_instaslice-fbc-<VERSION>-stage-release-plan.yaml` - Generated

## Quick Reference Commands

### Part A: instaslice-fbc Repository

```bash
# Complete workflow for adding OCP 4.21 FBC support
cd instaslice-fbc
git checkout -b add_ocp_421_support

# Create version directory structure
cp -r v4.20 v4.21

# Update catalog-template.yaml with correct bundle SHA
# Edit v4.21/catalog-template.yaml

# Create Tekton pipeline files
# Copy and modify .tekton/instaslice-fbc-4-20-pull-request.yaml to instaslice-fbc-4-21-pull-request.yaml
# Copy and modify .tekton/instaslice-fbc-4-20-push.yaml to instaslice-fbc-4-21-push.yaml

# Update generate-fbc.sh
# Add v4.21 to the second loop (with --migrate-level flag)

# Generate catalog
./generate-fbc.sh

# Commit changes
git add v4.21/
git add .tekton/instaslice-fbc-4-21-*.yaml
git add generate-fbc.sh
git commit -m "Add OCP 4.21 support to InstaSlice FBC"
git push origin add_ocp_421_support

# Create pull request and wait for merge
# Verify Konflux application is created and building successfully
```

### Part B: konflux-release-data Repository

```bash
# Complete workflow for adding OCP 4.21 (example)
cd konflux-release-data
git checkout -b das_4.21

# Edit files (Part B Steps 1-3)
# ... make your changes to ReleasePlanAdmission, ReleasePlan, and kustomization files ...

# Generate manifests
cd tenants-config
mkdir -p bin
./get-kustomize.sh bin
./build-single.sh dynamicacceleratorsl-tenant bin/kustomize
cd ..

# Run validation
python3 -m venv .venv
source .venv/bin/activate
python3 -m pip install tox
tox

# Commit changes
git add config/stone-prd-rh01.pg1f.p1/product/ReleasePlanAdmission/dynamicacceleratorsl/
git add tenants-config/cluster/stone-prd-rh01/tenants/dynamicacceleratorsl-tenant/
git add tenants-config/auto-generated/cluster/stone-prd-rh01/tenants/dynamicacceleratorsl-tenant/
git commit -m "Add InstaSlice FBC release support for OCP 4.21"
git push origin das_4.21
```

## Common Issues and Troubleshooting

### Part A: instaslice-fbc Repository Issues

#### Issue: opm not found or version too old

**Cause:** Operator Package Manager (opm) not installed or version is below 1.47.0

**Solution:**
```bash
# Check current version
opm version

# Install or upgrade opm
# For Linux:
curl -L https://github.com/operator-framework/operator-registry/releases/latest/download/linux-amd64-opm -o opm
chmod +x opm
sudo mv opm /usr/local/bin/

# For macOS:
brew install operator-sdk
```

#### Issue: generate-fbc.sh fails with "command not found: opm"

**Cause:** opm is not in PATH or not installed

**Solution:** Install opm as shown above, or specify the full path in generate-fbc.sh

#### Issue: Tekton pipeline doesn't trigger on PR or push

**Cause:** CEL expression in pipeline file doesn't match the changed files

**Solution:** Verify the `pipelinesascode.tekton.dev/on-cel-expression` annotation matches your version directory:
```yaml
# For v4.20, ensure it contains:
"v4.20/***".pathChanged()
```

#### Issue: catalog.json not generated after running generate-fbc.sh

**Cause:** Missing catalog directory or incorrect version in script

**Solution:**
```bash
# Ensure the catalog directory exists
mkdir -p v4.20/catalog/instaslice-operator

# Verify the version is in generate-fbc.sh
grep "v4.20" generate-fbc.sh

# Run the script with verbose output
sh -x ./generate-fbc.sh
```

#### Issue: Bundle image SHA not available

**Cause:** New operator bundle not yet published for the OCP version

**Solution:**
- Wait for the operator bundle to be built and published for the target OCP version
- Check registry.redhat.io for the latest bundle digest
- Coordinate with the InstaSlice operator release team

### Part B: konflux-release-data Repository Issues

#### Issue: tox not found

**Solution:**
```bash
python3 -m venv .venv
source .venv/bin/activate
python3 -m pip install tox
```

#### Issue: kustomize not found

**Solution:**
```bash
cd tenants-config
mkdir -p bin
./get-kustomize.sh bin
# Then use bin/kustomize as the path
```

#### Issue: verify-manifests.sh fails with "Did you forget to build the manifests locally?"

**Cause:** Generated manifests don't match source files or aren't committed.

**Solution:**
```bash
cd tenants-config
./build-manifests.sh bin/kustomize
cd ..
git add tenants-config/auto-generated/
```

#### Issue: yamllint failures

**Cause:** YAML formatting issues (indentation, line length, etc.)

**Solution:** Review the error output and fix YAML formatting:
- Use 2 spaces for indentation
- No trailing spaces
- Files should end with newline

#### Issue: CODEOWNERS lint failure

**Cause:** CODEOWNERS file not in alphabetical order

**Solution:**
```bash
tox -e codeowners-lint-fix
git add CODEOWNERS
```

#### Issue: ReleasePlan references non-existent application

**Cause:** Part A not completed - Konflux application doesn't exist yet

**Solution:**
- Complete Part A first to create the Konflux application
- Verify the application is building successfully in Konflux
- Ensure the application name in Part B matches the one created in Part A

## Key Differences Between Staging and Production

| Aspect | Staging | Production |
|--------|---------|------------|
| `auto-release` label | `"true"` | `"false"` |
| Release trigger | Automatic | Manual |
| Index registry | `iib-pub-pending` | `iib-pub` |
| Target index | Empty (staged) | `quay.io/redhat-prod/...` |
| Use case | Testing & validation | Customer releases |

## References

- [Konflux Release Service Documentation](https://github.com/konflux-ci/release-service-catalog)
- [konflux-release-data Repository](https://gitlab.cee.redhat.com/releng/konflux-release-data)
- [instaslice-fbc Repository](https://github.com/openshift/instaslice-fbc)
- [Konflux OLM Operator Sample Documentation](https://github.com/konflux-ci/olm-operator-konflux-sample/blob/main/docs/konflux-onboarding.md#building-a-file-based-catalog)
- [Operator Package Manager (opm)](https://github.com/operator-framework/operator-registry)

---
**Last Updated:** 2026-01-22
