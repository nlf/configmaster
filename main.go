package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	apps "k8s.io/api/apps/v1beta2"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type ChangeType struct {
	Type string
	Name string
}

type Deployment struct {
	Name    string
	Options meta.GetOptions
}

var (
	namespace    = "default"
	changeTimers = map[ChangeType]*time.Timer{}
	updateTimers = map[Deployment]*time.Timer{}
	delay        = time.Duration(5) * time.Second

	client *kubernetes.Clientset
	config *rest.Config
)

func main() {
	ns := os.Getenv("CONFIGMASTER_NAMESPACE")
	if ns != "" {
		namespace = ns
	}

	d := os.Getenv("CONFIGMASTER_DELAY")
	if d != "" {
		di, err := strconv.Atoi(d)
		if err != nil {
			panic(err)
		}

		delay = time.Duration(di) * time.Second
	}

	var err error
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" && os.Getenv("KUBERNETES_SERVICE_PORT") != "" {
		config, err = rest.InClusterConfig()
		if err != nil {
			panic(err)
		}
	} else {
		if os.Getenv("CONFIGMASTER_HOST") == "" {
			panic(errors.New("Must export CONFIGMASTER_HOST"))
		}

		config = &rest.Config{
			Host: os.Getenv("CONFIGMASTER_HOST"),
		}
	}

	wg := sync.WaitGroup{}
	wg.Add(1)

	client = kubernetes.NewForConfigOrDie(config)
	log.Printf("configmaster connecting to %s listening on namespace %s with %.0fs delay", config.Host, namespace, delay.Seconds())

	filterFunc := func(in watch.Event) (watch.Event, bool) {
		return in, in.Type == "MODIFIED"
	}

	go func() {
		configmaps, err := client.CoreV1().ConfigMaps(namespace).Watch(meta.ListOptions{})
		if err != nil {
			panic(err)
		}

		for event := range watch.Filter(configmaps, filterFunc).ResultChan() {
			configmap, _ := event.Object.(*core.ConfigMap)
			delayedUpdate(ChangeType{
				Type: "ConfigMap",
				Name: configmap.ObjectMeta.Name,
			})
		}
	}()

	go func() {
		secrets, err := client.CoreV1().Secrets(namespace).Watch(meta.ListOptions{})
		if err != nil {
			panic(err)
		}

		for event := range watch.Filter(secrets, filterFunc).ResultChan() {
			secret, _ := event.Object.(*core.Secret)
			delayedUpdate(ChangeType{
				Type: "Secret",
				Name: secret.ObjectMeta.Name,
			})
		}
	}()

	wg.Wait()
}

func delayedUpdate(change ChangeType) {
	if timer, ok := changeTimers[change]; ok {
		log.Printf("saw change to %s %s, resetting delay", change.Type, change.Name)
		timer.Reset(delay)
	} else {
		log.Printf("saw change to %s %s, starting countdown", change.Type, change.Name)
		changeTimers[change] = time.AfterFunc(delay, func() {
			delete(changeTimers, change)
			findAndQueueDeployments(change)
		})
	}
}

func findAndQueueDeployments(change ChangeType) {
	deployments, err := client.AppsV1beta2().Deployments(namespace).List(meta.ListOptions{})
	if err != nil {
		panic(err)
	}

deploymentLoop:
	for _, deployment := range deployments.Items {
		for _, container := range deployment.Spec.Template.Spec.Containers {
			for _, env := range container.EnvFrom {
				if change.Type == "ConfigMap" && env.ConfigMapRef != nil && env.ConfigMapRef.Name == change.Name {
					queueDeployment(deployment)
					continue deploymentLoop
				}

				if change.Type == "Secret" && env.SecretRef != nil && env.SecretRef.Name == change.Name {
					queueDeployment(deployment)
					continue deploymentLoop
				}
			}

			for _, env := range container.Env {
				if change.Type == "ConfigMap" && env.ValueFrom != nil && env.ValueFrom.ConfigMapKeyRef != nil && env.ValueFrom.ConfigMapKeyRef.Name == change.Name {
					queueDeployment(deployment)
					continue deploymentLoop
				}

				if change.Type == "Secret" && env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil && env.ValueFrom.SecretKeyRef.Name == change.Name {
					queueDeployment(deployment)
					continue deploymentLoop
				}
			}
		}
	}
}

func queueDeployment(deployment apps.Deployment) {
	dep := Deployment{
		Name: deployment.Name,
		Options: meta.GetOptions{
			ResourceVersion: deployment.ObjectMeta.ResourceVersion,
		},
	}

	if timer, ok := updateTimers[dep]; ok {
		log.Printf("resetting timer on queued update to deployment %s", deployment.Name)
		timer.Reset(delay)
	} else {
		log.Printf("queuing update to deployment %s", deployment.Name)
		updateTimers[dep] = time.AfterFunc(delay, func() {
			delete(updateTimers, dep)
			patchDeployment(dep)
		})
	}
}

func patchDeployment(deployment Deployment) {
	dep, err := client.AppsV1beta2().Deployments(namespace).Get(deployment.Name, deployment.Options)
	if err != nil {
		log.Printf("error retrieving deployment %s: %+v", deployment.Name, err)
		return
	}

	current, err := json.Marshal(dep)
	if err != nil {
		log.Printf("error marshaling deployment %s: %+v", dep.Name, err)
		return
	}

	if dep.Annotations == nil {
		dep.Annotations = map[string]string{}
	}

	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = map[string]string{}
	}

	now := time.Now().Format(time.RFC3339)

	dep.Annotations["configmaster/last.update"] = now
	dep.Spec.Template.Annotations["configmaster/last.update"] = now

	updated, err := json.Marshal(dep)
	if err != nil {
		log.Printf("error marshaling deployment %s: %+v", dep.Name, err)
		return
	}

	patch, err := strategicpatch.CreateTwoWayMergePatch(current, updated, apps.Deployment{})
	if err != nil {
		log.Printf("error generating patch for deployment %s: %+v", dep.Name, err)
		return
	}

	_, err = client.AppsV1beta2().Deployments(namespace).Patch(dep.Name, types.StrategicMergePatchType, patch)
	if err != nil {
		log.Printf("error patching deployment %s: %+v", dep.Name, err)
		return
	}

	log.Printf("patched deployment %s", dep.Name)
}
