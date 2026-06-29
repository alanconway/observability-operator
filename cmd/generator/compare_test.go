package main

import (
	"bytes"
	"fmt"
	"sort"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"

	obsv1alpha1 "github.com/rhobs/observability-operator/pkg/apis/observability/v1alpha1"
	uiv1alpha1 "github.com/rhobs/observability-operator/pkg/apis/uiplugin/v1alpha1"
	"github.com/rhobs/observability-operator/pkg/controllers/observability"
)

// TestNewGeneratorMatchesOldBehavior verifies that the new kustomize-based generator
// produces the same output as the old controller-based approach would have.
func TestNewGeneratorMatchesOldBehavior(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		wantKinds map[string]int // expected count of each resource kind
		checkNamespaces bool
	}{
		{
			name: "tracing enabled",
			input: `
apiVersion: observability.openshift.io/v1alpha1
kind: ObservabilityInstaller
metadata:
  name: test-installer
  namespace: test-namespace
spec:
  capabilities:
    tracing:
      enabled: true
      storage:
        objectStorage:
          s3:
            bucket: tempo
            endpoint: http://minio:9000
            accessKeyID: tempo
            accessKeySecret:
              name: minio-secret
              key: access_key_secret
`,
			wantKinds: map[string]int{
				"Subscription":            2, // opentelemetry + tempo
				"OpenTelemetryCollector":  1,
				"TempoStack":              1,
				"ClusterRole":             3, // otel components + tempo writer + tempo reader
				"ClusterRoleBinding":      3,
				"UIPlugin":                1,
				// Note: Secret placeholders are written separately by writeSecretPlaceholders, not part of the overlay
			},
			checkNamespaces: true,
		},
		{
			name: "operators only",
			input: `
apiVersion: observability.openshift.io/v1alpha1
kind: ObservabilityInstaller
metadata:
  name: test-installer
  namespace: test-namespace
spec:
  capabilities:
    tracing:
      operators:
        install: true
`,
			wantKinds: map[string]int{
				"Subscription": 2, // opentelemetry + tempo
			},
			checkNamespaces: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Decode input
			scheme := runtime.NewScheme()
			_ = obsv1alpha1.AddToScheme(scheme)
			_ = uiv1alpha1.AddToScheme(scheme)
			installer, _, err := decodeResources(scheme, []byte(tc.input))
			if err != nil {
				t.Fatalf("failed to decode input: %v", err)
			}

			// Generate with new kustomize approach
			cfg := observability.OverlayConfig{}
			overlay, err := observability.BuildOverlay(installer, cfg)
			if err != nil {
				t.Fatalf("failed to build overlay: %v", err)
			}

			yamlOut, err := overlay.BuildYAML()
			if err != nil {
				t.Fatalf("failed to build YAML: %v", err)
			}

			// Parse generated resources
			resources, err := parseYAMLResources(yamlOut)
			if err != nil {
				t.Fatalf("failed to parse generated YAML: %v", err)
			}

			// Count resource kinds
			kindCounts := make(map[string]int)
			for _, r := range resources {
				kind := r.GetKind()
				kindCounts[kind]++
			}

			// Check expected kinds
			for kind, wantCount := range tc.wantKinds {
				gotCount := kindCounts[kind]
				if gotCount != wantCount {
					t.Errorf("kind %s: got %d resources, want %d", kind, gotCount, wantCount)
				}
			}

			// Report unexpected kinds
			for kind, count := range kindCounts {
				if _, expected := tc.wantKinds[kind]; !expected {
					t.Logf("unexpected kind %s: %d resources", kind, count)
				}
			}

			// Verify no Namespace or OperatorGroup resources
			if kindCounts["Namespace"] > 0 {
				t.Errorf("found %d Namespace resources, expected 0 (old code didn't create these)", kindCounts["Namespace"])
			}
			if kindCounts["OperatorGroup"] > 0 {
				t.Errorf("found %d OperatorGroup resources, expected 0 (old code didn't create these)", kindCounts["OperatorGroup"])
			}

			// Check namespace consistency
			if tc.checkNamespaces {
				checkNamespaceConsistency(t, resources, installer.Namespace)
			}
		})
	}
}

// TestSubscriptionNamespaces verifies that Subscription resources are created in the correct namespace
func TestSubscriptionNamespaces(t *testing.T) {
	testNamespaces := []string{
		"test-ns-1",
		"test-ns-2",
		"openshift-cluster-observability-operator",
	}

	for _, ns := range testNamespaces {
		t.Run(ns, func(t *testing.T) {
			input := fmt.Sprintf(`
apiVersion: observability.openshift.io/v1alpha1
kind: ObservabilityInstaller
metadata:
  name: test
  namespace: %s
spec:
  capabilities:
    tracing:
      enabled: true
      storage:
        objectStorage:
          s3:
            bucket: tempo
            endpoint: http://minio:9000
            accessKeyID: tempo
            accessKeySecret:
              name: minio-secret
              key: access_key_secret
`, ns)

			scheme := runtime.NewScheme()
			_ = obsv1alpha1.AddToScheme(scheme)
			_ = uiv1alpha1.AddToScheme(scheme)
			installer, _, err := decodeResources(scheme, []byte(input))
			if err != nil {
				t.Fatalf("failed to decode input: %v", err)
			}

			cfg := observability.OverlayConfig{}
			overlay, err := observability.BuildOverlay(installer, cfg)
			if err != nil {
				t.Fatalf("failed to build overlay: %v", err)
			}

			yamlOut, err := overlay.BuildYAML()
			if err != nil {
				t.Fatalf("failed to build YAML: %v", err)
			}

			resources, err := parseYAMLResources(yamlOut)
			if err != nil {
				t.Fatalf("failed to parse YAML: %v", err)
			}

			// Find all Subscriptions
			var subscriptions []unstructured.Unstructured
			for _, r := range resources {
				if r.GetKind() == "Subscription" {
					subscriptions = append(subscriptions, r)
				}
			}

			if len(subscriptions) == 0 {
				t.Fatal("no Subscription resources found")
			}

			// Verify all Subscriptions are in the correct namespace
			for _, sub := range subscriptions {
				gotNs := sub.GetNamespace()
				if gotNs != ns {
					t.Errorf("Subscription %s has namespace %q, want %q (old code put operators in same namespace as ObservabilityInstaller)",
						sub.GetName(), gotNs, ns)
				}
			}
		})
	}
}

// parseYAMLResources parses a multi-document YAML into a slice of unstructured objects
func parseYAMLResources(data []byte) ([]unstructured.Unstructured, error) {
	var resources []unstructured.Unstructured
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)

	for {
		var obj unstructured.Unstructured
		err := decoder.Decode(&obj)
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, fmt.Errorf("failed to decode YAML: %w", err)
		}
		if obj.GetKind() != "" {
			resources = append(resources, obj)
		}
	}

	return resources, nil
}

// checkNamespaceConsistency verifies that all namespaced resources use the expected namespace
func checkNamespaceConsistency(t *testing.T, resources []unstructured.Unstructured, expectedNs string) {
	t.Helper()

	namespaceIssues := make(map[string][]string)

	for _, r := range resources {
		kind := r.GetKind()
		name := r.GetName()
		ns := r.GetNamespace()

		// Skip cluster-scoped resources
		if isClusterScoped(kind) {
			// For ClusterRoleBindings, check the subject namespaces
			if kind == "ClusterRoleBinding" {
				subjects, found, err := unstructured.NestedSlice(r.Object, "subjects")
				if err == nil && found {
					for _, subj := range subjects {
						subjMap, ok := subj.(map[string]interface{})
						if !ok {
							continue
						}
						subjKind, _, _ := unstructured.NestedString(subjMap, "kind")
						if subjKind == "ServiceAccount" {
							subjNs, found, _ := unstructured.NestedString(subjMap, "namespace")
							if found && subjNs != expectedNs {
								namespaceIssues[kind] = append(namespaceIssues[kind],
									fmt.Sprintf("%s: ServiceAccount subject has namespace %q, want %q", name, subjNs, expectedNs))
							} else if !found {
								namespaceIssues[kind] = append(namespaceIssues[kind],
									fmt.Sprintf("%s: ServiceAccount subject missing namespace field", name))
							}
						}
					}
				}
			}
			continue
		}

		// Check namespaced resources
		if ns != expectedNs {
			namespaceIssues[kind] = append(namespaceIssues[kind],
				fmt.Sprintf("%s: has namespace %q, want %q", name, ns, expectedNs))
		}
	}

	// Report issues
	if len(namespaceIssues) > 0 {
		var kinds []string
		for k := range namespaceIssues {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)

		for _, kind := range kinds {
			for _, issue := range namespaceIssues[kind] {
				t.Errorf("%s: %s", kind, issue)
			}
		}
	}
}

// isClusterScoped returns true if the resource kind is cluster-scoped
func isClusterScoped(kind string) bool {
	clusterScoped := map[string]bool{
		"ClusterRole":        true,
		"ClusterRoleBinding": true,
		"CustomResourceDefinition": true,
	}
	return clusterScoped[kind]
}

// TestReportDifferences generates a detailed comparison report
func TestReportDifferences(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping comparison report in short mode")
	}

	input := `
apiVersion: observability.openshift.io/v1alpha1
kind: ObservabilityInstaller
metadata:
  name: test-installer
  namespace: openshift-cluster-observability-operator
spec:
  capabilities:
    tracing:
      enabled: true
      storage:
        objectStorage:
          s3:
            bucket: tempo
            endpoint: http://minio.minio.svc:9000
            accessKeyID: tempo
            accessKeySecret:
              name: minio-secret
              key: access_key_secret
`

	scheme := runtime.NewScheme()
	_ = obsv1alpha1.AddToScheme(scheme)
	_ = uiv1alpha1.AddToScheme(scheme)
	installer, _, err := decodeResources(scheme, []byte(input))
	if err != nil {
		t.Fatalf("failed to decode input: %v", err)
	}

	cfg := observability.OverlayConfig{}
	overlay, err := observability.BuildOverlay(installer, cfg)
	if err != nil {
		t.Fatalf("failed to build overlay: %v", err)
	}

	yamlOut, err := overlay.BuildYAML()
	if err != nil {
		t.Fatalf("failed to build YAML: %v", err)
	}

	resources, err := parseYAMLResources(yamlOut)
	if err != nil {
		t.Fatalf("failed to parse YAML: %v", err)
	}

	// Generate report
	t.Log("=== GENERATED RESOURCES REPORT ===")
	t.Logf("Total resources: %d", len(resources))
	t.Log("")

	kindCounts := make(map[string]int)
	kindExamples := make(map[string][]string)

	for _, r := range resources {
		kind := r.GetKind()
		name := r.GetName()
		ns := r.GetNamespace()

		kindCounts[kind]++
		example := fmt.Sprintf("  - %s", name)
		if ns != "" {
			example += fmt.Sprintf(" (namespace: %s)", ns)
		}
		if len(kindExamples[kind]) < 3 {
			kindExamples[kind] = append(kindExamples[kind], example)
		}
	}

	var kinds []string
	for k := range kindCounts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)

	t.Log("Resource counts by kind:")
	for _, kind := range kinds {
		t.Logf("  %s: %d", kind, kindCounts[kind])
		for _, example := range kindExamples[kind] {
			t.Log(example)
		}
	}

	t.Log("")
	t.Log("=== VALIDATION RESULTS ===")

	// Check for resources that old code didn't create
	issues := []string{}
	if kindCounts["Namespace"] > 0 {
		issues = append(issues, fmt.Sprintf("❌ Found %d Namespace resources (old code: 0)", kindCounts["Namespace"]))
	} else {
		t.Log("✅ No Namespace resources (matches old behavior)")
	}

	if kindCounts["OperatorGroup"] > 0 {
		issues = append(issues, fmt.Sprintf("❌ Found %d OperatorGroup resources (old code: 0)", kindCounts["OperatorGroup"]))
	} else {
		t.Log("✅ No OperatorGroup resources (matches old behavior)")
	}

	expectedNamespace := installer.Namespace
	allCorrectNamespace := true
	for _, r := range resources {
		if r.GetKind() == "Subscription" {
			if r.GetNamespace() != expectedNamespace {
				allCorrectNamespace = false
				issues = append(issues, fmt.Sprintf("❌ Subscription %s has namespace %q, expected %q",
					r.GetName(), r.GetNamespace(), expectedNamespace))
			}
		}
	}
	if allCorrectNamespace && kindCounts["Subscription"] > 0 {
		t.Logf("✅ All %d Subscriptions in correct namespace: %s", kindCounts["Subscription"], expectedNamespace)
	}

	if len(issues) > 0 {
		t.Log("")
		t.Log("ISSUES FOUND:")
		for _, issue := range issues {
			t.Log(issue)
		}
		t.Fail()
	} else {
		t.Log("")
		t.Log("✅ All validations passed - new code matches old behavior")
	}
}
