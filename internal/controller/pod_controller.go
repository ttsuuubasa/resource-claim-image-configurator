package controller

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	imagev1alpha1 "github.com/ttsuuubasa/resource-claim-image-configurator/api/v1alpha1"
)

const bindingConditionValidateImage = "BindingConditions"

// PodReconciler watches Pods nominated to this node and patches their
// container images based on the associated ResourceClaim config.
type PodReconciler struct {
	Client   client.Client
	NodeName string
}

// SetupWithManager registers the controller with the manager.
func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Complete(r)
}

// devicePatch pairs an allocation result with the ImageConfig resolved from it.
type devicePatch struct {
	Result resourceapi.DeviceRequestAllocationResult
	Config imagev1alpha1.ImageConfig
}

// claimPatch holds a ResourceClaim and the per-device patches to apply.
type claimPatch struct {
	Claim   *resourceapi.ResourceClaim
	Devices []devicePatch
}

// Reconcile handles a single Pod event.
func (r *PodReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	var pod corev1.Pod
	if err := r.Client.Get(ctx, req.NamespacedName, &pod); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	patches, err := r.parseImageConfigs(ctx, &pod)
	if err != nil {
		return reconcile.Result{}, err
	}
	if len(patches) == 0 {
		return reconcile.Result{}, nil
	}

	if err := r.patchImages(ctx, &pod, patches); err != nil {
		return reconcile.Result{}, err
	}
	log.Info("image patched", "pod", req.NamespacedName)

	// Mark binding conditions as satisfied.
	for _, p := range patches {
		if err := r.setBindingConditions(ctx, p); err != nil {
			return reconcile.Result{}, err
		}
	}

	return reconcile.Result{}, nil
}

// parseImageConfigs fetches ResourceClaims referenced by the pod and returns
// claimPatch entries containing the configs to apply and the results whose
// binding condition needs to be satisfied afterward.
func (r *PodReconciler) parseImageConfigs(ctx context.Context, pod *corev1.Pod) ([]claimPatch, error) {
	decoder := imagev1alpha1.Codec.UniversalDeserializer()

	var patches []claimPatch

	for _, rcs := range pod.Status.ResourceClaimStatuses {
		claimKey := types.NamespacedName{
			Namespace: pod.Namespace,
			Name:      *rcs.ResourceClaimName,
		}
		var claim resourceapi.ResourceClaim
		if err := r.Client.Get(ctx, claimKey, &claim); err != nil {
			return nil, fmt.Errorf("get claim %s: %w", claimKey, err)
		}
		if claim.Status.Allocation == nil {
			return nil, fmt.Errorf("claim %s not yet allocated", claimKey)
		}

		// Find requests whose "validate-image" condition is still pending.
		var pendingResults []resourceapi.DeviceRequestAllocationResult
		for _, result := range claim.Status.Allocation.Devices.Results {
			if !slices.Contains(result.BindingConditions, bindingConditionValidateImage) {
				continue
			}
			if isBindingConditionAlreadySet(&claim, &result, bindingConditionValidateImage) {
				continue
			}
			pendingResults = append(pendingResults, result)
		}
		if len(pendingResults) == 0 {
			continue
		}

		// For each pending result, find the config targeting it.
		var devices []devicePatch
		for _, result := range pendingResults {
			for _, cfg := range claim.Status.Allocation.Devices.Config {
				if cfg.Opaque == nil || cfg.Opaque.Parameters.Raw == nil {
					continue
				}
				if len(cfg.Requests) > 0 && !slices.Contains(cfg.Requests, result.Request) {
					continue
				}
				obj, _, err := decoder.Decode(cfg.Opaque.Parameters.Raw, nil, nil)
				if err != nil {
					continue
				}
				ic, ok := obj.(*imagev1alpha1.ImageConfig)
				if !ok || ic.ContainerName == "" || ic.Image == "" {
					continue
				}
				devices = append(devices, devicePatch{Result: result, Config: *ic})
			}
		}

		if len(devices) > 0 {
			patches = append(patches, claimPatch{
				Claim:   &claim,
				Devices: devices,
			})
		}
	}

	return patches, nil
}

// isBindingConditionAlreadySet checks whether the given condition is already
// set to True in Status.Devices for the device identified by the allocation result.
func isBindingConditionAlreadySet(claim *resourceapi.ResourceClaim, result *resourceapi.DeviceRequestAllocationResult, condition string) bool {
	for _, ds := range claim.Status.Devices {
		if ds.Driver != result.Driver || ds.Pool != result.Pool || ds.Device != result.Device {
			continue
		}
		for _, c := range ds.Conditions {
			if c.Type == condition && c.Status == metav1.ConditionTrue {
				return true
			}
		}
	}
	return false
}

// patchImages updates container images on the pod according to the provided claimPatches.
func (r *PodReconciler) patchImages(ctx context.Context, pod *corev1.Pod, patches []claimPatch) error {
	for i := range pod.Spec.Containers {
		for _, p := range patches {
			for _, d := range p.Devices {
				if pod.Spec.Containers[i].Name == d.Config.ContainerName {
					pod.Spec.Containers[i].Image = d.Config.Image
				}
			}
		}
	}
	if err := r.Client.Update(ctx, pod); err != nil {
		return fmt.Errorf("update pod %s/%s: %w", pod.Namespace, pod.Name, err)
	}
	return nil
}

// setBindingConditions sets the "validate-image" binding condition to True
// on the claim's Status.Devices for each device in the patch.
func (r *PodReconciler) setBindingConditions(ctx context.Context, cp claimPatch) error {
	now := metav1.Now()
	for _, d := range cp.Devices {
		cp.Claim.Status.Devices = append(cp.Claim.Status.Devices, resourceapi.AllocatedDeviceStatus{
			Driver: d.Result.Driver,
			Pool:   d.Result.Pool,
			Device: d.Result.Device,
			Conditions: []metav1.Condition{{
				Type:               bindingConditionValidateImage,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: now,
				Reason:             "ImagePatched",
				Message:            "Container image has been updated",
			}},
		})
	}
	if err := r.Client.Status().Update(ctx, cp.Claim); err != nil {
		return fmt.Errorf("update claim status %s/%s: %w", cp.Claim.Namespace, cp.Claim.Name, err)
	}
	return nil
}
