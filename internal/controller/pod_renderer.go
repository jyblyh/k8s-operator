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
	"github.com/jyblyh/k8s-operator/internal/roleinjector"
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
//   - M4: 由 RoleInjector 产出的主容器命令、ConfigMap volumeMount、安全上下文等。
//
// inject 可以传 nil（向后兼容老调用方）；为 nil 时与 M3 行为完全一致。
func renderPod(
	vn *vntopov1alpha1.VNode,
	initImage string,
	scheme *runtime.Scheme,
	inject *roleinjector.RoleInject,
) (*corev1.Pod, error) {
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

	// 应用 RoleInjector 的产出（如果有）：
	//   - 注入主容器 command / volumeMounts / securityContext
	//   - pod 级 volumes 追加
	//   - extraConfigMaps（用户写的）也以 volume 形式挂入
	//
	// 用户优先级：tpl.Spec.Containers[0].Command 已经非空时，**不**覆盖 inject.Command；
	// 这给高级用户保留 escape hatch（把 RoleInjector 当默认值用）。
	applyRoleInject(pod, inject)
	applyExtraConfigMaps(pod, vn)

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

// applyRoleInject 把 inject 的产物落到 pod 上：
//
//   - inject.Command/Args  仅在主容器 (containers[0]) 没自填时才注入
//   - inject.Env           追加到主容器 env
//   - inject.Mounts        追加到主容器 volumeMounts
//   - inject.Volumes       追加到 pod.spec.volumes
//   - inject.Privileged    把主容器 SecurityContext.Privileged 设 true（如果还没设）
//   - inject.Capabilities  追加到主容器 SecurityContext.Capabilities.Add
//
// 故意不动除主容器外的 sidecar，避免误伤用户多容器拓扑。
func applyRoleInject(pod *corev1.Pod, inject *roleinjector.RoleInject) {
	if inject == nil {
		return
	}
	if len(pod.Spec.Containers) == 0 {
		// 没有主容器（不太可能，CRD validation 会拦），保险起见 noop
		return
	}
	main := &pod.Spec.Containers[0]

	// command / args：用户已填则尊重
	if len(main.Command) == 0 && len(inject.Command) > 0 {
		main.Command = append([]string(nil), inject.Command...)
	}
	if len(main.Args) == 0 && len(inject.Args) > 0 {
		main.Args = append([]string(nil), inject.Args...)
	}

	// env / mounts / volumes：追加（同名先到先得，与原项目 ConfigMap volume 名约定不冲突）
	main.Env = append(main.Env, inject.Env...)
	main.VolumeMounts = append(main.VolumeMounts, inject.Mounts...)
	pod.Spec.Volumes = append(pod.Spec.Volumes, inject.Volumes...)

	// SecurityContext：用户已显式开 privileged 或自配 capability 时，不覆盖；
	// 否则按 inject 要求填。
	if inject.Privileged || len(inject.Capabilities) > 0 {
		if main.SecurityContext == nil {
			main.SecurityContext = &corev1.SecurityContext{}
		}
		if inject.Privileged {
			if main.SecurityContext.Privileged == nil {
				t := true
				main.SecurityContext.Privileged = &t
			}
		}
		if len(inject.Capabilities) > 0 {
			if main.SecurityContext.Capabilities == nil {
				main.SecurityContext.Capabilities = &corev1.Capabilities{}
			}
			main.SecurityContext.Capabilities.Add = append(
				main.SecurityContext.Capabilities.Add, inject.Capabilities...)
		}
	}
}

// applyExtraConfigMaps 把用户在 spec.extraConfigMaps 里声明的 ConfigMap
// 以 volume 形式挂载到主容器。volume 名用 "extra-cm-{idx}"，避免和
// RoleInjector 自动生成的 volume 冲突。
//
// 注意：ConfigMap 资源本身的 create/update 在 controller 的 ensureConfigMaps 里做；
// 这里只负责"挂"。
func applyExtraConfigMaps(pod *corev1.Pod, vn *vntopov1alpha1.VNode) {
	if vn == nil || len(vn.Spec.ExtraConfigMaps) == 0 || len(pod.Spec.Containers) == 0 {
		return
	}
	main := &pod.Spec.Containers[0]
	for i, ecm := range vn.Spec.ExtraConfigMaps {
		if ecm.MountPath == "" {
			continue
		}
		cmName := ecm.Name
		if cmName == "" {
			cmName = defaultExtraCMName(vn.Name, i)
		}
		volName := defaultExtraCMVolumeName(i)
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: volName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
				},
			},
		})
		main.VolumeMounts = append(main.VolumeMounts, corev1.VolumeMount{
			Name:      volName,
			MountPath: ecm.MountPath,
			ReadOnly:  ecm.ReadOnly,
		})
	}
}
