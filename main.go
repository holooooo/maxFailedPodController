package main

import (
	"path/filepath"
	"time"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"maxFailPodController/src/controller"
)

func main() {
	// 初始化k8s连接
	config, err := rest.InClusterConfig()
	if err != nil {
		klog.Errorf("无法获取sa, %v", err)

		klog.Info("尝试获取测试配置")

		config, err = clientcmd.BuildConfigFromFlags("https://kubernetes.docker.internal/", filepath.Join("kubernetes", "test.kubeconfig"))
		if err != nil {
			klog.Errorf("无法获取测试配置, %v", err)
			return
		}
	}
	kubeClient, err := kubernetes.NewForConfig(config)

	stopch := make(chan struct{})
	informer := informers.NewSharedInformerFactory(kubeClient, 10*time.Second)
	cont := controller.NewDeploymentController(kubeClient,
		informer.Apps().V1().Deployments(),
		informer.Apps().V1().ReplicaSets(),
		informer.Core().V1().Pods(),
	)
	go informer.Start(stopch)

	cont.Run(4, stopch)
}
