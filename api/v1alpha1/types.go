package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ImageConfig specifies a container image override delivered via
// opaque device allocation config in a ResourceClaim.
//
// Example JSON:
//
//	{ "apiVersion": "image.example.com/v1alpha1",
//	  "kind": "ImageConfig",
//	  "containerName": "app",
//	  "image": "nginx:1.27" }
type ImageConfig struct {
	metav1.TypeMeta `json:",inline"`
	ContainerName   string `json:"containerName"`
	Image           string `json:"image"`
}
