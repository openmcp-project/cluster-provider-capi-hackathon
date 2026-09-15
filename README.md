# cluster-provider-capi

A cluster provider for the [OpenControlPlane](https://open-control-plane.io) ecosystem that provisions Kubernetes clusters using [Cluster API](https://cluster-api.sigs.k8s.io/) (CAPI) topology / ClusterClass.

The provider creates and manages a CAPI `Cluster` resource for each OpenControlPlane `Cluster` resource with `spec.profile: capi`. All ClusterClass templates and infrastructure providers must already be installed — this provider only manages the `Cluster` resource itself.

> **Note**: This provider is infrastructure-agnostic — it works with any CAPI infrastructure provider that supports ClusterClass topology. The setup instructions and examples in this document showcase GCP (GKE via CAPG) as the reference implementation. Adapting to other infrastructure providers (AWS, Azure, vSphere, etc.) requires replacing the ClusterClass, templates, and credentials with their respective equivalents.

## Prerequisites

- OpenControlPlane platform cluster running the `openmcp-operator` e.g. via the [Quickstart guide](https://open-control-plane.io/operators/quickstart/)
- `helm` CLI
- `kubectl` CLI configured to point at the **platform cluster**
- GCP service account JSON key file with GKE permissions

## Installation

### 1. Add Helm repositories

```bash
helm repo add jetstack https://charts.jetstack.io
helm repo add capi-operator https://kubernetes-sigs.github.io/cluster-api-operator
helm repo update
```

### 2. Install cert-manager

cert-manager is required by the CAPI operator:

```bash
helm install cert-manager jetstack/cert-manager \
  --namespace cert-manager \
  --create-namespace \
  --set installCRDs=true
```

### 3. Create the GCP credentials secret

The CAPI GCP infrastructure provider (CAPG) needs a GCP service account key to manage GKE clusters.

#### Required IAM roles

As per the [CAPG documentation](https://cluster-api-gcp.sigs.k8s.io/quick-start.html#create-a-service-account), the service account needs:

| Role | Scope | Purpose |
|------|-------|---------|
| `roles/editor` | Project | Create and manage GCP resources including GKE clusters |
| `roles/iam.serviceAccountTokenCreator` | Compute Engine default SA | Issue tokens for the node service account (required for GKE) |

```bash
export PROJECT_ID=my-gcp-project-id   # TODO: replace
export PROJECT_NUMBER=$(gcloud projects describe ${PROJECT_ID} --format="value(projectNumber)")
export SA_EMAIL=my-sa@${PROJECT_ID}.iam.gserviceaccount.com   # TODO: replace
export COMPUTE_SA="${PROJECT_NUMBER}-compute@developer.gserviceaccount.com"

# Project-wide
gcloud projects add-iam-policy-binding ${PROJECT_ID} \
  --member="serviceAccount:${SA_EMAIL}" \
  --role="roles/editor"

# Scoped to the Compute Engine default service account
gcloud iam service-accounts add-iam-policy-binding ${COMPUTE_SA} \
  --project=${PROJECT_ID} \
  --member="serviceAccount:${SA_EMAIL}" \
  --role="roles/iam.serviceAccountTokenCreator"
```

#### Create the secret

```bash
export CREDENTIALS_SECRET_NAME="gcp-credentials-secret"
export CREDENTIALS_SECRET_NAMESPACE="default"
export GCP_B64ENCODED_CREDENTIALS=$(cat /path/to/gcp-serviceaccount-key.json | base64 | tr -d '\n')

kubectl create secret generic "${CREDENTIALS_SECRET_NAME}" \
  --from-literal=GCP_B64ENCODED_CREDENTIALS="${GCP_B64ENCODED_CREDENTIALS}" \
  --namespace "${CREDENTIALS_SECRET_NAMESPACE}"
```

### 4. Install the CAPI operator with GCP infrastructure provider

This installs CAPI core, the kubeadm bootstrap provider, and the GCP infrastructure provider (CAPG) with GKE and ClusterTopology feature gates enabled:

```bash
helm install capi-operator capi-operator/cluster-api-operator \
  --create-namespace -n capi-operator-system \
  --set configSecret.name=${CREDENTIALS_SECRET_NAME} \
  --set configSecret.namespace=${CREDENTIALS_SECRET_NAMESPACE} \
  --set core.cluster-api.manager.featureGates.ClusterTopology=true \
  --set bootstrap.kubeadm.manager.featureGates.ClusterTopology=true \
  --set infrastructure.gcp.manager.featureGates.ClusterTopology=true \
  --set infrastructure.gcp.manager.featureGates.GKE=true \
  --wait --timeout 90s
```

### 5. Install the CRD

Run the init command to install the `ProviderConfig` CRD on the platform cluster:

```bash
export POD_NAMESPACE=openmcp-system

go run ./cmd/cluster-provider-capi init \
  --environment=local \
  --provider-name=capi
```

Or apply the CRD manifest directly:

```bash
kubectl apply -f api/crds/manifests/capi.cluster.open-control-plane.io_providerconfigs.yaml
```

### 6. Deploy the ClusterClass and templates

Apply all CAPI infrastructure templates and the `ClusterClass` to the platform cluster:

```bash
kubectl apply -f examples/gcp/clusterclass.yaml
```

This includes:
- `ClusterClass` (`gke-default-class`)
- `GCPManagedClusterTemplate`
- `GCPManagedControlPlaneTemplate`
- `GCPManagedMachinePoolTemplate`
- `KubeadmConfigTemplate`

### 7. Create the ClusterProfile

The `ClusterProfile` advertises available Kubernetes versions and links the profile name `capi` to this provider:

```yaml
apiVersion: clusters.openmcp.cloud/v1alpha1
kind: ClusterProfile
metadata:
  name: capi
spec:
  providerRef:
    name: capi
  providerConfigRef:
    name: capi
  supportedVersions:
    - version: 1.36.2-gke.2064000
```

```bash
kubectl apply -f examples/gcp/clusterprofile.yaml
```

### 8. Create the ProviderConfig

The `ProviderConfig` tells the provider which `ClusterClass` to use and what topology variables to pass to every provisioned cluster.

> **Before applying**: edit `examples/gcp/providerconfig.yaml` and replace `my-gcp-project-id` with your actual GCP project ID.

```bash
kubectl apply -f examples/gcp/providerconfig.yaml
```

> The `clusterClassNamespace` must match the namespace where the `ClusterClass` is deployed.
> CAPI requires the `Cluster` resource and its `ClusterClass` to be in the same namespace.

### 9. Configure the openmcp-operator scheduler

> **Before applying**: edit `examples/gcp/openmcp-operator-configmap.yaml` and replace the Kubernetes version if needed.

```bash
kubectl apply -f examples/gcp/openmcp-operator-configmap.yaml
```

Then restart the operator to pick up the change:

```bash
kubectl rollout restart deployment/openmcp-operator -n openmcp-system
```

### 10. Run the provider

```bash
export POD_NAMESPACE=openmcp-system

go run -buildvcs=false ./cmd/cluster-provider-capi run \
  --environment=local \
  --provider-name=capi \
  --metrics-secure=false \
  --metrics-bind-address=:8080 \
  --health-probe-bind-address=:8081
```

## Usage

Switch to the **onboarding cluster** and create a `ControlPlane`:

```bash
kubectl apply -f examples/gcp/controlplane.yaml
```

The following resources are created automatically:

1. OpenControlPlane `Cluster` (on the platform cluster, by the scheduler)
2. CAPI `Cluster` with topology referencing `gke-default-class` (by this provider)
3. `GCPManagedControlPlane`, `GCPManagedCluster`, `GCPManagedMachinePool` (by CAPI topology expansion)
4. GKE cluster (by CAPG)

Once the GKE cluster is provisioned, the `ControlPlane` transitions to `Ready`.

## How it works

The provider watches OpenControlPlane `Cluster` resources with `spec.profile: capi`. For each one it:

1. Looks up the `ProviderConfig` for the configured `--provider-name`
2. Fetches the `ClusterClass` to discover all defined machine pool classes
3. Creates a CAPI `Cluster` in the `clusterClassNamespace` with:
   - topology class pointing to the configured `ClusterClass`
   - topology variables from `ProviderConfig.spec.topologyVariables`
   - one `MachinePool` entry per machine pool class defined in the `ClusterClass`, with `replicas: 3` (suitable for GKE regional clusters)
4. Tracks the CAPI `Cluster` status and reflects it on the OpenControlPlane `Cluster`
5. On deletion, deletes the CAPI `Cluster` and waits for the infrastructure to be cleaned up

CAPI Cluster names are derived as `capi-<8-char-hash>` to stay within GKE's 40-character node pool name limit.

## Known limitations

- **Kubernetes version**: Must be a full GKE version string (e.g. `1.36.2-gke.2064000`), not short semver. Set it in the openmcp-operator ConfigMap under `scheduler.purposeMappings.mcp.template.spec.kubernetes.version`.
- **Node pool replicas**: Hardcoded to `3` to satisfy GKE regional cluster requirements (1 node per zone × 3 zones). This is suitable for regional clusters in `europe-west1` and similar 3-zone regions.
- **ControlPlane deletion**: Due to a known issue in `ps-managedcontrolplane`, the `ControlPlane` on the onboarding cluster may remain in `Terminating` after the GKE cluster is deleted (GKE deletion takes 5-10 minutes and the controller misses the namespace deletion event). Switch to the onboarding cluster and trigger a reconcile with:
  ```bash
  kubectl annotate controlplane.core.open-control-plane.io <name> -n <namespace> \
    "openmcp.cloud/operation=reconcile" --overwrite
  ```

## Support, Feedback, Contributing

This project is open to feature requests/suggestions, bug reports etc. via [GitHub issues](https://github.com/openmcp-project/cluster-provider-gcp-hackathon/issues). Contribution and feedback are encouraged and always welcome. For more information about how to contribute, the project structure, as well as additional contribution information, see our [Contribution Guidelines](https://github.com/openmcp-project/.github/blob/main/CONTRIBUTING.md).

## Security / Disclosure

If you find any bug that may be a security problem, please follow our instructions at [in our security policy](https://github.com/openmcp-project/cluster-provider-gcp-hackathon/security/policy) on how to report it. Please do not create GitHub issues for security-related doubts or problems.

## Code of Conduct

We as members, contributors, and leaders pledge to make participation in our community a harassment-free experience for everyone. By participating in this project, you agree to abide by its [Code of Conduct](https://github.com/openmcp-project/.github/blob/main/CODE_OF_CONDUCT.md) at all times.

## Licensing

Copyright OpenControlPlane contributors. Please see our [LICENSE](LICENSE) for copyright and license information. Detailed information including third-party components and their licensing/copyright information is available [via the REUSE tool](https://api.reuse.software/info/github.com/openmcp-project/cluster-provider-gcp-hackathon).

---

<p align="center">
  <a href="https://apeirora.eu/content/projects/">
    <img alt="BMWK-EU funding logo" src="https://apeirora.eu/assets/img/BMWK-EU.png" width="300"/>
  </a>
</p>

<p align="center">
  OpenControlPlane is part of <a href="https://apeirora.eu/content/projects/">ApeiroRA</a>, an EU Important Project of Common European Interest (IPCEI-CIS).
</p>

<p align="center">
  Copyright Linux Foundation Europe. For web site terms of use, trademark policy and other project policies please see <a href="https://linuxfoundation.eu/en/policies">https://linuxfoundation.eu/en/policies</a>.
</p>
