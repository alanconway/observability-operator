package observability

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	"sigs.k8s.io/yaml"

	"github.com/rhobs/observability-operator/config/stack"
)

const (
	overlayDir = "/stack/overlays/generated"
	stackRoot  = "/stack"
)

type Overlay struct {
	namespace  string
	base       string
	components []string
	patches    map[string][]byte
	resources  map[string][]byte
}

func (o *Overlay) SetBase(relPath string) {
	o.base = relPath
}

func NewOverlay(namespace string) *Overlay {
	return &Overlay{
		namespace:  namespace,
		components: nil,
		patches:    make(map[string][]byte),
		resources:  make(map[string][]byte),
	}
}

func (o *Overlay) AddComponent(relPath string) {
	o.components = append(o.components, relPath)
}

func (o *Overlay) RemoveComponent(relPath string) {
	var filtered []string
	for _, c := range o.components {
		if c != relPath {
			filtered = append(filtered, c)
		}
	}
	o.components = filtered
}

func (o *Overlay) AddPatch(path string, content []byte) {
	o.patches[path] = content
}

func (o *Overlay) AddResource(path string, content []byte) {
	o.resources[path] = content
}

func (o *Overlay) buildResMap() (resmap.ResMap, error) {
	fSys := filesys.MakeFsInMemory()

	if err := copyEmbeddedFS(fSys, stack.FS, stackRoot); err != nil {
		return nil, fmt.Errorf("loading embedded stack manifests: %w", err)
	}

	if err := o.writeOverlay(fSys); err != nil {
		return nil, fmt.Errorf("writing overlay: %w", err)
	}

	k := krusty.MakeKustomizer(krusty.MakeDefaultOptions())
	resMap, err := k.Run(fSys, overlayDir)
	if err != nil {
		return nil, fmt.Errorf("kustomize build: %w", err)
	}
	return resMap, nil
}

func (o *Overlay) Build() ([]client.Object, error) {
	resMap, err := o.buildResMap()
	if err != nil {
		return nil, err
	}

	var objects []client.Object
	for _, res := range resMap.Resources() {
		jsonBytes, err := res.MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("marshaling resource %s: %w", res.CurId(), err)
		}
		obj := &unstructured.Unstructured{}
		if err := obj.UnmarshalJSON(jsonBytes); err != nil {
			return nil, fmt.Errorf("unmarshaling resource %s: %w", res.CurId(), err)
		}
		objects = append(objects, obj)
	}
	return objects, nil
}

func (o *Overlay) BuildYAML() ([]byte, error) {
	resMap, err := o.buildResMap()
	if err != nil {
		return nil, err
	}
	return resMap.AsYaml()
}

// WriteToDir writes the full kustomize bundle to configDir.
// The output contains the embedded stack manifests (base/, components/) and
// an overlays/generated/ overlay that can be built with `kustomize build configDir/overlays/generated/`.
func (o *Overlay) WriteToDir(configDir string) error {
	if err := os.CopyFS(configDir, stack.FS); err != nil {
		return fmt.Errorf("copying embedded stack manifests: %w", err)
	}
	return o.writeOverlayToDir(filepath.Join(configDir, "overlays", "generated"))
}

func writeDirFile(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}

func writeFSFile(fSys filesys.FileSystem, path string, content []byte) error {
	if err := fSys.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	return fSys.WriteFile(path, content)
}

func (o *Overlay) writeOverlayToDir(dir string) error {
	kust := types.Kustomization{
		TypeMeta: types.TypeMeta{
			APIVersion: "kustomize.config.k8s.io/v1beta1",
			Kind:       "Kustomization",
		},
		Transformers: []string{"namespace-transformer.yaml"},
	}
	if o.base != "" {
		kust.Resources = append(kust.Resources, o.base)
	} else {
		kust.Components = o.components
	}
	for path := range o.resources {
		kust.Resources = append(kust.Resources, path)
	}
	for path := range o.patches {
		kust.Patches = append(kust.Patches, types.Patch{Path: path})
	}

	kustYAML, err := yaml.Marshal(kust)
	if err != nil {
		return fmt.Errorf("marshaling kustomization: %w", err)
	}
	if err := writeDirFile(filepath.Join(dir, "kustomization.yaml"), kustYAML); err != nil {
		return err
	}

	nsTransformer := fmt.Sprintf(`apiVersion: builtin
kind: NamespaceTransformer
metadata:
  name: namespace-transformer
  namespace: %s
unsetOnly: true
setRoleBindingSubjects: allServiceAccounts
`, o.namespace)
	if err := writeDirFile(filepath.Join(dir, "namespace-transformer.yaml"), []byte(nsTransformer)); err != nil {
		return err
	}

	for path, content := range o.patches {
		if err := writeDirFile(filepath.Join(dir, path), content); err != nil {
			return err
		}
	}
	for path, content := range o.resources {
		if err := writeDirFile(filepath.Join(dir, path), content); err != nil {
			return err
		}
	}

	return nil
}

func (o *Overlay) writeOverlay(fSys filesys.FileSystem) error {
	if err := fSys.MkdirAll(overlayDir); err != nil {
		return err
	}

	// Build the kustomization
	kust := types.Kustomization{
		TypeMeta: types.TypeMeta{
			APIVersion: "kustomize.config.k8s.io/v1beta1",
			Kind:       "Kustomization",
		},
		Transformers: []string{"namespace-transformer.yaml"},
	}
	if o.base != "" {
		kust.Resources = append(kust.Resources, o.base)
	} else {
		kust.Components = o.components
	}

	for path := range o.resources {
		kust.Resources = append(kust.Resources, path)
	}
	for path := range o.patches {
		kust.Patches = append(kust.Patches, types.Patch{Path: path})
	}

	kustYAML, err := yaml.Marshal(kust)
	if err != nil {
		return fmt.Errorf("marshaling kustomization: %w", err)
	}
	if err := fSys.WriteFile(filepath.Join(overlayDir, "kustomization.yaml"), kustYAML); err != nil {
		return err
	}

	// Write the namespace transformer
	nsTransformer := fmt.Sprintf(`apiVersion: builtin
kind: NamespaceTransformer
metadata:
  name: namespace-transformer
  namespace: %s
unsetOnly: true
setRoleBindingSubjects: allServiceAccounts
`, o.namespace)
	if err := fSys.WriteFile(filepath.Join(overlayDir, "namespace-transformer.yaml"), []byte(nsTransformer)); err != nil {
		return err
	}

	// Write patch and resource files
	for path, content := range o.patches {
		if err := writeFSFile(fSys, filepath.Join(overlayDir, path), content); err != nil {
			return err
		}
	}
	for path, content := range o.resources {
		if err := writeFSFile(fSys, filepath.Join(overlayDir, path), content); err != nil {
			return err
		}
	}

	return nil
}

func copyEmbeddedFS(fSys filesys.FileSystem, embedded fs.FS, root string) error {
	return fs.WalkDir(embedded, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(root, path)
		if d.IsDir() {
			return fSys.MkdirAll(target)
		}
		data, err := fs.ReadFile(embedded, path)
		if err != nil {
			return err
		}
		return fSys.WriteFile(target, data)
	})
}
