# Pod-Idle-Scaler
**Operator** that automatically scales down idle Kubernetes Deployments to a specified replica count based on custom activity probes.

[![Main Pipeline](https://github.com/Zapi-web/pod-idle-scaler/actions/workflows/main.yml/badge.svg)](https://github.com/Zapi-web/pod-idle-scaler/actions/workflows/main.yml)


## Description

**Pod-Idle-Scaler** evaluates application activity using either **HTTP probes** or **PromQL queries** and scales down workloads when traffic ceases for a designated duration.

### Under the Hood
* **Operator** does reconcile loop, for `isActive` probe doesn't create new HTTP Clients and cache and use old ones from pool.
* Depends on active status, saves phase of deployment and when starts new iteration, looks on it, decides what it supposed to do
* **Operator** hooks annotation to your deployment, when scales it down to remember original amount of replicas. And deletes it when scales up

## customResourceDefinitions

| Field | Type | Default | Description |
| :--- | :--- | :--- | :--- |
| **scaleTargetRef** | object | - (required) | Reference to the target deployment, kind should be Deployment |
| **trigger** | object | - (required) | Which trigger the operator uses to understand (PromQL or HTTP) |
| **idleTimeout** | duration | 15m | How long the scaler will wait before changing phase to sleeping and scale down |
| **checkTimeout** | duration | 1m | How often the scaler needs to check the target deployment |
| **scaleMinimum** | int32 (>=0) | 1 | How many replicas we need after scaling down |

**Trigger** uses type: **HTTP** or **PromQL** so you can connect your metrics from Prometheus/VictoriaMetrics to it.

For ``trigger.http`` should be specified: url another fields like: method (POST,GET,HEAD), expect code (200), body pattern (regular expression), headers, tokenSecretRef (if you need to specify secret token in headers), tlsConfig can be specified by default

**You can NOT specify two types at the same time, scaler will not be deployed**

For ``trigger.promql`` required only: url, and query another fields are almost similar to http, but do not have regular expression, and have a threshold

## Scaler Phases

* **Active** - Deployment have load, so it will be not scaled down
* **Idling** - Target doesn't have any load, waiting for `idleTimeout`
* **Sleeping** - Target is scaled down to `scaleMinimum`
* **Error** - Scaler received an error, needs to be checked

## Getting Started

### Prerequisites
- **go** version v1.26.0+
- **docker** v20.10+ (BuildKit is required for multi-arch builds)
- **kubectl** v1.28+
- Access to a **Kubernetes** v1.28+ cluster
- **kind** v0.33.0+ — only needed for the e2e tests, which require an isolated cluster

### Install from a release

Every tagged release ships a self-contained `install.yaml` with the CRD, RBAC, and the
controller Deployment already wired to the published image. Nothing to build.

1. Install the operator (it lands in the `k8s-operator-system` namespace):

```sh
kubectl apply -f https://github.com/Zapi-web/pod-idle-scaler/releases/latest/download/install.yaml
```

2. Verify the controller is running:

```sh
kubectl -n k8s-operator-system get deployment
```

3. Create a test workload **and a Service for it**. The `IdleScaler` must live in the
same namespace as the workload it scales, and the Service matters: a Deployment alone
does not create a DNS name, so the trigger URL would not resolve.

```sh
kubectl create deployment my-api --image=nginx --replicas=2
kubectl expose deployment my-api --port=80 --name=my-api
```

4. Apply an `IdleScaler` for it. Note the fully qualified URL: the operator runs in
`k8s-operator-system`, so a short name like `http://my-api/` would be resolved only
inside the operator's own namespace and fail. `checkTimeout: 5s` keeps this example
short (the default is `1m`).

```sh
cat <<'EOF' | kubectl apply -f -
apiVersion: finops.zapi-web.github.io/v1alpha1
kind: IdleScaler
metadata:
  name: my-api-idler
spec:
  scaleTargetRef:
    kind: Deployment
    name: my-api
  idleTimeout: 30s
  checkTimeout: 5s
  scaleMinimum: 1
  trigger:
    type: HTTP
    http:
      url: http://my-api.default.svc.cluster.local/healthz
      expectedCode: 200
EOF
```

5. Watch the phase change. This example probes `/healthz`, which the plain `nginx`
image does not serve — it answers `404`, and any reply other than `expectedCode` counts
as "no activity". After `idleTimeout` of that, the workload is scaled to `scaleMinimum`:

```sh
kubectl get idlescaler my-api-idler -w      # Active -> Idling -> Sleeping
kubectl get deployment my-api               # replicas drop to 1
```

> **NOTE:** A trigger that *errors* (for example an unresolvable host or an unreachable
> URL) is not treated as "no activity". The operator stops acting and reports
> `phase: Error`, leaving the workload alone so that a broken trigger can never scale
> down a healthy service. To tell the two apart, an unsupported HTTP answer gives
> `Idling`/`Sleeping`, while a connection error gives `Error`.

To use the built-in example instead, note that it points at `my-api` with the URL
`https://my-api.example.com/healthz`. Create that Deployment and Service first, or edit
`config/samples/finops_v1alpha1_idlescaler.yaml` to match your own workload, then apply
it with `kubectl apply -k config/samples/`.

### Build and deploy from source

1. Clone the repository and install the CRDs:

```sh
make install
```

2. Build and push the manager image to your own registry:

```sh
make docker-build docker-push IMG=<some-registry>/pod-idle-scaler:tag
```

**NOTE:** The image must be pushed to a registry your cluster can pull from. Make sure
the credentials for `IMG` are configured, otherwise `docker-push` and the subsequent
image pull will fail. Run `make build-installer IMG=<some-registry>/pod-idle-scaler:tag`
to produce a `dist/install.yaml` wired to your own image.

3. Deploy the manager to the cluster:

```sh
make deploy IMG=<some-registry>/pod-idle-scaler:tag
```

> **NOTE:** If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

### To Uninstall

**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete idlescaler my-api-idler
```

The operator's finalizer restores the target Deployment to its original replica count
before the `IdleScaler` is removed. For a release install this is all you need.

**If you installed from source**, also remove the CRDs and the controller:

```sh
make uninstall   # delete the CRDs
make undeploy    # delete the controller
```

> **WARNING:** Delete the `IdleScaler` instances *before* removing the CRDs. Deleting a
> CRD first skips the operator's finalizer entirely, leaving the Deployments it had
> scaled down stuck at `scaleMinimum` with no one to restore them.

## Project Distribution

### By providing a bundle with all YAML files

1. Build an installer for an image you have already published to a registry:

```sh
make build-installer IMG=<some-registry>/pod-idle-scaler:tag
```

**NOTE:** This target generates `dist/install.yaml`, which contains all the resources
built with Kustomize — CRDs, RBAC, and the manager Deployment — wired to `IMG`. Installing
it requires only `kubectl`, no additional tooling.

2. Install it:

```sh
kubectl apply -f dist/install.yaml
```

Released versions of this file are attached to every tag, so end users can install a
version without building anything:

```sh
kubectl apply -f https://github.com/Zapi-web/pod-idle-scaler/releases/latest/download/install.yaml
```

## Contributing

Contributions are welcome via pull requests. PRs must pass the CI pipeline (unit tests, lint, e2e) before merging.

Run these locally before opening a PR:

```sh
make test        # unit tests (envtest)
make test-e2e    # e2e tests — requires an isolated Kind cluster, not your dev/prod one
make lint-fix    # auto-fix code style
```

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

