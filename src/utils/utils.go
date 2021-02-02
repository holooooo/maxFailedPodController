package utils

import (
	v1 "k8s.io/api/core/v1"

	"k8s.io/klog"
)

func FilterFailedPods(pods []*v1.Pod) []*v1.Pod {
	var result []*v1.Pod
	for _, p := range pods {
		if IsPodFailed(p) {
			result = append(result, p)
		} else {
			klog.V(4).Infof("Ignoring inactive pod %v/%v in state %v, deletion time %v",
				p.Namespace, p.Name, p.Status.Phase, p.DeletionTimestamp)
		}
	}
	return result
}

func IsPodFailed(p *v1.Pod) bool {
	return v1.PodFailed == p.Status.Phase
}
