/*
Copyright 2026 BUPT AIOps Lab.
*/

package agent

import (
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// podOnThisNode 仅放行调度到本节点的 Pod 事件。
//
// 注意：Pod 创建瞬间 Spec.NodeName 可能为空（尚未调度），这种情况会被过滤掉，
// 等调度完成的下一次更新事件再触发。
func podOnThisNode(nodeName string) predicate.Predicate {
	matches := func(obj interface{}) bool {
		p, ok := obj.(*corev1.Pod)
		if !ok {
			return false
		}
		return p.Spec.NodeName == nodeName
	}
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return matches(e.Object) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			return matches(e.ObjectNew) || matches(e.ObjectOld)
		},
		DeleteFunc:  func(e event.DeleteEvent) bool { return matches(e.Object) },
		GenericFunc: func(e event.GenericEvent) bool { return matches(e.Object) },
	}
}
