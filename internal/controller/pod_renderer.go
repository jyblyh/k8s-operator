/*
Copyright 2026 BUPT AIOps Lab.
*/

package controller

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	vntopov1alpha1 "github.com/jyblyh/k8s-operator/api/v1alpha1"
	"github.com/jyblyh/k8s-operator/internal/common"
)

// renderPod 把 VNode.spec.template 渲染成最终下发到集群的 Pod。
//
// 注入项：
//   - metadata.ownerReferences -> vn
//   - metadata.labels: vntopo.bupt.site/{vnode,role,dc}
//   - metadata.annotations: vntopo.bupt.site/managed-by
//   - spec.nodeSelector / spec.affinity（来自 vn.spec）
//   - spec.initContainers 追加 vntopo-init
//   - 必要的 env / volumeMount 用于 init 容器联通本节点 agent socket
func renderPod(vn *vntopov1alpha1.VNode, initImage string, scheme *runtime.Scheme) (*corev1.Pod, error) {
	tpl := vn.Spec.Template.DeepCopy()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        vn.Name,
			Namespace:   vn.Namespace,
			Labels:      mergeLabels(tpl.ObjectMeta.Labels, vnodeLabels(vn)),
			Annotations: mergeLabels(tpl.ObjectMeta.Annotations, vnodeAnnotations()),
		},
		Spec: tpl.Spec,
	}

	// 调度
	if len(vn.Spec.NodeSelector) > 0 {
		if pod.Spec.NodeSelector == nil {
			pod.Spec.NodeSelector = map[string]string{}
		}
		for k, v := range vn.Spec.NodeSelector {
			pod.Spec.NodeSelector[k] = v
		}
	}
	if vn.Spec.Affinity != nil {
		pod.Spec.Affinity = vn.Spec.Affinity.DeepCopy()
	}

	// 注入 init 容器：拨本节点 agent 触发建链。
	//
	// imagePullPolicy=Always：dev 阶段固定 :dev tag，每次 push 镜像后下次 Pod 创建
	// 都会从 registry 重新拉，无需手动清节点缓存。生产可以改成 IfNotPresent。
	pod.Spec.InitContainers = append(pod.Spec.InitContainers, corev1.Container{
		Name:            common.InitContainerName,
		Image:           initImage,
		ImagePullPolicy: corev1.PullAlways,
		Env: []corev1.EnvVar{
			{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
			{Name: "POD_NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
			{Name: "HOST_IP", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.hostIP"}}},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "vntopo-agent-sock", MountPath: common.AgentSocketDir},
		},
	})

	// 提供给 init 容器的 hostPath 卷（agent DaemonSet 会把 socket 暴露到这里）。
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: "vntopo-agent-sock",
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				Path: common.AgentSocketDir,
				Type: ptrHostPathType(corev1.HostPathDirectoryOrCreate),
			},
		},
	})

	// ownerRef：用 controller-runtime 官方工具，自动填 APIVersion/Kind/UID，
	// 并设置 Controller=true、BlockOwnerDeletion=true，同时校验 ns 一致。
	if err := controllerutil.SetControllerReference(vn, pod, scheme); err != nil {
		return nil, err
	}

	return pod, nil
}

func vnodeLabels(vn *vntopov1alpha1.VNode) map[string]string {
	out := map[string]string{
		common.LabelVNode: vn.Name,
		common.LabelRole:  string(vn.Spec.Role),
	}
	if vn.Spec.DataCenter != "" {
		out[common.LabelDC] = vn.Spec.DataCenter
	}
	return out
}

func vnodeAnnotations() map[string]string {
	return map[string]string{
		common.AnnotationManagedBy: common.ManagedByValue,
	}
}

func mergeLabels(base, override map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}

func ptrHostPathType(t corev1.HostPathType) *corev1.HostPathType { return &t }
