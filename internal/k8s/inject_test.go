package k8s

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func testConfig() InjectConfig {
	return InjectConfig{
		InstrumentationImage: "nabiz/dotnet-instrumentation:1.16.0",
		CollectorEndpoint:    "http://nabiz-collector.nabiz.svc:4317",
		ClusterName:          "prod-eu",
		DefaultSampleRatio:   "1.0",
	}
}

func newPod(annotations map[string]string, containers ...string) *corev1.Pod {
	if len(containers) == 0 {
		containers = []string{"app"}
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "api-7d9f8b6c5d-x2k9p",
			Namespace:   "shop",
			Annotations: annotations,
			Labels:      map[string]string{"app": "api"},
		},
	}
	for _, c := range containers {
		pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: c, Image: c + ":1"})
	}
	return pod
}

func envOf(c corev1.Container) map[string]corev1.EnvVar {
	m := make(map[string]corev1.EnvVar, len(c.Env))
	for _, e := range c.Env {
		m[e.Name] = e
	}
	return m
}

func TestShouldInjectOnlyWhenAnnotated(t *testing.T) {
	cases := []struct {
		annotations map[string]string
		want        bool
	}{
		{nil, false},
		{map[string]string{}, false},
		{map[string]string{AnnotationInject: "true"}, true},
		{map[string]string{AnnotationInject: "TRUE"}, true},
		{map[string]string{AnnotationInject: "dotnet"}, true},
		{map[string]string{AnnotationInject: "false"}, false},
		// A pod that has already been injected must not be processed a second time.
		{map[string]string{AnnotationInject: "true", AnnotationInjected: "true"}, false},
	}
	for _, tc := range cases {
		if got := ShouldInject(newPod(tc.annotations)); got != tc.want {
			t.Errorf("%v -> %v, %v bekleniyordu", tc.annotations, got, tc.want)
		}
	}
}

func TestInjectAddsProfilerWithoutTouchingImage(t *testing.T) {
	pod := newPod(map[string]string{AnnotationInject: "true"})
	originalImage := pod.Spec.Containers[0].Image

	container, err := Inject(pod, testConfig())
	if err != nil {
		t.Fatalf("injection error: %v", err)
	}
	if container != "app" {
		t.Errorf("hedef container = %q, app bekleniyordu", container)
	}
	if pod.Spec.Containers[0].Image != originalImage {
		t.Error("the application image was changed; injection must not touch the image")
	}

	env := envOf(pod.Spec.Containers[0])
	if env["CORECLR_ENABLE_PROFILING"].Value != "1" {
		t.Error("the CLR profiler was not enabled")
	}
	if !strings.HasPrefix(env["CORECLR_PROFILER_PATH"].Value, mountPath+"/native/") {
		t.Errorf("the profiler path does not go through the symlink: %q", env["CORECLR_PROFILER_PATH"].Value)
	}
	if env["OTEL_EXPORTER_OTLP_ENDPOINT"].Value != "http://nabiz-collector.nabiz.svc:4317" {
		t.Errorf("the collector endpoint is wrong: %q", env["OTEL_EXPORTER_OTLP_ENDPOINT"].Value)
	}
}

func TestInjectAddsInitContainerAndSharedVolume(t *testing.T) {
	pod := newPod(map[string]string{AnnotationInject: "true"})
	if _, err := Inject(pod, testConfig()); err != nil {
		t.Fatal(err)
	}

	if len(pod.Spec.InitContainers) != 1 || pod.Spec.InitContainers[0].Name != initContainer {
		t.Fatalf("init container eklenmedi: %+v", pod.Spec.InitContainers)
	}
	if len(pod.Spec.Volumes) != 1 || pod.Spec.Volumes[0].EmptyDir == nil {
		t.Fatalf("the shared emptyDir was not added: %+v", pod.Spec.Volumes)
	}
	if len(pod.Spec.Containers[0].VolumeMounts) != 1 {
		t.Fatalf("no mount was added to the application container")
	}

	// The architecture must be resolved on the node, not fixed in the webhook.
	script := pod.Spec.InitContainers[0].Command[2]
	if !strings.Contains(script, "uname -m") {
		t.Error("the init script does not resolve the architecture at runtime")
	}
	if !strings.Contains(script, "ln -sfn") {
		t.Error("init script native symlink'i kurmuyor")
	}
}

func TestInjectCarriesKubernetesDimensionsViaDownwardAPI(t *testing.T) {
	pod := newPod(map[string]string{AnnotationInject: "true"})
	if _, err := Inject(pod, testConfig()); err != nil {
		t.Fatal(err)
	}
	env := envOf(pod.Spec.Containers[0])

	for _, name := range []string{"NABIZ_K8S_NAMESPACE", "NABIZ_K8S_POD", "NABIZ_K8S_NODE"} {
		e, ok := env[name]
		if !ok {
			t.Fatalf("%s eklenmedi", name)
		}
		if e.ValueFrom == nil || e.ValueFrom.FieldRef == nil {
			t.Errorf("%s uses a fixed value instead of the downward API", name)
		}
	}

	attrs := env["OTEL_RESOURCE_ATTRIBUTES"].Value
	for _, want := range []string{
		"k8s.namespace.name=$(NABIZ_K8S_NAMESPACE)",
		"k8s.pod.name=$(NABIZ_K8S_POD)",
		"k8s.node.name=$(NABIZ_K8S_NODE)",
		"k8s.cluster.name=prod-eu",
	} {
		if !strings.Contains(attrs, want) {
			t.Errorf("resource attribute eksik: %s (var olan: %s)", want, attrs)
		}
	}
}

func TestInjectNeverOverridesUserEnv(t *testing.T) {
	pod := newPod(map[string]string{AnnotationInject: "true"})
	pod.Spec.Containers[0].Env = []corev1.EnvVar{
		{Name: "OTEL_SERVICE_NAME", Value: "elle-verilen-ad"},
		{Name: "OTEL_TRACES_SAMPLER_ARG", Value: "0.01"},
	}

	if _, err := Inject(pod, testConfig()); err != nil {
		t.Fatal(err)
	}
	env := envOf(pod.Spec.Containers[0])
	if env["OTEL_SERVICE_NAME"].Value != "elle-verilen-ad" {
		t.Errorf("the user's service name was overwritten: %q", env["OTEL_SERVICE_NAME"].Value)
	}
	if env["OTEL_TRACES_SAMPLER_ARG"].Value != "0.01" {
		t.Errorf("the user's sampling ratio was overwritten: %q", env["OTEL_TRACES_SAMPLER_ARG"].Value)
	}
}

func TestServiceNameResolutionOrder(t *testing.T) {
	cfg := testConfig()

	pod := newPod(map[string]string{AnnotationInject: "true", AnnotationServiceName: "sepet-servisi"})
	_, _ = Inject(pod, cfg)
	if got := envOf(pod.Spec.Containers[0])["OTEL_SERVICE_NAME"].Value; got != "sepet-servisi" {
		t.Errorf("the annotation did not win: %q", got)
	}

	pod = newPod(map[string]string{AnnotationInject: "true"})
	pod.Labels = map[string]string{"app.kubernetes.io/name": "odeme"}
	_, _ = Inject(pod, cfg)
	if got := envOf(pod.Spec.Containers[0])["OTEL_SERVICE_NAME"].Value; got != "odeme" {
		t.Errorf("the standard label was not used: %q", got)
	}

	pod = newPod(map[string]string{AnnotationInject: "true"})
	_, _ = Inject(pod, cfg)
	if got := envOf(pod.Spec.Containers[0])["OTEL_SERVICE_NAME"].Value; got != "api" {
		t.Errorf("the app label was not used: %q", got)
	}
}

func TestInjectTargetsAnnotatedContainer(t *testing.T) {
	pod := newPod(map[string]string{
		AnnotationInject:    "true",
		AnnotationContainer: "worker",
	}, "sidecar", "worker")

	container, err := Inject(pod, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if container != "worker" {
		t.Errorf("hedef container = %q, worker bekleniyordu", container)
	}
	if len(pod.Spec.Containers[0].Env) != 0 {
		t.Error("sidecar'a dokunuldu")
	}
	if len(pod.Spec.Containers[1].Env) == 0 {
		t.Error("the target container was not instrumented")
	}
}

func TestInjectFailsOnUnknownContainer(t *testing.T) {
	pod := newPod(map[string]string{
		AnnotationInject:    "true",
		AnnotationContainer: "olmayan",
	}, "app")
	if _, err := Inject(pod, testConfig()); err == nil {
		t.Error("expected an error for a container that does not exist")
	}
}

func TestMuslAnnotationSwitchesRuntimeDirectory(t *testing.T) {
	pod := newPod(map[string]string{AnnotationInject: "true", AnnotationLibc: "musl"})
	if _, err := Inject(pod, testConfig()); err != nil {
		t.Fatal(err)
	}
	script := pod.Spec.InitContainers[0].Command[2]
	if !strings.Contains(script, "linux-musl-") {
		t.Errorf("the musl directory was not selected: %s", script)
	}
}

func TestInjectIsIdempotent(t *testing.T) {
	pod := newPod(map[string]string{AnnotationInject: "true"})
	if _, err := Inject(pod, testConfig()); err != nil {
		t.Fatal(err)
	}
	envCount := len(pod.Spec.Containers[0].Env)

	// ShouldInject should block the second round; even if Inject is called again,
	// the structures must not be duplicated.
	if ShouldInject(pod) {
		t.Fatal("an already-injected pod is being asked for again")
	}
	if _, err := Inject(pod, testConfig()); err != nil {
		t.Fatal(err)
	}
	if len(pod.Spec.InitContainers) != 1 {
		t.Errorf("the init container was duplicated: %d", len(pod.Spec.InitContainers))
	}
	if len(pod.Spec.Volumes) != 1 {
		t.Errorf("the volume was duplicated: %d", len(pod.Spec.Volumes))
	}
	if len(pod.Spec.Containers[0].VolumeMounts) != 1 {
		t.Errorf("the volume mount was duplicated: %d", len(pod.Spec.Containers[0].VolumeMounts))
	}
	if len(pod.Spec.Containers[0].Env) != envCount {
		t.Errorf("env was duplicated: %d -> %d", envCount, len(pod.Spec.Containers[0].Env))
	}
}
