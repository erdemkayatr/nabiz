// Package k8s holds the admission webhook that automatically injects .NET
// instrumentation into pods.
//
// Injection never touches the application image: the instrumentation is copied
// into a shared emptyDir by an init container, and only environment variables
// are added to the application container. Undoing it is deleting the annotation
// and restarting the pod.
package k8s

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

const (
	// AnnotationInject asks for .NET instrumentation to be injected into the pod.
	AnnotationInject = "nabiz.io/inject-dotnet"
	// AnnotationServiceName sets OTEL_SERVICE_NAME explicitly. Without it the
	// name is derived from the workload.
	AnnotationServiceName = "nabiz.io/service-name"
	// AnnotationContainer says which container to instrument when the pod has
	// more than one.
	AnnotationContainer = "nabiz.io/container"
	// AnnotationSampleRatio is the agent-side sampling ratio, e.g. "0.1".
	AnnotationSampleRatio = "nabiz.io/sample-ratio"
	// AnnotationLibc is set to "musl" when the application image is
	// Alpine-based. The default is glibc; the official .NET images are
	// Debian-based.
	AnnotationLibc = "nabiz.io/libc"
	// AnnotationInjected is the marker that prevents injecting twice.
	AnnotationInjected = "nabiz.io/injected"

	volumeName    = "nabiz-dotnet-auto"
	mountPath     = "/nabiz-auto"
	initContainer = "nabiz-dotnet-init"
)

// InjectConfig is the webhook's injection behaviour.
type InjectConfig struct {
	// InstrumentationImage is the image carrying the auto-instrumentation files.
	InstrumentationImage string
	// CollectorEndpoint is the OTLP target, e.g. http://nabiz-collector.nabiz:4317.
	CollectorEndpoint string
	// ClusterName is the k8s.cluster.name added to every span.
	ClusterName string
	// DefaultSampleRatio is the ratio used when the annotation is absent.
	DefaultSampleRatio string
}

// ShouldInject says whether the pod is asking to be injected.
func ShouldInject(pod *corev1.Pod) bool {
	if pod.Annotations[AnnotationInjected] == "true" {
		return false
	}
	switch strings.ToLower(pod.Annotations[AnnotationInject]) {
	case "true", "dotnet", "yes", "1":
		return true
	}
	return false
}

// Inject mutates the pod in place and returns the name of the container that
// was instrumented.
func Inject(pod *corev1.Pod, cfg InjectConfig) (string, error) {
	if len(pod.Spec.Containers) == 0 {
		return "", fmt.Errorf("the pod has no containers")
	}

	idx, err := targetContainer(pod)
	if err != nil {
		return "", err
	}
	target := &pod.Spec.Containers[idx]

	addVolume(pod)
	addInitContainer(pod, cfg)

	target.VolumeMounts = appendVolumeMount(target.VolumeMounts, corev1.VolumeMount{
		Name:      volumeName,
		MountPath: mountPath,
	})
	target.Env = mergeEnv(target.Env, instrumentationEnv(pod, target, cfg))

	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[AnnotationInjected] = "true"

	return target.Name, nil
}

func targetContainer(pod *corev1.Pod) (int, error) {
	name := pod.Annotations[AnnotationContainer]
	if name == "" {
		return 0, nil
	}
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == name {
			return i, nil
		}
	}
	return 0, fmt.Errorf("the container named in the annotation was not found: %s", name)
}

func addVolume(pod *corev1.Pod) {
	for _, v := range pod.Spec.Volumes {
		if v.Name == volumeName {
			return
		}
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name:         volumeName,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	})
}

// initScript copies the instrumentation onto the shared volume and symlinks
// the right architecture directory as "native". The architecture is only known
// on the node the pod will run on, so it is resolved here rather than in the
// webhook; Kubernetes' own $(VAR) substitution cannot reach node information
// either.
func initScript(pod *corev1.Pod) string {
	libc := ""
	if strings.EqualFold(pod.Annotations[AnnotationLibc], "musl") {
		libc = "musl-"
	}
	return "set -e; " +
		"cp -a /otel/. " + mountPath + "/; " +
		"case \"$(uname -m)\" in aarch64|arm64) A=arm64;; *) A=x64;; esac; " +
		"ln -sfn " + mountPath + "/linux-" + libc + "${A} " + mountPath + "/native"
}

func addInitContainer(pod *corev1.Pod, cfg InjectConfig) {
	for _, c := range pod.Spec.InitContainers {
		if c.Name == initContainer {
			return
		}
	}
	runAsNonRoot := true
	pod.Spec.InitContainers = append(pod.Spec.InitContainers, corev1.Container{
		Name:    initContainer,
		Image:   cfg.InstrumentationImage,
		Command: []string{"/bin/sh", "-c", initScript(pod)},
		VolumeMounts: []corev1.VolumeMount{{
			Name:      volumeName,
			MountPath: mountPath,
		}},
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot:             &runAsNonRoot,
			ReadOnlyRootFilesystem:   boolPtr(true),
			AllowPrivilegeEscalation: boolPtr(false),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		},
	})
}

// instrumentationEnv produces the environment variables that enable the CLR
// profiler and carry the Kubernetes dimensions the topology needs.
//
// The Kubernetes data comes from the pod itself through the downward API; the
// collector never touches the Kubernetes API on the hot path. That is why the
// topology's Kubernetes dimension is essentially free.
func instrumentationEnv(pod *corev1.Pod, target *corev1.Container, cfg InjectConfig) []corev1.EnvVar {
	sampleRatio := pod.Annotations[AnnotationSampleRatio]
	if sampleRatio == "" {
		sampleRatio = cfg.DefaultSampleRatio
	}

	resourceAttrs := []string{
		"k8s.namespace.name=$(NABIZ_K8S_NAMESPACE)",
		"k8s.pod.name=$(NABIZ_K8S_POD)",
		"k8s.node.name=$(NABIZ_K8S_NODE)",
		"k8s.container.name=" + target.Name,
	}
	if cfg.ClusterName != "" {
		resourceAttrs = append(resourceAttrs, "k8s.cluster.name="+cfg.ClusterName)
	}

	env := []corev1.EnvVar{
		// --- downward API: the topology's Kubernetes dimensions ---
		fieldEnv("NABIZ_K8S_NAMESPACE", "metadata.namespace"),
		fieldEnv("NABIZ_K8S_POD", "metadata.name"),
		fieldEnv("NABIZ_K8S_NODE", "spec.nodeName"),

		// --- CLR profiler ---
		{Name: "CORECLR_ENABLE_PROFILING", Value: "1"},
		{Name: "CORECLR_PROFILER", Value: "{918728DD-259F-4A6A-AC2B-B85E1B658318}"},
		{Name: "CORECLR_PROFILER_PATH", Value: mountPath + "/native/OpenTelemetry.AutoInstrumentation.Native.so"},
		{Name: "DOTNET_ADDITIONAL_DEPS", Value: mountPath + "/AdditionalDeps"},
		{Name: "DOTNET_SHARED_STORE", Value: mountPath + "/store"},
		{Name: "DOTNET_STARTUP_HOOKS", Value: mountPath + "/net/OpenTelemetry.AutoInstrumentation.StartupHook.dll"},
		{Name: "OTEL_DOTNET_AUTO_HOME", Value: mountPath},

		// --- export ---
		{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: cfg.CollectorEndpoint},
		{Name: "OTEL_EXPORTER_OTLP_PROTOCOL", Value: "grpc"},
		{Name: "OTEL_METRICS_EXPORTER", Value: "none"},
		{Name: "OTEL_LOGS_EXPORTER", Value: "none"},

		// --- performance ---
		// The sampling decision is made in the agent: a dropped span is never
		// serialized, never reaches the network and never occupies the
		// collector. parentbased guarantees that a trace gets the same decision
		// in every service.
		{Name: "OTEL_TRACES_SAMPLER", Value: "parentbased_traceidratio"},
		{Name: "OTEL_TRACES_SAMPLER_ARG", Value: sampleRatio},
		// Batch exporter: no network call on the request path.
		{Name: "OTEL_BSP_SCHEDULE_DELAY", Value: "2000"},
		{Name: "OTEL_BSP_MAX_QUEUE_SIZE", Value: "8192"},
		{Name: "OTEL_BSP_MAX_EXPORT_BATCH_SIZE", Value: "1024"},

		{Name: "OTEL_SERVICE_NAME", Value: serviceName(pod, target)},
		{Name: "OTEL_RESOURCE_ATTRIBUTES", Value: strings.Join(resourceAttrs, ",")},
	}
	return env
}

// serviceName derives the service name from the annotation, or from the pod.
func serviceName(pod *corev1.Pod, target *corev1.Container) string {
	if v := pod.Annotations[AnnotationServiceName]; v != "" {
		return v
	}
	if v := pod.Labels["app.kubernetes.io/name"]; v != "" {
		return v
	}
	if v := pod.Labels["app"]; v != "" {
		return v
	}
	if pod.GenerateName != "" {
		return strings.TrimSuffix(pod.GenerateName, "-")
	}
	return target.Name
}

func fieldEnv(name, path string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: name,
		ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: path},
		},
	}
}

// mergeEnv never overwrites an existing variable: the user's own setting
// always wins.
func mergeEnv(existing, added []corev1.EnvVar) []corev1.EnvVar {
	present := make(map[string]struct{}, len(existing))
	for _, e := range existing {
		present[e.Name] = struct{}{}
	}
	for _, e := range added {
		if _, ok := present[e.Name]; ok {
			continue
		}
		existing = append(existing, e)
	}
	return existing
}

func appendVolumeMount(mounts []corev1.VolumeMount, m corev1.VolumeMount) []corev1.VolumeMount {
	for _, existing := range mounts {
		if existing.Name == m.Name {
			return mounts
		}
	}
	return append(mounts, m)
}

func boolPtr(b bool) *bool { return &b }
