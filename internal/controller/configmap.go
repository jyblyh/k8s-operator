/*
Copyright 2026 BUPT AIOps Lab.
*/

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	vntopov1alpha1 "github.com/jyblyh/k8s-operator/api/v1alpha1"
	"github.com/jyblyh/k8s-operator/internal/common"
)

// ensureConfigMaps 同步 RoleInjector + ExtraConfigMaps 期望的所有 ConfigMap，
// 并返回它们 data 部分的聚合 hash（hex sha256），controller 把它写到
// VNode.status.configHash，供 agent 触发 reload。
//
// 行为：
//   - 不存在 → create
//   - 存在但 data 不一致 → update（保留 OwnerReference）
//   - 一致 → 跳过（避免 noisy reconcile）
//
// 所有 ConfigMap 都会被设上 OwnerReference -> vn，VNode 删除时级联删除，
// 不需要 finalizer 单独清理。
func ensureConfigMaps(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	vn *vntopov1alpha1.VNode,
	desired []corev1.ConfigMap,
) (string, error) {
	logger := log.FromContext(ctx).WithName("configmap")

	// 计算聚合 hash 时，desired 顺序必须稳定。按 Namespace/Name 排序。
	sort.SliceStable(desired, func(i, j int) bool {
		if desired[i].Namespace != desired[j].Namespace {
			return desired[i].Namespace < desired[j].Namespace
		}
		return desired[i].Name < desired[j].Name
	})

	hash := computeConfigMapsHash(desired)

	for i := range desired {
		want := &desired[i]
		if want.Namespace == "" {
			want.Namespace = vn.Namespace
		}
		// 给 ConfigMap 打统一的 managed-by label / annotation，方便 kubectl 检索。
		ensureManagedLabels(want, vn)

		if err := controllerutil.SetControllerReference(vn, want, scheme); err != nil {
			return "", fmt.Errorf("set ownerref on cm %s/%s: %w", want.Namespace, want.Name, err)
		}

		key := types.NamespacedName{Namespace: want.Namespace, Name: want.Name}
		var got corev1.ConfigMap
		err := c.Get(ctx, key, &got)
		switch {
		case apierrors.IsNotFound(err):
			if err := c.Create(ctx, want); err != nil {
				return "", fmt.Errorf("create cm %s: %w", key, err)
			}
			logger.Info("configmap created", "cm", key.String())
		case err != nil:
			return "", fmt.Errorf("get cm %s: %w", key, err)
		default:
			if configMapDataEqual(got.Data, want.Data) && configMapBinaryDataEqual(got.BinaryData, want.BinaryData) {
				continue
			}
			// 保留 ResourceVersion，让 K8s 走 OCC 校验
			updated := got.DeepCopy()
			updated.Data = want.Data
			updated.BinaryData = want.BinaryData
			ensureManagedLabels(updated, vn)
			if err := controllerutil.SetControllerReference(vn, updated, scheme); err != nil {
				return "", fmt.Errorf("set ownerref on cm %s: %w", key, err)
			}
			if err := c.Update(ctx, updated); err != nil {
				return "", fmt.Errorf("update cm %s: %w", key, err)
			}
			logger.Info("configmap updated", "cm", key.String())
		}
	}

	return hash, nil
}

// ensureExtraConfigMaps 把用户在 spec.extraConfigMaps 里声明的 ConfigMap 也走
// ensureConfigMaps 流程；返回 desired 列表（让上层一起算 hash）。
func desiredExtraConfigMaps(vn *vntopov1alpha1.VNode) []corev1.ConfigMap {
	if len(vn.Spec.ExtraConfigMaps) == 0 {
		return nil
	}
	out := make([]corev1.ConfigMap, 0, len(vn.Spec.ExtraConfigMaps))
	for i, ecm := range vn.Spec.ExtraConfigMaps {
		name := ecm.Name
		if name == "" {
			name = defaultExtraCMName(vn.Name, i)
		}
		out = append(out, corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: vn.Namespace,
			},
			Data: ecm.Data,
		})
	}
	return out
}

// computeConfigMapsHash 把 (name, key=value) 三元组按字典序拼起来取 sha256。
//
// 选 sha256 而不是 fnv 是因为：
//  1. ConfigMap data 可能包含敏感信息（虽然这里都是网络配置），sha256 抗碰撞更稳；
//  2. hex 输出 64 字符固定长度，写到 status.configHash 字段无格式问题；
//  3. 单次计算成本可忽略，agent 也只是字符串比对。
func computeConfigMapsHash(cms []corev1.ConfigMap) string {
	h := sha256.New()
	for _, cm := range cms {
		fmt.Fprintf(h, "name=%s/%s\n", cm.Namespace, cm.Name)

		// data 按 key 排序写入
		dataKeys := make([]string, 0, len(cm.Data))
		for k := range cm.Data {
			dataKeys = append(dataKeys, k)
		}
		sort.Strings(dataKeys)
		for _, k := range dataKeys {
			fmt.Fprintf(h, "data:%s=%s\n", k, cm.Data[k])
		}

		// binaryData 同样排序写入
		binKeys := make([]string, 0, len(cm.BinaryData))
		for k := range cm.BinaryData {
			binKeys = append(binKeys, k)
		}
		sort.Strings(binKeys)
		for _, k := range binKeys {
			fmt.Fprintf(h, "bdata:%s=%x\n", k, cm.BinaryData[k])
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func configMapDataEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func configMapBinaryDataEqual(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok {
			return false
		}
		if len(v) != len(w) {
			return false
		}
		for i := range v {
			if v[i] != w[i] {
				return false
			}
		}
	}
	return true
}

func ensureManagedLabels(cm *corev1.ConfigMap, vn *vntopov1alpha1.VNode) {
	if cm.Labels == nil {
		cm.Labels = map[string]string{}
	}
	cm.Labels[common.LabelVNode] = vn.Name
	if vn.Spec.DataCenter != "" {
		cm.Labels[common.LabelDC] = vn.Spec.DataCenter
	}
	if cm.Annotations == nil {
		cm.Annotations = map[string]string{}
	}
	cm.Annotations[common.AnnotationManagedBy] = common.ManagedByValue
}

func defaultExtraCMName(vnName string, idx int) string {
	return fmt.Sprintf("%s-extra-%d", vnName, idx)
}

func defaultExtraCMVolumeName(idx int) string {
	return fmt.Sprintf("vntopo-extra-cm-%d", idx)
}
