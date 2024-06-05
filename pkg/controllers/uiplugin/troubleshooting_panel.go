package uiplugin

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	osv1alpha1 "github.com/openshift/api/console/v1alpha1"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	uiv1alpha1 "github.com/rhobs/observability-operator/pkg/apis/uiplugin/v1alpha1"
)

func createTroubleshootingPanelPluginInfo(plugin *uiv1alpha1.UIPlugin, namespace, name, image string, features []string) (*UIPluginInfo, error) {
	troubleshootingPanelConfig := plugin.Spec.TroubleshootingPanel

	configYaml, err := marshalTroubleshootingPanelPluginConfig(troubleshootingPanelConfig)
	if err != nil {
		return nil, fmt.Errorf("error creating plugin configuration file: %w", err)
	}

	extraArgs := []string{
		"-plugin-config-path=/etc/plugin/config/config.yaml",
	}

	if len(features) > 0 {
		extraArgs = append(extraArgs, fmt.Sprintf("-features=%s", strings.Join(features, ",")))
	}

	proxyName, proxyNamespace := "korrel8r", "korrel8r"

	if plugin.Spec.TroubleshootingPanel != nil {
		if plugin.Spec.TroubleshootingPanel.Korrel8r.Name != "" {
			proxyName = plugin.Spec.TroubleshootingPanel.Korrel8r.Name
		}
		if plugin.Spec.TroubleshootingPanel.Korrel8r.Namespace != "" {
			proxyNamespace = plugin.Spec.TroubleshootingPanel.Korrel8r.Namespace
		}
	}

	pluginInfo := &UIPluginInfo{
		Image:             image,
		Name:              plugin.Name,
		ConsoleName:       "troubleshooting-panel-console-plugin",
		DisplayName:       "Troubleshooting Panel Console Plugin",
		ResourceNamespace: namespace,
		LokiServiceNames:  make(map[string]string),
		ExtraArgs:         extraArgs,
		Proxies: []osv1alpha1.ConsolePluginProxy{
			{
				Type:      osv1alpha1.ProxyTypeService,
				Alias:     "korrel8r",
				Authorize: false,
				Service: osv1alpha1.ConsolePluginProxyServiceConfig{
					Name:      proxyName,
					Namespace: proxyNamespace,
					Port:      8443,
				},
			},
		},
		ConfigMap: &corev1.ConfigMap{
			TypeMeta: metav1.TypeMeta{
				APIVersion: corev1.SchemeGroupVersion.String(),
				Kind:       "ConfigMap",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Data: map[string]string{
				"config.yaml": configYaml,
			},
		},
	}

	return pluginInfo, nil
}

func marshalTroubleshootingPanelPluginConfig(cfg *uiv1alpha1.TroubleshootingPanelConfig) (string, error) {
	if cfg == nil {
		return "", nil
	}

	if cfg.Timeout == "" {
		return "", nil
	}

	pluginCfg := struct {
		Timeout string `yaml:"timeout"`
	}{
		Timeout: cfg.Timeout,
	}

	buf := &bytes.Buffer{}
	if err := yaml.NewEncoder(buf).Encode(pluginCfg); err != nil {
		return "", err
	}

	return buf.String(), nil
}

func getLokiServiceName(ctx context.Context, k client.Client, ns string) (string, error) {

	serviceList := &corev1.ServiceList{}
	if err := k.List(ctx, serviceList, client.InNamespace(ns)); err != nil {
		return "", err
	}

	// Accumulate services that contain "gateway" in their names
	var gatewayServices []corev1.Service
	for _, service := range serviceList.Items {
		if strings.Contains(service.Name, "gateway") {
			gatewayServices = append(gatewayServices, service)
		}
	}
	key := "app.kubernetes.io/managed-by"
	if len(gatewayServices) > 0 {
		for _, service := range gatewayServices {
			// Check if the service has the lokistack label
			if service.Labels[key] == "lokistack-controller" {
				return service.Name, nil
			}
		}
	}
	return "", nil
}
