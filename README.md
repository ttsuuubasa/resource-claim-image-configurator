# resource-claim-image-configurator

Proof-of-concept controller demonstrating DRA Device Binding Conditions (KEP-5007). The controller observes ResourceClaims waiting on binding conditions and mutates the corresponding Pod's container image once the allocation decision is made, enabling runtime selection of GPU- or CPU-specific images.

## Overview

When a Pod requests a device via DRA (Dynamic Resource Allocation) with a prioritized list, the scheduler picks the best available device. This controller inspects the allocation result and patches the Pod's container image to match the allocated device — for example, switching between a CUDA image and a CPU-only image depending on which GPU (or no GPU) was allocated.

The flow:

1. User creates a `ResourceClaimTemplate` with a prioritized device request and opaque config (`ImageConfig`) that maps each request to a container image.
2. The scheduler allocates a device whose ResourceSlice declares `bindingConditions: ["BindingConditions"]`, blocking Pod startup until this controller acknowledges.
3. This controller detects the pending condition, reads the `ImageConfig` from the claim's allocation config, patches the Pod's container image, then marks the binding condition as satisfied.
4. The kubelet proceeds to start the Pod with the correct image.

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

```yaml
spec:
  devices:
    - name: gpu-0
      bindingConditions:
        - BindingConditions
      bindingFailureConditions:
        - BindingFailureConditions
      attributes:
        model:
          string: LATEST-GPU-MODEL
      capacity:
        memory:
          value: 80Gi
  driver: gpu.example.com
  nodeName: dra-example-driver-cluster-worker
```

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

### What happens

1. The scheduler evaluates the `firstAvailable` list in priority order. In this example, it first tries to allocate a device from the `gpu.example.com` DeviceClass. If no GPU is available, it falls back to `cpu.example.com`.
2. The allocated device's ResourceSlice declares `bindingConditions: ["BindingConditions"]`, so the kubelet holds the Pod — it will not start containers until all binding conditions are satisfied.
3. This controller watches Pods with `status.nominatedNodeName` set to its node. When it detects the Pod, it fetches the associated ResourceClaim and reads `status.allocation.devices.config`.
4. Based on which subrequest was chosen (e.g., `device/gpu`), the controller finds the matching `ImageConfig` in the opaque config and patches the Pod's container image (e.g., `busybox:latest` → `fedora:latest`).
5. The controller then writes a `BindingConditions: True` condition to the claim's `status.devices`, satisfying the binding condition.
6. The kubelet sees the condition is satisfied and starts the Pod with the patched image (`fedora:latest` for GPU, `ubuntu:latest` for CPU).

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
