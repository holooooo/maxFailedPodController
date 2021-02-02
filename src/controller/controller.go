package controller

import (
	"context"
	"reflect"
	"strconv"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	appsinformers "k8s.io/client-go/informers/apps/v1"
	coreinformers "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	appslisters "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"maxFailPodController/src/utils"

	"k8s.io/apimachinery/pkg/util/runtime"
)

const (
	labelConst string = "holooooo.io/MaxFailedPod"
	rsKind     string = "ReplicaSet"
	dpKind     string = "Deployment"
)

var (
	keyFunc = cache.DeletionHandlingMetaNamespaceKeyFunc
)

// DeploymentController 用以监听deployment，statefulset中的pod变化
type DeploymentController struct {
	kubeClient clientset.Interface

	dpLister        appslisters.DeploymentLister
	dpListerSynced  cache.InformerSynced
	rsLister        appslisters.ReplicaSetLister
	rsListerSynced  cache.InformerSynced
	podLister       corelisters.PodLister
	podListerSynced cache.InformerSynced

	queue workqueue.RateLimitingInterface
}

// NewDeploymentController 用以初始化这个Controller
func NewDeploymentController(kubeClient clientset.Interface,
	dpInformer appsinformers.DeploymentInformer,
	rsInformer appsinformers.ReplicaSetInformer,
	podInformer coreinformers.PodInformer) *DeploymentController {
	klog.Info("正在初始化控制器")
	dc := &DeploymentController{
		kubeClient: kubeClient,
		queue:      workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "deployment"),
	}

	dpInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    dc.addDeploy,
		UpdateFunc: dc.updateDeploy,
	})
	dc.dpLister = dpInformer.Lister()
	dc.dpListerSynced = dpInformer.Informer().HasSynced

	rsInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{})
	dc.rsLister = rsInformer.Lister()
	dc.rsListerSynced = rsInformer.Informer().HasSynced

	podInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: dc.updatePod,
	})
	dc.podLister = podInformer.Lister()
	dc.dpListerSynced = dpInformer.Informer().HasSynced
	return dc
}

func (dc *DeploymentController) enqueueDeploy(deploy *appsv1.Deployment) {
	key, err := keyFunc(deploy)
	if err != nil {
		klog.Errorf("couldn't get key for object %#v: %v", deploy, err)
		return
	}
	// klog.Infof("添加 %s/%s 入队列", deploy.Namespace, deploy.Name)
	dc.queue.Add(key)
}

func (dc *DeploymentController) addDeploy(obj interface{}) {
	deploy := obj.(*appsv1.Deployment)
	dc.enqueueDeploy(deploy)
}

func (dc *DeploymentController) updateDeploy(old interface{}, cur interface{}) {
	oldDeploy := old.(*appsv1.Deployment)
	curDeploy := cur.(*appsv1.Deployment)

	if curDeploy.UID != oldDeploy.UID {
		_, err := keyFunc(oldDeploy)
		if err != nil {
			klog.Errorf("无法得到该key对应的对象 %#v: %v", oldDeploy, err)
			return
		}
	}
	if *(oldDeploy.Spec.Replicas) != *(curDeploy.Spec.Replicas) {
		klog.V(4).Infof("%v 的replica已经变化: %d->%d", curDeploy.Name, *(oldDeploy.Spec.Replicas), *(curDeploy.Spec.Replicas))
	}
	dc.enqueueDeploy(curDeploy)
}

// 只有当pod更新为失败阶段后才处理
func (dc *DeploymentController) updatePod(old interface{}, cur interface{}) {
	curPod := cur.(*v1.Pod)
	oldPod := old.(*v1.Pod)
	if curPod.ResourceVersion == oldPod.ResourceVersion {
		return
	}

	if curPod.Status.Phase == oldPod.Status.Phase || curPod.Status.Phase != "Failed" {
		return
	}

	curControllerRef := metav1.GetControllerOf(curPod)
	oldControllerRef := metav1.GetControllerOf(oldPod)
	controllerRefChanged := !reflect.DeepEqual(curControllerRef, oldControllerRef)
	if controllerRefChanged && oldControllerRef != nil {
		// The ControllerRef was changed. Sync the old controller, if any.
		if deploy := dc.resolveControllerRef(oldPod.Namespace, oldControllerRef); deploy != nil {
			dc.enqueueDeploy(deploy)
		}
	}
}

// 查找pod所属的deployment
func (dc *DeploymentController) resolveControllerRef(namespace string, controllerRef *metav1.OwnerReference) *appsv1.Deployment {
	if controllerRef.Kind != rsKind {
		return nil
	}
	// 先通过ownerReferences找到replica，再找到deployment
	rs, err := dc.rsLister.ReplicaSets(namespace).Get(controllerRef.Name)
	if err != nil {
		return nil
	}
	if rs.UID != controllerRef.UID {
		return nil
	}

	rsControllerRef := metav1.GetControllerOf(rs)
	if rsControllerRef == nil || rsControllerRef.Kind != dpKind {
		return nil
	}
	dp, err := dc.dpLister.Deployments(namespace).Get(rsControllerRef.Name)
	if dp.UID != rsControllerRef.UID {
		return nil
	}
	return dp
}

// Run 用于运行worker
func (dc *DeploymentController) Run(workers int, stopch <-chan struct{}) {
	defer runtime.HandleCrash()
	defer dc.queue.ShuttingDown()

	klog.Infof("启动 maxFailedPod 控制器")
	defer klog.Infof("关闭 maxFailedPod 控制器")

	for i := 0; i < workers; i++ {
		go wait.Until(dc.worker, time.Second, stopch)
	}
	<-stopch
}

func (dc *DeploymentController) worker() {
	for dc.processNextWorkItem() {
	}
}

func (dc *DeploymentController) processNextWorkItem() bool {
	key, quit := dc.queue.Get()
	if quit {
		return false
	}
	defer dc.queue.Done(key)

	err := dc.syncHandler(key.(string))
	if err == nil {
		dc.queue.Forget(key)
		return true
	}

	klog.Errorf("同步 %q 对象失败，错误为: %v", key, err)
	dc.queue.AddRateLimited(key)
	return true
}

func (dc *DeploymentController) syncHandler(key string) error {
	startTime := time.Now()
	defer func() {
		klog.V(4).Infof("完成 %q 的同步，用时 %v", key, time.Since(startTime))
	}()

	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	deploy, err := dc.dpLister.Deployments(namespace).Get(name)
	if errors.IsNotFound(err) {
		klog.V(4).Infof("deployment %v 已被删除", key)
		return nil
	}
	if err != nil {
		return err
	}

	allPods, err := dc.podLister.Pods(deploy.Namespace).List(labels.Everything())
	if err != nil {
		return err
	}
	filteredPods := utils.FilterFailedPods(allPods)

	if deploy.DeletionTimestamp == nil {
		err = dc.manageDeploy(filteredPods, deploy)
	}
	return err
}

func (dc *DeploymentController) manageDeploy(filteredPods []*v1.Pod, deploy *appsv1.Deployment) error {
	if maxFailedPod, ok := deploy.ObjectMeta.Labels[labelConst]; ok {
		val, err := strconv.ParseInt(maxFailedPod, 10, 32)
		if err != nil {
			return err
		}
		// 设置为0代表不干扰pod创建
		if val == 0 {
			return nil
		}

		diff := len(filteredPods) - int(val)
		// klog.Infof("当前同步deploy %s中有%v个failed pod，当前允许保留%v个failed pod", deploy.Name, len(filteredPods), val)
		if diff > 0 {
			err = dc.deletePods(filteredPods, diff)
			return err
		}
	}

	expectVal := 50
	if *deploy.Spec.Replicas > int32(10) {
		expectVal = int(*deploy.Spec.Replicas * 5)
	}
	expectValStr := strconv.Itoa(expectVal)
	needUpdate := true
	if deploy.ObjectMeta.Labels == nil {
		deploy.ObjectMeta.Labels = map[string]string{labelConst: expectValStr}
	} else if deploy.ObjectMeta.Labels[labelConst] != expectValStr {
		deploy.ObjectMeta.Labels[labelConst] = expectValStr
	} else {
		needUpdate = false
	}

	if needUpdate {
		_, err := dc.kubeClient.AppsV1().Deployments(deploy.Namespace).Update(context.TODO(), deploy, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
	}

	return nil
}

func (dc *DeploymentController) deletePods(filteredPods []*v1.Pod, diff int) error {
	podsToDelete := filteredPods[:diff]

	errCh := make(chan error, diff)
	var wg sync.WaitGroup
	wg.Add(diff)
	for _, pod := range podsToDelete {
		go func(targetPod *v1.Pod) {
			defer wg.Done()
			err := dc.kubeClient.CoreV1().Pods(targetPod.Namespace).Delete(context.TODO(), targetPod.Name, metav1.DeleteOptions{})
			errCh <- err
		}(pod)
	}
	wg.Wait()

	select {
	case err := <-errCh:
		if err != nil {
			return err
		}
	default:
	}

	return nil
}
