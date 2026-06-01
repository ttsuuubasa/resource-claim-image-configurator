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

	imagev1alpha1 "github.com/ttsuuubasa/dra-driver-image-configurator/api/v1alpha1"
)

const BindingConditionValidateImage = "image-verified"
const BindingFailuerConditionPrepareImage = "image-prepare-failed"

// PodReconciler watches Pods nominated to this node and patches their
// container images based on the associated ResourceClaim config.
type PodReconciler struct {
	Client client.Client
}

// SetupWithManager registers the controller with the manager.
func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Complete(r)
}

// claimBindingResult pairs a ResourceClaim with its pending binding results.
type claimBindingResult struct {
	Claim   *resourceapi.ResourceClaim
	Results []resourceapi.DeviceRequestAllocationResult
}

// Reconcile handles a single Pod event.
func (r *PodReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	var pod corev1.Pod
	if err := r.Client.Get(ctx, req.NamespacedName, &pod); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	claims, err := r.fetchClaims(ctx, &pod)
	if err != nil {
		return reconcile.Result{}, err
	}

	bindingResults := collectPendingBindingResults(claims)
	if len(bindingResults) == 0 {
		return reconcile.Result{}, nil
	}

	imageConfigs := collectImageConfigs(claims)
	if len(imageConfigs) == 0 {
		return reconcile.Result{}, nil
	}

	if err := r.patchImages(ctx, &pod, imageConfigs); err != nil {
		return reconcile.Result{}, err
	}
	log.Info("image patched", "pod", req.NamespacedName)

	for _, cbr := range bindingResults {
		if err := r.setBindingCondition(ctx, cbr); err != nil {
			return reconcile.Result{}, err
		}
	}

	return reconcile.Result{}, nil
}

// fetchClaims returns all ResourceClaims referenced by the pod.
func (r *PodReconciler) fetchClaims(ctx context.Context, pod *corev1.Pod) ([]*resourceapi.ResourceClaim, error) {
	var claims []*resourceapi.ResourceClaim
	for _, rcs := range pod.Status.ResourceClaimStatuses {
		claimKey := types.NamespacedName{
			Namespace: pod.Namespace,
			Name:      *rcs.ResourceClaimName,
		}
		claim := &resourceapi.ResourceClaim{}
		if err := r.Client.Get(ctx, claimKey, claim); err != nil {
			return nil, fmt.Errorf("get claim %s: %w", claimKey, err)
		}
		if claim.Status.Allocation == nil {
			return nil, fmt.Errorf("claim %s not yet allocated", claimKey)
		}
		claims = append(claims, claim)
	}
	return claims, nil
}

// collectPendingBindingResults returns per-claim pending binding results where the
// "image-verified" binding condition is required but not yet satisfied.
func collectPendingBindingResults(claims []*resourceapi.ResourceClaim) []claimBindingResult {
	var pending []claimBindingResult
	for _, claim := range claims {
		var results []resourceapi.DeviceRequestAllocationResult
		for _, result := range claim.Status.Allocation.Devices.Results {
			if !slices.Contains(result.BindingConditions, BindingConditionValidateImage) {
				continue
			}
			if isBindingConditionAlreadySet(claim, &result, BindingConditionValidateImage) {
				continue
			}
			results = append(results, result)
		}
		if len(results) > 0 {
			pending = append(pending, claimBindingResult{Claim: claim, Results: results})
		}
	}
	return pending
}

// collectImageConfigs extracts all ImageConfig objects from the allocated device
// configs across all claims.
func collectImageConfigs(claims []*resourceapi.ResourceClaim) []*imagev1alpha1.ImageConfig {
	decoder := imagev1alpha1.Codec.UniversalDeserializer()
	var imageConfigs []*imagev1alpha1.ImageConfig
	for _, claim := range claims {
		for _, cfg := range claim.Status.Allocation.Devices.Config {
			if cfg.Opaque == nil || cfg.Opaque.Parameters.Raw == nil {
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
			imageConfigs = append(imageConfigs, ic)
		}
	}
	return imageConfigs
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

// patchImages updates container images on the pod according to the provided ImageConfigs.
func (r *PodReconciler) patchImages(ctx context.Context, pod *corev1.Pod, imageConfigs []*imagev1alpha1.ImageConfig) error {
	for i := range pod.Spec.Containers {
		for _, ic := range imageConfigs {
			if pod.Spec.Containers[i].Name == ic.ContainerName {
				pod.Spec.Containers[i].Image = ic.Image
			}
		}
	}
	if err := r.Client.Update(ctx, pod); err != nil {
		return fmt.Errorf("update pod %s/%s: %w", pod.Namespace, pod.Name, err)
	}
	return nil
}

// setBindingCondition sets the "image-verified" binding condition to True
// on the claim's Status.Devices for all pending binding results in a single update.
func (r *PodReconciler) setBindingCondition(ctx context.Context, cbr claimBindingResult) error {
	now := metav1.Now()
	for _, result := range cbr.Results {
		cbr.Claim.Status.Devices = append(cbr.Claim.Status.Devices, resourceapi.AllocatedDeviceStatus{
			Driver:  result.Driver,
			Pool:    result.Pool,
			Device:  result.Device,
			ShareID: (*string)(result.ShareID),
			Conditions: []metav1.Condition{{
				Type:               BindingConditionValidateImage,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: now,
				Reason:             "ImagePatched",
				Message:            "Container image has been updated",
			}},
		})
	}
	if err := r.Client.Status().Update(ctx, cbr.Claim); err != nil {
		return fmt.Errorf("update claim status %s/%s: %w", cbr.Claim.Namespace, cbr.Claim.Name, err)
	}
	return nil
}
