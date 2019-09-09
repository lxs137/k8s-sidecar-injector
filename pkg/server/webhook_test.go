package server

import (
	"encoding/json"
	"io/ioutil"
	"k8s.io/api/core/v1"
	"net/http"
	"testing"

	"github.com/evanphx/json-patch"
	"github.com/ghodss/yaml"
	"github.com/tumblr/k8s-sidecar-injector/internal/pkg/config"
	_ "github.com/tumblr/k8s-sidecar-injector/internal/pkg/testing"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	sidecars = "test/fixtures/sidecars"

	// all these configs are deserialized into metav1.ObjectMeta structs
	obj1             = "test/fixtures/k8s/object1.yaml"
	obj2             = "test/fixtures/k8s/object2.yaml"
	env1             = "test/fixtures/k8s/env1.yaml"
	obj3Missing      = "test/fixtures/k8s/object3-missing.yaml"
	obj4             = "test/fixtures/k8s/object4.yaml"
	obj5             = "test/fixtures/k8s/object5.yaml"
	obj6             = "test/fixtures/k8s/object6.yaml"
	obj7             = "test/fixtures/k8s/object7.yaml"
	ignoredNamespace = "test/fixtures/k8s/ignored-namespace-pod.yaml"
	badSidecar       = "test/fixtures/k8s/bad-sidecar.yaml"

	testIgnoredNamespaces = []string{"ignore-me"}
)

type expectedSidecarConfiguration struct {
	configuration   string
	expectedSidecar string
	expectedError   error
}

func TestLoadConfig(t *testing.T) {
	expectedNumInjectionConfigs := 6
	c, err := config.LoadConfigDirectory(sidecars)
	if err != nil {
		t.Error(err)
		t.Fail()
	}
	c.AnnotationNamespace = "injector.unittest.com"
	if len(c.Injections) != expectedNumInjectionConfigs {
		t.Errorf("expected %d injection configs to be loaded from %s, but got %d", expectedNumInjectionConfigs, sidecars, len(c.Injections))
		t.Fail()
	}
	if c.AnnotationNamespace != "injector.unittest.com" {
		t.Errorf("expected injector.unittest.com default AnnotationNamespace but got %s", c.AnnotationNamespace)
		t.Fail()
	}

	s := &WebhookServer{
		Config: c,
		Server: &http.Server{
			Addr: ":6969",
		},
	}

	// load some objects that are k8s metadata objects
	tests := []expectedSidecarConfiguration{
		{configuration: obj1, expectedSidecar: "sidecar-test"},
		{configuration: obj2, expectedSidecar: "complex-sidecar"},
		{configuration: env1, expectedSidecar: "env1"},
		{configuration: obj3Missing, expectedSidecar: "", expectedError: ErrMissingRequestAnnotation}, // this one is missing any annotations :)
		{configuration: obj4, expectedSidecar: "", expectedError: ErrSkipAlreadyInjected},             // this one is already injected, so it should not get injected again
		{configuration: obj5, expectedSidecar: "volume-mounts"},
		{configuration: obj6, expectedSidecar: "host-aliases"},
		{configuration: obj7, expectedSidecar: "init-containers"},
		{configuration: ignoredNamespace, expectedSidecar: "", expectedError: ErrSkipIgnoredNamespace},
		{configuration: badSidecar, expectedSidecar: "this-doesnt-exist", expectedError: ErrRequestedSidecarNotFound},
	}

	for _, test := range tests {
		data, err := ioutil.ReadFile(test.configuration)
		if err != nil {
			t.Errorf("unable to load object metadata yaml: %v", err)
			t.Fail()
		}

		var obj *metav1.ObjectMeta
		if err := yaml.Unmarshal(data, &obj); err != nil {
			t.Errorf("unable to unmarshal object metadata yaml: %v", err)
			t.Fail()
		}
		key, err := s.getSidecarConfigurationRequested(testIgnoredNamespaces, obj)
		if err != test.expectedError {
			t.Errorf("%s: error %v did not match %v", test.configuration, err, test.expectedError)
			t.Fail()
		}
		if key != test.expectedSidecar {
			t.Errorf("%s: expected sidecar to be %v but was %v instead", test.configuration, test.expectedSidecar, key)
			t.Fail()
		}
	}
}

func patchPod(originPodPath string, patchFunc func(*v1.Pod) ([]byte, error)) (*v1.Pod, error) {
	podDataYaml, err := ioutil.ReadFile(originPodPath)
	if err != nil {
		return nil, err
	}

	dataJson, err := yaml.YAMLToJSON(podDataYaml)
	if err != nil {
		return nil, err
	}

	var pod *v1.Pod
	if err := yaml.Unmarshal(podDataYaml, &pod); err != nil {
		return nil, err
	}

	patchData, err := patchFunc(pod)

	if err != nil {
		return nil, err
	}

	patchObj, err := jsonpatch.DecodePatch(patchData)
	if err != nil {
		return nil, err
	}

	mutatePodData, err := patchObj.Apply(dataJson)
	if err != nil {
		return nil, err
	}

	var mutatePod *v1.Pod
	if err := yaml.Unmarshal(mutatePodData, &mutatePod); err != nil {
		return nil, err
	}

	return mutatePod, nil
}

func TestPatchEnv(t *testing.T) {
	configData, err := ioutil.ReadFile("test/fixtures/sidecars/env1.yaml")
	if err != nil {
		t.Errorf("unable to load pod yaml: %v", err)
		t.Fail()
	}

	var injConfig *config.InjectionConfig
	if err := yaml.Unmarshal(configData, &injConfig); err != nil {
		t.Errorf("unable to unmarshal config yaml: %v", err)
		t.Fail()
	}
	patchFunc := func(pod *v1.Pod) ([]byte, error) {
		return createPatch(pod, injConfig, map[string]string{
			"injector.droidvirt.io/request": "env1",
		})
	}

	mutatePod, err := patchPod("test/fixtures/pods/env1.yaml", patchFunc)
	if err != nil {
		t.Errorf("unable to patch pod: %s", err)
		t.Fail()
	}
	t.Logf("After mutate: %s", mutatePod)

	if len(mutatePod.Spec.Containers) != 1 || len(mutatePod.Spec.Containers[0].Env) != 4 {
		t.Errorf("Patch error, expect 4 env, got %d", len(mutatePod.Spec.Containers[0].Env))
		t.Fail()
	}
}

func TestPatchVolumeMounts(t *testing.T) {
	configData, err := ioutil.ReadFile("test/fixtures/sidecars/volume-mounts.yaml")
	if err != nil {
		t.Errorf("unable to load pod yaml: %v", err)
		t.Fail()
	}

	var injConfig *config.InjectionConfig
	if err := yaml.Unmarshal(configData, &injConfig); err != nil {
		t.Errorf("unable to unmarshal config yaml: %v", err)
		t.Fail()
	}
	patchFunc := func(pod *v1.Pod) ([]byte, error) {
		return createPatch(pod, injConfig, map[string]string{
			"injector.droidvirt.io/request": "volume-mounts",
		})
	}

	mutatePod, err := patchPod("test/fixtures/pods/volume-mounts.yaml", patchFunc)
	if err != nil {
		t.Errorf("unable to patch pod: %s", err)
		t.Fail()
	}
	t.Logf("After mutate: %s", mutatePod)

	if len(mutatePod.Spec.Volumes) != 3 || len(mutatePod.Spec.Containers[0].VolumeMounts) != 2 {
		t.Errorf("Patch error, expect 3 volumes, got %d; expect 2 volumeMounts, got %d", len(mutatePod.Spec.Volumes), len(mutatePod.Spec.Containers[0].VolumeMounts))
		t.Fail()
	}
}

func TestPatchComplex(t *testing.T) {
	configData, err := ioutil.ReadFile("test/fixtures/sidecars/complex.yaml")
	if err != nil {
		t.Errorf("unable to load pod yaml: %v", err)
		t.Fail()
	}

	var injConfig *config.InjectionConfig
	if err := yaml.Unmarshal(configData, &injConfig); err != nil {
		t.Errorf("unable to unmarshal config yaml: %v", err)
		t.Fail()
	}
	patchFunc := func(pod *v1.Pod) ([]byte, error) {
		patchData, err := createPatch(pod, injConfig, map[string]string{
			"injector.droidvirt.io/request": "complex",
		})
		var patch []patchOperation
		if err := json.Unmarshal(patchData, &patch); err != nil {
			t.Errorf("Broken patch data: %v", err)
		}
		t.Logf("patch is: %+v", patch)
		return patchData, err
	}

	mutatePod, err := patchPod("test/fixtures/pods/complex.yaml", patchFunc)
	if err != nil {
		t.Errorf("unable to patch pod: %s", err)
		t.Fail()
	}
	t.Logf("After mutate: %s", mutatePod)

	if len(mutatePod.Spec.Containers) != 3 || len(mutatePod.Spec.Volumes) != 2 {
		t.Errorf("Patch error, expect 3 containers, got %d; expect 2 volumes, got %d", len(mutatePod.Spec.Containers), len(mutatePod.Spec.Volumes))
		t.Fail()
	}
}
