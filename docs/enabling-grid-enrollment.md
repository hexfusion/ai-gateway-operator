# Enabling Grid enrollment

Grid enrollment is an optional service managed by AI Gateway operator. Enable it
explicitly on the cluster that hosts the Grid certificate authority:

```yaml
apiVersion: components.platform.opendatahub.io/v1alpha1
kind: AIGateway
metadata:
  name: default-aigateway
spec:
  aiGrid:
    managementState: Managed
    grid-enrollment:
      enabled: true
```

`grid-enrollment` is optional. Omission, `{}`, or `enabled: false` installs only
the Grid CRDs and operator. `aiGrid.managementState: Removed` also disables
enrollment. No ACM discovery or cluster labels select the enrollment host.

The DataScienceCluster API needs a separate change in opendatahub-operator to
expose and forward this field. Do not configure the field in a DSC until that
change is included in its payload.

## Payload and deployment

The module Deployment must provide both image references:

- `RELATED_IMAGE_ODH_PRAXIS_AI_GRID_ENROLLMENT_IMAGE`: Grid enrollment, including
  its `bootstrap` command and Kubernetes authorization support.
- `RELATED_IMAGE_ODH_PRAXIS_AI_GRID_ENROLLMENT_DB_IMAGE`: the SCLorg PostgreSQL 16
  image supported by the vendored chart.

Use digest-pinned images from the platform payload. Enabling enrollment without
both references reports a reconciliation error. Image selection is independent
of the optional API field.

The operator renders the pinned `grid-enrollment` chart into `ai-grid-enrollment`.
It creates namespace-scoped bootstrap RBAC, a bootstrap Job, PostgreSQL with an
8 Gi PVC, and the enrollment Deployment and Service. PostgreSQL requires a default
StorageClass. Kubernetes TokenReview and SubjectAccessReview authorize enrollment
administration. The chart creates administrator Roles; administrators bind subjects
explicitly. CSR audit recording is disabled in this installation profile.

The bootstrap Job generates missing CA material and database credentials, derives
the public CA bundle, and issues serving certificates. The existing Grid bootstrap
command owns certificate validation and renewal. The operator never renders
certificate bytes or enables forced CA regeneration. The signing key stays in
`ai-grid-enrollment`, outside the Grid operator's applications namespace.

Required Secret mounts prevent the workloads from starting before their material
exists. `AIGridReady` and aggregate readiness stay false until bootstrap succeeds
and the enrollment and database Deployments are available. Bootstrap failure is
reported in status; inspect the Job logs for details.

## Lifecycle

| Resource | Writer | Disable or remove AI Grid |
|---|---|---|
| Enrollment Deployment, database Deployment, Services and RBAC | AI Gateway operator | Remove |
| Bootstrap Job | AI Gateway operator | Remove |
| CA, serving certificates and database credentials | Grid enrollment bootstrap | Retain |
| Database PVC and namespace | AI Gateway operator | Retain, including deletion of the AIGateway object |

A changed bootstrap Job specification creates a new Job name, so image upgrades do
not patch an immutable Pod template. Completed Jobs have no TTL and are reused on
ordinary reconciliation. To retry a failed bootstrap or renew serving certificates
without an image change, delete its Job; the operator recreates it. Bootstrap
reuses existing CA and database credentials. Preserve and back up both the Secrets
and PVC when moving or reinstalling this service.

The Service is cluster-internal. External exposure, site invitations, local site
identity, and `GridNetwork` configuration remain separate installation steps.
This field selects the enrollment host; it does not configure a complete Grid.
