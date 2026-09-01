// Package manifest renders Service, EndpointSlice, and Endpoints objects
// as a single multi-document YAML stream for `kubectl apply -f -`,
// stamping the TypeMeta the live controller's typed client doesn't need.
package manifest

import (
	"bytes"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// Render marshals svcs, slices, and (optionally empty) eps into one
// multi-document YAML stream, ordered by kind then name, so re-running
// against an unchanged cluster produces a byte-stable diff. Sorts and
// stamps TypeMeta on its inputs in place -- pass freshly built slices.
func Render(svcs []corev1.Service, slices []discoveryv1.EndpointSlice, eps []corev1.Endpoints) ([]byte, error) { //nolint:staticcheck // SA1019: intentional legacy support for Kubernetes <1.33
	sort.Slice(svcs, func(i, j int) bool { return svcs[i].Name < svcs[j].Name })
	sort.Slice(slices, func(i, j int) bool { return slices[i].Name < slices[j].Name })
	sort.Slice(eps, func(i, j int) bool { return eps[i].Name < eps[j].Name }) //nolint:staticcheck

	var buf bytes.Buffer
	for i := range svcs {
		svcs[i].TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}
		if err := appendDoc(&buf, &svcs[i]); err != nil {
			return nil, fmt.Errorf("rendering Service %s: %w", svcs[i].Name, err)
		}
	}
	for i := range slices {
		slices[i].TypeMeta = metav1.TypeMeta{APIVersion: "discovery.k8s.io/v1", Kind: "EndpointSlice"}
		if err := appendDoc(&buf, &slices[i]); err != nil {
			return nil, fmt.Errorf("rendering EndpointSlice %s: %w", slices[i].Name, err)
		}
	}
	for i := range eps { //nolint:staticcheck
		eps[i].TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Endpoints"} //nolint:staticcheck
		if err := appendDoc(&buf, &eps[i]); err != nil {
			return nil, fmt.Errorf("rendering Endpoints %s: %w", eps[i].Name, err)
		}
	}
	return buf.Bytes(), nil
}

func appendDoc(buf *bytes.Buffer, obj any) error {
	doc, err := yaml.Marshal(obj)
	if err != nil {
		return err
	}
	if buf.Len() > 0 {
		buf.WriteString("---\n")
	}
	buf.Write(doc)
	return nil
}
