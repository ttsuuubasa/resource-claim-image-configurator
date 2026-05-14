package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

var (
	SchemeGroupVersion = schema.GroupVersion{Group: "image.example.com", Version: "v1alpha1"}
	SchemeBuilder      = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme        = SchemeBuilder.AddToScheme

	scheme = runtime.NewScheme()
	Codec  = serializer.NewCodecFactory(scheme)
)

func init() {
	AddToScheme(scheme)
}

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(SchemeGroupVersion, &ImageConfig{})
	return nil
}

func (in *ImageConfig) DeepCopyObject() runtime.Object {
	return &ImageConfig{
		TypeMeta:      in.TypeMeta,
		ContainerName: in.ContainerName,
		Image:         in.Image,
	}
}
