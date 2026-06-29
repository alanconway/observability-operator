package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/yaml"
	kyaml "sigs.k8s.io/yaml"

	obsv1alpha1 "github.com/rhobs/observability-operator/pkg/apis/observability/v1alpha1"
	uiv1alpha1 "github.com/rhobs/observability-operator/pkg/apis/uiplugin/v1alpha1"
	"github.com/rhobs/observability-operator/pkg/controllers/observability"
	"github.com/rhobs/observability-operator/pkg/controllers/uiplugin"
)

var defaultImages = map[string]string{
	"ui-dashboards":                "quay.io/openshift-observability-ui/console-dashboards-plugin:v0.4.3",
	"ui-troubleshooting-panel-pf6": "quay.io/openshift-observability-ui/troubleshooting-panel-console-plugin:v0.4.5",
	"ui-troubleshooting-panel":     "quay.io/openshift-observability-ui/troubleshooting-panel-console-plugin:v1.0.0",
	"ui-distributed-tracing-pf4":   "quay.io/openshift-observability-ui/distributed-tracing-console-plugin:v0.3.3",
	"ui-distributed-tracing-pf5":   "quay.io/openshift-observability-ui/distributed-tracing-console-plugin:v0.4.3",
	"ui-distributed-tracing-pf6":   "quay.io/openshift-observability-ui/distributed-tracing-console-plugin:v1.0.3",
	"ui-distributed-tracing":       "quay.io/openshift-observability-ui/distributed-tracing-console-plugin:v1.1.0",
	"ui-logging-pf4":               "quay.io/openshift-observability-ui/logging-view-plugin:v6.0.5",
	"ui-logging-pf5":               "quay.io/openshift-observability-ui/logging-view-plugin:v6.1.6",
	"ui-logging":                   "quay.io/openshift-observability-ui/logging-view-plugin:v6.2.1",
	"korrel8r":                     "quay.io/korrel8r/korrel8r:0.11.1",
	"health-analyzer":              "quay.io/openshiftanalytics/cluster-health-analyzer:v1.1.1",
	"ui-monitoring-pf5":            "quay.io/openshift-observability-ui/monitoring-console-plugin:v0.4.5",
	"ui-monitoring-pf6":            "quay.io/openshift-observability-ui/monitoring-console-plugin:v0.5.4",
	"ui-monitoring":                "quay.io/openshift-observability-ui/monitoring-console-plugin:v1.0.0",
	"perses":                       "quay.io/openshift-observability-ui/perses:v0.54.0",
}

func errExit(err error, format string, args ...any) {
	if err != nil {
		fmt.Fprintf(os.Stderr, format, args...)
		fmt.Fprintln(os.Stderr)
		os.Exit(1)
	}
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `
Usage: %v [OPTIONS] [RESOURCE_FILE]...

Reads each RESOURCE_FILE as a stream of YAML documents.
If a resource file argument is "-", then also read from stdin.
Input resources should belong to the Cluster Observability Operator (ObservabilityInstaller, UIPlugin etc.)

Reconciles the input resources and writes the resulting resources as a YAML stream to stdout.
`, filepath.Base(os.Args[0]))
		flag.PrintDefaults()
	}
	clusterVersion := flag.String("cluster-version", "4.22", "OpenShift cluster version for compatibility matrix")
	namespace := flag.String("namespace", "openshift-cluster-observability-operator", "Namespace for UIPlugin resources")
	configDir := flag.String("config", "", "Root directory for the configuration tree. The generated overlay is written to CONFIG/overlays/generated")
	otelCSV := flag.String("opentelemetry-csv", "", "OpenTelemetry Operator starting CSV name. Empty string means the latest version will be installed.")
	tempoCSV := flag.String("tempo-csv", "", "Tempo Operator starting CSV name. Empty string means the latest version will be installed.")
	flag.Parse()

	yamlData, err := readInputs(flag.Args())
	errExit(err, "error: %v", err)

	scheme := runtime.NewScheme()
	err = obsv1alpha1.AddToScheme(scheme)
	errExit(err, "error adding observability scheme: %v", err)
	err = uiv1alpha1.AddToScheme(scheme)
	errExit(err, "error adding uiplugin scheme: %v", err)

	installer, plugins, err := decodeResources(scheme, yamlData)
	errExit(err, "error: %v", err)

	cfg := observability.OverlayConfig{
		OpenTelemetryOperator: observability.OperatorInstallConfig{
			StartingCSV: *otelCSV,
		},
		TempoOperator: observability.OperatorInstallConfig{
			StartingCSV: *tempoCSV,
		},
	}

	overlay, err := observability.BuildOverlay(installer, cfg)
	errExit(err, "error: %v", err)

	pluginConf := uiplugin.UIPluginBuildConfig{
		Images:         defaultImages,
		Namespace:      *namespace,
		ClusterVersion: *clusterVersion,
	}
	resolveUIPlugins(overlay, plugins, pluginConf)

	if *configDir != "" {
		err = os.MkdirAll(*configDir, 0o755)
		errExit(err, "error creating config directory %s: %v", *configDir, err)
		err = overlay.WriteToDir(*configDir)
		errExit(err, "error writing config to %s: %v", *configDir, err)
		return
	}

	var out bytes.Buffer

	yamlOut, err := overlay.BuildYAML()
	errExit(err, "error building overlay: %v", err)
	out.Write(yamlOut)

	writeSecretPlaceholders(&out, installer)

	os.Stdout.Write(out.Bytes())
}

func writeSecretPlaceholders(out *bytes.Buffer, installer *obsv1alpha1.ObservabilityInstaller) {
	if installer.Spec.Capabilities == nil {
		return
	}
	if tracing := installer.Spec.Capabilities.Tracing; tracing != nil && tracing.Enabled {
		name := fmt.Sprintf("coo-%s-tempo", installer.Name)
		fmt.Fprintf(os.Stderr, "WARNING: Secret %q must be created with object storage credentials before applying\n", name)
		if out.Len() > 0 {
			out.WriteString("---\n")
		}
		fmt.Fprintf(out, `apiVersion: v1
kind: Secret
metadata:
  name: %s
  annotations:
    observability.openshift.io/placeholder: "true"
  # PLACEHOLDER: replace with real object storage credentials
data: {}
`, name)

		if oss := tracing.GetStorage().GetObjectStorageSpec(); oss != nil {
			if tls := oss.GetTLS(); tls != nil {
				if tls.CertSecret != nil {
					certName := fmt.Sprintf("coo-%s-tempo-storage-cert", installer.Name)
					fmt.Fprintf(os.Stderr, "WARNING: Secret %q must be created with TLS certificate before applying\n", certName)
					out.WriteString("---\n")
					fmt.Fprintf(out, `apiVersion: v1
kind: Secret
metadata:
  name: %s
  annotations:
    observability.openshift.io/placeholder: "true"
  # PLACEHOLDER: replace with TLS certificate and key
data: {}
`, certName)
				}
				if tls.CAConfigMap != nil {
					caName := fmt.Sprintf("coo-%s-tempo-storage-ca", installer.Name)
					fmt.Fprintf(os.Stderr, "WARNING: ConfigMap %q must be created with CA certificate before applying\n", caName)
					out.WriteString("---\n")
					fmt.Fprintf(out, `apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  annotations:
    observability.openshift.io/placeholder: "true"
  # PLACEHOLDER: replace with CA certificate
data: {}
`, caName)
				}
			}
		}
	}
}

// readInputs reads YAML data from the given files, or from stdin if none are provided.
func readInputs(files []string) ([]byte, error) {
	var buf bytes.Buffer
	for _, f := range files {
		var (
			data []byte
			err  error
		)
		if f == "-" {
			data, err = io.ReadAll(os.Stdin)
		} else {
			data, err = os.ReadFile(f)
		}
		if err != nil {
			return nil, err
		}
		if buf.Len() > 0 {
			buf.WriteString("\n---\n")
		}
		buf.Write(data)
	}
	return buf.Bytes(), nil
}

// resolveUIPlugins builds the overlay to discover UIPlugin CRs, resolves them
// (along with any input UIPlugins) into actual resources (Deployment, Service, etc.),
// removes the UIPlugin components from the overlay, and adds the resolved resources.
func resolveUIPlugins(overlay *observability.Overlay, inputPlugins []*uiv1alpha1.UIPlugin, conf uiplugin.UIPluginBuildConfig) {
	objects, err := overlay.Build()
	errExit(err, "error building overlay: %v", err)

	var generatedPlugins []*uiv1alpha1.UIPlugin
	for _, obj := range objects {
		gvk := obj.GetObjectKind().GroupVersionKind()
		if gvk.Group == "observability.openshift.io" && gvk.Kind == "UIPlugin" {
			plugin := &uiv1alpha1.UIPlugin{}
			u := obj.(*unstructured.Unstructured)
			err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, plugin)
			errExit(err, "error converting UIPlugin %s: %v", obj.GetName(), err)
			generatedPlugins = append(generatedPlugins, plugin)
		}
	}

	allPlugins := append(generatedPlugins, inputPlugins...)
	if len(allPlugins) == 0 {
		return
	}

	// Remove components that generate UIPlugin CRs; the generator resolves them directly.
	overlay.RemoveComponent("../../components/console/tracing")

	for _, plugin := range allPlugins {
		pluginOverlay, _, err := uiplugin.BuildUIPluginOverlay(plugin, conf, logr.Discard())
		errExit(err, "error building UIPlugin %s overlay: %v", plugin.Name, err)

		pluginObjects, err := pluginOverlay.Build()
		errExit(err, "error building UIPlugin %s: %v", plugin.Name, err)

		for _, obj := range pluginObjects {
			data, err := kyaml.Marshal(obj)
			errExit(err, "error marshaling UIPlugin %s resource: %v", plugin.Name, err)
			gvk := obj.GetObjectKind().GroupVersionKind()
			name := fmt.Sprintf("uiplugin-%s-%s-%s.yaml", plugin.Name, gvk.Kind, obj.GetName())
			overlay.AddResource(name, data)
		}
	}
}

// decodeResources splits a multi-document YAML stream, decodes each document,
// and returns the ObservabilityInstaller and UIPlugins found.
func decodeResources(scheme *runtime.Scheme, data []byte) (*obsv1alpha1.ObservabilityInstaller, []*uiv1alpha1.UIPlugin, error) {
	decode := serializer.NewCodecFactory(scheme).UniversalDeserializer().Decode
	reader := yaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))

	var installer *obsv1alpha1.ObservabilityInstaller
	var plugins []*uiv1alpha1.UIPlugin

	for {
		doc, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("reading YAML document: %w", err)
		}
		doc = bytes.TrimSpace(doc)
		if len(doc) == 0 {
			continue
		}

		obj, _, err := decode(doc, nil, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("decoding YAML document: %w", err)
		}

		switch o := obj.(type) {
		case *obsv1alpha1.ObservabilityInstaller:
			if installer != nil {
				return nil, nil, fmt.Errorf("multiple ObservabilityInstaller resources found")
			}
			installer = o
		case *uiv1alpha1.UIPlugin:
			plugins = append(plugins, o)
		}
	}
	if installer == nil {
		installer = &obsv1alpha1.ObservabilityInstaller{}
	}
	return installer, plugins, nil
}
