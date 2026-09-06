package upgrade

import (
	"context"
	"fmt"
	"strings"

	"github.com/blang/semver/v4"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// staticPodApps are the control-plane static-pod labels scanned by G3. kube-proxy
// is intentionally omitted — this environment runs none, and its propagation is
// CABPT config regeneration anyway.
var staticPodApps = []string{"kube-apiserver", "kube-controller-manager", "kube-scheduler"}

// GateResult reports the convergence gate's verdict with actionable blockers.
type GateResult struct {
	Converged bool
	// Blockers names exactly what is lagging: machine names, node names with
	// kubelet versions, static-pod versions.
	Blockers []string
	// FallbackMachines lists Machines that carried no UpToDate condition and were
	// judged by spec.version minor equality instead; the caller events this.
	FallbackMachines []string
}

// EvaluateGate combines CAPI Machine state with a live workload scan (G1–G3).
// It deliberately does not trust Machine conditions alone: the terraform
// bootstrap node is invisible to CAPI, so only the Node (G2) and static-pod (G3)
// clauses can see it lag. Minor-equality is used for G2/G3 (bootstrap manifests
// are minor-scoped; a patch skew between the terraform-applied bootstrap node and
// CAPI machines is legitimate); G1 stays exact because UpToDate already encodes
// the control-plane provider's own convergence definition.
func EvaluateGate(ctx context.Context, mgmt client.Client, workload kubernetes.Interface,
	cluster, namespace string, target semver.Version, ignoreNodes sets.Set[string],
) (GateResult, error) {
	var result GateResult

	if err := gateMachines(ctx, mgmt, cluster, namespace, target, &result); err != nil {
		return result, err
	}
	if err := gateNodes(ctx, workload, target, ignoreNodes, &result); err != nil {
		return result, err
	}
	if err := gateStaticPods(ctx, workload, target, &result); err != nil {
		return result, err
	}

	result.Converged = len(result.Blockers) == 0
	return result, nil
}

// gateMachines is G1: every Machine of the cluster is UpToDate (exact), with a
// minor-equality fallback for Machines whose owner does not publish the
// condition.
func gateMachines(ctx context.Context, mgmt client.Client, cluster, namespace string, target semver.Version, result *GateResult) error {
	machines := &clusterv1.MachineList{}
	if err := mgmt.List(ctx, machines, client.InNamespace(namespace),
		client.MatchingLabels{clusterv1.ClusterNameLabel: cluster}); err != nil {
		return fmt.Errorf("listing machines: %w", err)
	}
	for i := range machines.Items {
		m := &machines.Items[i]
		cond := apimeta.FindStatusCondition(m.Status.Conditions, clusterv1.MachineUpToDateCondition)
		if cond == nil {
			result.FallbackMachines = append(result.FallbackMachines, m.Name)
			v, err := semver.ParseTolerant(m.Spec.Version)
			if err != nil || !sameMinor(v, target) {
				result.Blockers = append(result.Blockers,
					fmt.Sprintf("machine %s has no UpToDate condition and spec.version %q is not target minor", m.Name, m.Spec.Version))
			}
			continue
		}
		if cond.Status != "True" {
			result.Blockers = append(result.Blockers,
				fmt.Sprintf("machine %s UpToDate=%s (%s)", m.Name, cond.Status, cond.Reason))
		}
	}
	return nil
}

// gateNodes is G2: every workload Node's kubelet runs the target minor — the
// clause that covers the bootstrap node's kubelet.
func gateNodes(ctx context.Context, workload kubernetes.Interface, target semver.Version, ignoreNodes sets.Set[string], result *GateResult) error {
	nodes, err := workload.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing workload nodes: %w", err)
	}
	for i := range nodes.Items {
		n := &nodes.Items[i]
		if ignoreNodes.Has(n.Name) {
			continue
		}
		v, err := semver.ParseTolerant(n.Status.NodeInfo.KubeletVersion)
		if err != nil || !sameMinor(v, target) {
			result.Blockers = append(result.Blockers,
				fmt.Sprintf("node %s kubelet %s is not target minor v%d.%d", n.Name, n.Status.NodeInfo.KubeletVersion, target.Major, target.Minor))
		}
	}
	return nil
}

// gateStaticPods is G3: a DetectLowestVersion-style scan (image-tag semver,
// lowest wins) over the control-plane static pods, which covers the bootstrap
// node's static pods that G1 and G2 cannot see. At least one kube-apiserver pod
// must be present.
func gateStaticPods(ctx context.Context, workload kubernetes.Interface, target semver.Version, result *GateResult) error {
	sawAPIServer := false
	var lowest *semver.Version
	var lowestImage string

	for _, app := range staticPodApps {
		pods, err := workload.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{LabelSelector: "k8s-app=" + app})
		if err != nil {
			return fmt.Errorf("listing %s pods: %w", app, err)
		}
		for i := range pods.Items {
			pod := &pods.Items[i]
			if app == "kube-apiserver" {
				sawAPIServer = true
			}
			for _, container := range pod.Spec.Containers {
				v, ok := imageTagVersion(container.Image)
				if !ok {
					continue
				}
				if lowest == nil || v.LT(*lowest) {
					vv := v
					lowest = &vv
					lowestImage = container.Image
				}
			}
		}
	}

	switch {
	case !sawAPIServer:
		result.Blockers = append(result.Blockers, "no kube-apiserver static pod found in kube-system")
	case lowest != nil && !sameMinor(*lowest, target):
		result.Blockers = append(result.Blockers,
			fmt.Sprintf("static pod %s runs %s, not target minor v%d.%d", lowestImage, lowest.String(), target.Major, target.Minor))
	}
	return nil
}

// imageTagVersion parses the semver tag of a container image reference.
func imageTagVersion(image string) (semver.Version, bool) {
	idx := strings.LastIndex(image, ":")
	if idx < 0 {
		return semver.Version{}, false
	}
	tag := image[idx+1:]
	if strings.Contains(tag, "/") { // the colon was a registry port, no tag present
		return semver.Version{}, false
	}
	v, err := semver.ParseTolerant(tag)
	if err != nil {
		return semver.Version{}, false
	}
	return v, true
}

func sameMinor(a, b semver.Version) bool {
	return a.Major == b.Major && a.Minor == b.Minor
}
