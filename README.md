# resource-claim-image-configurator

Proof-of-concept controller demonstrating how DRA Device Binding Conditions (KEP-5007) can enable runtime container image selection based on device allocation results.

The goal of this PoC is to validate a future API extension where `BindingConditions` would be specified directly in the ResourceClaim, allowing an external controller to mutate a Pod's container image after the scheduler decides which device to allocate. In this PoC, we emulate that behavior by declaring `BindingConditions` on the ResourceSlice side (via the DRA driver), since the ResourceClaim-side API does not yet exist.

## Overview

When a Pod requests a device via DRA (Dynamic Resource Allocation) with a prioritized list (`firstAvailable`), the scheduler picks the best available device.
This controller inspects the allocation result and patches the Pod's container image to match the allocated device, enabling runtime selection of device-specific images.

For example, given a `ResourceClaimTemplate` with GPU as the first priority and CPU as the fallback:

- The first Pod is allocated a GPU device (`gpu.example.com`) because it has the highest priority. The controller reads the matching config from the ResourceClaim and patches the container image (e.g., `busybox:latest` → `fedora:latest`).
- The second Pod cannot get a GPU because it is already occupied. The scheduler falls back to `cpu.example.com` via the prioritized list, and the controller patches the container image accordingly (e.g., `busybox:latest` → `ubuntu:latest`).

The flow:

1. User creates a `ResourceClaimTemplate` with a prioritized device request (`firstAvailable`) and opaque config that maps each subrequest to a container image.
2. The scheduler allocates a device whose ResourceSlice declares `bindingConditions: ["BindingConditions"]`, blocking Pod startup until this controller acknowledges.
   > **Note:** Since the ResourceClaim-side `BindingConditions` API does not yet exist, this PoC emulates the behavior by declaring `BindingConditions` on the ResourceSlice side (via the DRA driver).
3. This controller detects the pending condition, reads the config from the claim's allocation result, and patches the Pod's container image to the one specified for the chosen subrequest.
4. The controller marks the binding condition as satisfied (`status: "True"`, `reason: ImagePatched`).
5. The kubelet proceeds to start the Pod with the correct image.

## Prerequisites

- Kubernetes v1.34+ with `DynamicResourceAllocation` and `DRADeviceBindingConditions` feature gates enabled
- [dra-example-driver](https://github.com/kubernetes-sigs/dra-example-driver) deployed as the DRA driver

## Setup

### 1. Deploy dra-example-driver with two drivers

Follow the [dra-example-driver README](https://github.com/kubernetes-sigs/dra-example-driver/tree/main) to deploy the driver twice — once for `gpu.example.com` and once for `cpu.example.com`. Each instance creates ResourceSlices with `bindingConditions` on its devices.

Verify that ResourceSlices are created with `bindingConditions`:

```bash
kubectl get resourceslice
```

Expected output:

```
NAME                                                            NODE                                DRIVER            POOL                                AGE
00000-gpu.example.com-dra-example-driver-cluster-worker-xxxxx   dra-example-driver-cluster-worker   gpu.example.com   dra-example-driver-cluster-worker   1m
00000-cpu.example.com-dra-example-driver-cluster-worker-xxxxx   dra-example-driver-cluster-worker   cpu.example.com   dra-example-driver-cluster-worker   1m
```

Each device in the ResourceSlice should have `bindingConditions` declared:

<details>
<summary>Full ResourceSlice YAML example (two drivers)</summary>

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceSlice
metadata:
  creationTimestamp: "2026-05-19T08:18:51Z"
  generateName: 00000-cpu.example.com-dra-example-driver-cluster-worker-
  generation: 1
  name: 00000-cpu.example.com-dra-example-driver-cluster-worker-r9957
  ownerReferences:
    - apiVersion: v1
      controller: true
      kind: Node
      name: dra-example-driver-cluster-worker
      uid: 46122907-4eaa-4e45-857c-fcad187eaeb6
  resourceVersion: "1058"
  uid: 711dd16a-41d0-4afd-9e1b-c3c34b50697e
spec:
  devices:
    - attributes:
        driverVersion:
          version: 1.0.0
        index:
          int: 0
        model:
          string: LATEST-GPU-MODEL
        uuid:
          string: gpu-18db0e85-99e9-c746-8531-ffeb86328b39
      bindingConditions:
        - BindingConditions
      bindingFailureConditions:
        - BindingFailureConditions
      capacity:
        memory:
          value: 80Gi
      name: gpu-0
  driver: cpu.example.com
  nodeName: dra-example-driver-cluster-worker
  pool:
    generation: 1
    name: dra-example-driver-cluster-worker
    resourceSliceCount: 1
---
apiVersion: resource.k8s.io/v1
kind: ResourceSlice
metadata:
  creationTimestamp: "2026-05-19T08:15:27Z"
  generateName: 00000-gpu.example.com-dra-example-driver-cluster-worker-
  generation: 2
  name: 00000-gpu.example.com-dra-example-driver-cluster-worker-srjp9
  ownerReferences:
    - apiVersion: v1
      controller: true
      kind: Node
      name: dra-example-driver-cluster-worker
      uid: 46122907-4eaa-4e45-857c-fcad187eaeb6
  resourceVersion: "963"
  uid: 18ce8be4-b9b7-4878-b997-866a5dc5ba22
spec:
  devices:
    - attributes:
        driverVersion:
          version: 1.0.0
        index:
          int: 0
        model:
          string: LATEST-GPU-MODEL
        uuid:
          string: gpu-18db0e85-99e9-c746-8531-ffeb86328b39
      bindingConditions:
        - BindingConditions
      bindingFailureConditions:
        - BindingFailureConditions
      capacity:
        memory:
          value: 80Gi
      name: gpu-0
  driver: gpu.example.com
  nodeName: dra-example-driver-cluster-worker
  pool:
    generation: 1
    name: dra-example-driver-cluster-worker
    resourceSliceCount: 1
```

</details>

## Usage

### 1. Deploy the controller

```bash
kubectl apply -f deploy/daemonset.yaml
```

### 2. Create a ResourceClaimTemplate with a prioritized list

Use `firstAvailable` to specify a prioritized list of subrequests. The scheduler will try them in order and pick the first one that can be satisfied. Each subrequest references a different DeviceClass.

The `config` section uses the `<main request>/<subrequest>` format to apply `ImageConfig` to the specific subrequest that gets chosen.

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: gpu-or-cpu
spec:
  spec:
    devices:
      requests:
        - name: device
          firstAvailable:
            - name: gpu
              deviceClassName: gpu.example.com
            - name: cpu
              deviceClassName: cpu.example.com
      config:
        - requests: ["device/gpu"]
          opaque:
            driver: image.example.com
            parameters:
              apiVersion: image.example.com/v1alpha1
              kind: ImageConfig
              containerName: app
              image: fedora:latest
        - requests: ["device/cpu"]
          opaque:
            driver: image.example.com
            parameters:
              apiVersion: image.example.com/v1alpha1
              kind: ImageConfig
              containerName: app
              image: ubuntu:latest
```

### 3. Create a Pod referencing the claim

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: my-app-1
spec:
  terminationGracePeriodSeconds: 0
  containers:
    - name: app
      image: busybox:latest  # will be overwritten by the controller
      command: ["sleep", "infinity"]
      resources:
        claims:
          - name: device
  resourceClaims:
    - name: device
      resourceClaimTemplateName: gpu-or-cpu
```

### 4. Verify the behavior

This scenario demonstrates the prioritized allocation and image patching:

- The first Pod (`my-app-1`) is allocated a GPU device because `gpu.example.com` has the highest priority in `firstAvailable`. The controller reads the matching `ImageConfig` and patches the container image from `busybox:latest` to `fedora:latest`.
- The second Pod (`my-app-2`) cannot get a GPU because it is already occupied by `my-app-1`. The scheduler falls back to `cpu.example.com` via the prioritized list, and the controller patches the container image from `busybox:latest` to `ubuntu:latest`.

**Step 1: Create the first Pod**

```bash
$ kubectl apply -f pod-1.yaml
pod/my-app-1 created
```

**Step 2: Verify the first Pod is allocated the GPU**

The scheduler picks `gpu.example.com` (highest priority in `firstAvailable`):

```bash
$ kubectl get resourceclaim my-app-1-device-r8m2x -o jsonpath='{.status}' | jq .
```

<details>
<summary>Output</summary>

```json
{
  "allocation": {
    "allocationTimestamp": "2026-05-19T08:44:31Z",
    "devices": {
      "config": [
        {
          "opaque": {
            "driver": "image.example.com",
            "parameters": {
              "apiVersion": "image.example.com/v1alpha1",
              "containerName": "app",
              "image": "fedora:latest",
              "kind": "ImageConfig"
            }
          },
          "requests": ["device/gpu"],
          "source": "FromClaim"
        }
      ],
      "results": [
        {
          "bindingConditions": ["BindingConditions"],
          "bindingFailureConditions": ["BindingFailureConditions"],
          "device": "gpu-0",
          "driver": "gpu.example.com",
          "pool": "dra-example-driver-cluster-worker",
          "request": "device/gpu"
        }
      ]
    },
    "nodeSelector": {
      "nodeSelectorTerms": [
        {
          "matchFields": [
            {
              "key": "metadata.name",
              "operator": "In",
              "values": ["dra-example-driver-cluster-worker"]
            }
          ]
        }
      ]
    }
  },
  "devices": [
    {
      "conditions": [
        {
          "lastTransitionTime": "2026-05-19T08:44:31Z",
          "message": "Container image has been updated",
          "reason": "ImagePatched",
          "status": "True",
          "type": "BindingConditions"
        }
      ],
      "device": "gpu-0",
      "driver": "gpu.example.com",
      "pool": "dra-example-driver-cluster-worker"
    }
  ],
  "reservedFor": [
    {
      "name": "my-app-1",
      "resource": "pods",
      "uid": "8a418bfc-1d97-44df-acc7-2c0fed6865b1"
    }
  ]
}
```

</details>

**Step 3: Confirm the first Pod's image is patched**

```bash
$ kubectl get pod my-app-1 -o jsonpath='{.spec.containers[0].image}'
fedora:latest
```

**Step 4: Confirm the first Pod is running**

```bash
$ kubectl get pod my-app-1
NAME       READY   STATUS    RESTARTS   AGE
my-app-1   1/1     Running   0          30s
```

**Step 5: Create the second Pod**

```bash
$ kubectl apply -f pod-2.yaml
pod/my-app-2 created
```

**Step 6: Verify the second Pod falls back to the CPU**

With the single GPU device already consumed by `my-app-1`, the scheduler falls back to `cpu.example.com`:

```bash
$ kubectl get resourceclaim my-app-2-device-4knq7 -o jsonpath='{.status}' | jq .
```

<details>
<summary>Output</summary>

```json
{
  "allocation": {
    "allocationTimestamp": "2026-05-19T08:44:35Z",
    "devices": {
      "config": [
        {
          "opaque": {
            "driver": "image.example.com",
            "parameters": {
              "apiVersion": "image.example.com/v1alpha1",
              "containerName": "app",
              "image": "ubuntu:latest",
              "kind": "ImageConfig"
            }
          },
          "requests": ["device/cpu"],
          "source": "FromClaim"
        }
      ],
      "results": [
        {
          "bindingConditions": ["BindingConditions"],
          "bindingFailureConditions": ["BindingFailureConditions"],
          "device": "gpu-0",
          "driver": "cpu.example.com",
          "pool": "dra-example-driver-cluster-worker",
          "request": "device/cpu"
        }
      ]
    },
    "nodeSelector": {
      "nodeSelectorTerms": [
        {
          "matchFields": [
            {
              "key": "metadata.name",
              "operator": "In",
              "values": ["dra-example-driver-cluster-worker"]
            }
          ]
        }
      ]
    }
  },
  "devices": [
    {
      "conditions": [
        {
          "lastTransitionTime": "2026-05-19T08:44:35Z",
          "message": "Container image has been updated",
          "reason": "ImagePatched",
          "status": "True",
          "type": "BindingConditions"
        }
      ],
      "device": "gpu-0",
      "driver": "cpu.example.com",
      "pool": "dra-example-driver-cluster-worker"
    }
  ],
  "reservedFor": [
    {
      "name": "my-app-2",
      "resource": "pods",
      "uid": "b2c53e91-7a12-4f8e-9d34-1a5f6c8e2b47"
    }
  ]
}
```

</details>

**Step 7: Confirm the second Pod's image is patched**

```bash
$ kubectl get pod my-app-2 -o jsonpath='{.spec.containers[0].image}'
ubuntu:latest
```

**Step 8: Confirm the second Pod is running**

```bash
$ kubectl get pod my-app-2
NAME       READY   STATUS    RESTARTS   AGE
my-app-2   1/1     Running   0          25s
```

## ImageConfig Schema

The opaque parameters must conform to:

```json
{
  "apiVersion": "image.example.com/v1alpha1",
  "kind": "ImageConfig",
  "containerName": "<name of the container in the Pod spec>",
  "image": "<desired container image>"
}
```

## License

See [LICENSE](LICENSE).
