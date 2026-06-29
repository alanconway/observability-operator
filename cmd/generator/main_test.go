package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const emptyInstaller = `apiVersion: observability.openshift.io/v1alpha1
kind: ObservabilityInstaller
metadata:
  name: test
`

func goRun(args ...string) *exec.Cmd {
	return exec.Command("go", append([]string{"run", "."}, args...)...)
}

func TestMainStdin(t *testing.T) {
	cmd := goRun("-")
	cmd.Stdin = strings.NewReader(emptyInstaller)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "generate failed: %s", out)
	require.Empty(t, out)
}

func TestMainFiles(t *testing.T) {
	inputFile := filepath.Join(t.TempDir(), "installer.yaml")
	require.NoError(t, os.WriteFile(inputFile, []byte(emptyInstaller), 0o644))
	cmd := goRun(inputFile)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "generate failed: %s", out)
	require.Empty(t, out)
}

func TestMainNoInput(t *testing.T) {
	cmd := goRun()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "generate failed: %s", out)
	require.Empty(t, out)
}

const tracingInstaller = `apiVersion: observability.openshift.io/v1alpha1
kind: ObservabilityInstaller
metadata:
  name: test
  namespace: test-ns
spec:
  capabilities:
    tracing:
      enabled: true
      storage:
        objectStorage:
          s3:
            bucket: test
            endpoint: http://minio:9000
`

func TestMainTracingOutput(t *testing.T) {
	cmd := goRun("-")
	cmd.Stdin = strings.NewReader(tracingInstaller)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "generate failed: %s", out)
	output := string(out)
	require.Contains(t, output, "kind: TempoStack")
	require.Contains(t, output, "kind: OpenTelemetryCollector")
	require.Contains(t, output, "kind: Subscription")
	require.Contains(t, output, "kind: Secret")
	require.Contains(t, output, "kind: ConsolePlugin")
	require.NotContains(t, output, "kind: UIPlugin")
}

func TestMainCSVFlags(t *testing.T) {
	cmd := goRun("-opentelemetry-csv", "otel-v1.0.0", "-tempo-csv", "tempo-v2.0.0", "-")
	cmd.Stdin = strings.NewReader(tracingInstaller)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "generate with CSV flags failed: %s", out)
	output := string(out)
	require.Contains(t, output, "otel-v1.0.0")
	require.Contains(t, output, "tempo-v2.0.0")
}

func TestMainConfigDir(t *testing.T) {
	dir := t.TempDir()
	cmd := goRun("-config", dir, "-")
	cmd.Stdin = strings.NewReader(tracingInstaller)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "generate -config failed: %s", out)
	generatedDir := filepath.Join(dir, "overlays", "generated")
	entries, err := os.ReadDir(generatedDir)
	require.NoError(t, err)
	require.NotEmpty(t, entries, "overlays/generated directory should not be empty")
}
