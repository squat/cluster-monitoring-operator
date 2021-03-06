// Copyright 2018 The Cluster Monitoring Operator Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package client

import (
	"fmt"
	"time"

	"github.com/coreos/prometheus-operator/pkg/alertmanager"
	"github.com/coreos/prometheus-operator/pkg/client/monitoring"
	monv1 "github.com/coreos/prometheus-operator/pkg/client/monitoring/v1"
	"github.com/coreos/prometheus-operator/pkg/k8sutil"
	prometheusoperator "github.com/coreos/prometheus-operator/pkg/prometheus"
	"github.com/golang/glog"
	routev1 "github.com/openshift/api/route/v1"
	secv1 "github.com/openshift/api/security/v1"
	openshiftrouteclientset "github.com/openshift/client-go/route/clientset/versioned"
	openshiftsecurityclientset "github.com/openshift/client-go/security/clientset/versioned"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1beta2"
	"k8s.io/api/core/v1"
	"k8s.io/api/extensions/v1beta1"
	v1betaextensions "k8s.io/api/extensions/v1beta1"
	rbacv1beta1 "k8s.io/api/rbac/v1beta1"
	extensionsobj "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

const (
	deploymentCreateTimeout = 5 * time.Minute
)

type Client struct {
	namespace      string
	appVersionName string
	kclient        kubernetes.Interface
	ossclient      openshiftsecurityclientset.Interface
	osrclient      openshiftrouteclientset.Interface
	mclient        monitoring.Interface
	eclient        apiextensionsclient.Interface
}

func New(namespace string, appVersionName string) (*Client, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	mclient, err := monitoring.NewForConfig(
		&monv1.DefaultCrdKinds,
		monv1.Group,
		cfg)
	if err != nil {
		return nil, err
	}

	kclient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, errors.Wrap(err, "creating kubernetes clientset client")
	}

	eclient, err := apiextensionsclient.NewForConfig(cfg)
	if err != nil {
		return nil, errors.Wrap(err, "creating apiextensions client")
	}

	ossclient, err := openshiftsecurityclientset.NewForConfig(cfg)
	if err != nil {
		return nil, errors.Wrap(err, "creating openshift security client")
	}

	osrclient, err := openshiftrouteclientset.NewForConfig(cfg)
	if err != nil {
		return nil, errors.Wrap(err, "creating openshift route client")
	}

	return &Client{
		namespace:      namespace,
		appVersionName: appVersionName,
		kclient:        kclient,
		ossclient:      ossclient,
		osrclient:      osrclient,
		mclient:        mclient,
		eclient:        eclient,
	}, nil
}

func (c *Client) KubernetesInterface() kubernetes.Interface {
	return c.kclient
}

func (c *Client) Namespace() string {
	return c.namespace
}

// ConfigMapListWatch returns a new ListWatch on the ConfigMap resource.
func (c *Client) ConfigMapListWatch() *cache.ListWatch {
	return cache.NewListWatchFromClient(c.kclient.CoreV1().RESTClient(), "configmaps", c.namespace, fields.Everything())
}

func (c *Client) WaitForPrometheusOperatorCRDsReady() error {
	wait.Poll(time.Second, time.Minute*5, func() (bool, error) {
		err := c.WaitForCRDReady(k8sutil.NewCustomResourceDefinition(monv1.DefaultCrdKinds.Prometheus, monv1.Group, map[string]string{}, false))
		if err != nil {
			return false, err
		}

		err = c.WaitForCRDReady(k8sutil.NewCustomResourceDefinition(monv1.DefaultCrdKinds.Alertmanager, monv1.Group, map[string]string{}, false))
		if err != nil {
			return false, err
		}

		err = c.WaitForCRDReady(k8sutil.NewCustomResourceDefinition(monv1.DefaultCrdKinds.ServiceMonitor, monv1.Group, map[string]string{}, false))
		if err != nil {
			return false, err
		}

		_, err = c.mclient.MonitoringV1().Prometheuses(c.namespace).List(metav1.ListOptions{})
		if err != nil {
			return false, err
		}

		_, err = c.mclient.MonitoringV1().Alertmanagers(c.namespace).List(metav1.ListOptions{})
		if err != nil {
			return false, err
		}

		_, err = c.mclient.MonitoringV1().ServiceMonitors(c.namespace).List(metav1.ListOptions{})
		if err != nil {
			return false, err
		}

		return true, nil
	})

	return nil
}

func (c *Client) CreateOrUpdateSecurityContextConstraints(s *secv1.SecurityContextConstraints) error {
	sccclient := c.ossclient.SecurityV1().SecurityContextConstraints()
	_, err := sccclient.Get(s.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := sccclient.Create(s)
		return errors.Wrap(err, "creating SecurityContextConstraints object failed")
	}
	if err != nil {
		return errors.Wrap(err, "retrieving SecurityContextConstraints object failed")
	}

	_, err = sccclient.Update(s)
	return errors.Wrap(err, "updating SecurityContextConstraints object failed")
}

func (c *Client) CreateRouteIfNotExists(r *routev1.Route) error {
	rclient := c.osrclient.RouteV1().Routes(r.GetNamespace())
	_, err := rclient.Get(r.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := rclient.Create(r)
		return errors.Wrap(err, "creating Route object failed")
	}
	return nil
}

func (c *Client) CreateOrUpdatePrometheus(p *monv1.Prometheus) error {
	pclient := c.mclient.MonitoringV1().Prometheuses(p.GetNamespace())
	_, err := pclient.Get(p.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := pclient.Create(p)
		return errors.Wrap(err, "creating Prometheus object failed")
	}
	if err != nil {
		return errors.Wrap(err, "retrieving Prometheus object failed")
	}

	_, err = pclient.Update(p)
	return errors.Wrap(err, "updating Prometheus object failed")
}

func (c *Client) CreateOrUpdatePrometheusRule(p *monv1.PrometheusRule) error {
	pclient := c.mclient.MonitoringV1().PrometheusRules(p.GetNamespace())
	_, err := pclient.Get(p.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := pclient.Create(p)
		return errors.Wrap(err, "creating PrometheusRule object failed")
	}
	if err != nil {
		return errors.Wrap(err, "retrieving PrometheusRule object failed")
	}

	_, err = pclient.Update(p)
	return errors.Wrap(err, "updating PrometheusRule object failed")
}

func (c *Client) CreateOrUpdateAlertmanager(a *monv1.Alertmanager) error {
	aclient := c.mclient.MonitoringV1().Alertmanagers(a.GetNamespace())
	_, err := aclient.Get(a.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := aclient.Create(a)
		return errors.Wrap(err, "creating Alertmanager object failed")
	}
	if err != nil {
		return errors.Wrap(err, "retrieving Alertmanager object failed")
	}

	_, err = aclient.Update(a)
	return errors.Wrap(err, "updating Alertmanager object failed")
}

func (c *Client) DeleteDeployment(d *v1beta1.Deployment) error {
	p := metav1.DeletePropagationForeground
	err := c.kclient.AppsV1beta2().Deployments(d.GetNamespace()).Delete(d.GetName(), &metav1.DeleteOptions{PropagationPolicy: &p})
	if apierrors.IsNotFound(err) {
		return nil
	}

	return err
}

func (c *Client) DeletePrometheus(p *monv1.Prometheus) error {
	pclient := c.mclient.MonitoringV1().Prometheuses(p.GetNamespace())

	err := pclient.Delete(p.GetName(), nil)
	if err != nil && !apierrors.IsNotFound(err) {
		return errors.Wrap(err, "deleting Prometheus object failed")
	}

	err = wait.Poll(time.Second*10, time.Minute*10, func() (bool, error) {
		pods, err := c.KubernetesInterface().Core().Pods(p.GetNamespace()).List(prometheusoperator.ListOptions(p.GetName()))
		if err != nil {
			return false, errors.Wrap(err, "retrieving pods during polling failed")
		}

		glog.V(6).Infof("waiting for %d Pods to be deleted", len(pods.Items))
		glog.V(6).Infof("done waiting? %s", len(pods.Items) == 0)

		return len(pods.Items) == 0, nil
	})

	return errors.Wrap(err, "waiting for Prometheus Pods to be gone failed")
}

func (c *Client) DeleteDaemonSet(d *v1beta1.DaemonSet) error {
	orphanDependents := false
	err := c.kclient.AppsV1beta2().DaemonSets(d.GetNamespace()).Delete(d.GetName(), &metav1.DeleteOptions{OrphanDependents: &orphanDependents})
	if apierrors.IsNotFound(err) {
		return nil
	}

	return err
}

func (c *Client) DeleteServiceMonitor(namespace, name string) error {
	sclient := c.mclient.MonitoringV1().ServiceMonitors(namespace)

	err := sclient.Delete(name, nil)
	// if the object does not exist then everything is good here
	if err != nil && !apierrors.IsNotFound(err) {
		return errors.Wrap(err, "deleting ServiceMonitor object failed")
	}

	return nil
}

func (c *Client) WaitForPrometheus(p *monv1.Prometheus) error {
	return wait.Poll(time.Second*10, time.Minute*5, func() (bool, error) {
		p, err := c.mclient.MonitoringV1().Prometheuses(p.GetNamespace()).Get(p.GetName(), metav1.GetOptions{})
		if err != nil {
			return false, errors.Wrap(err, "retrieving Prometheus object failed")
		}
		status, _, err := prometheusoperator.PrometheusStatus(c.kclient.(*kubernetes.Clientset), p)
		if err != nil {
			return false, errors.Wrap(err, "retrieving Prometheus status failed")
		}

		expectedReplicas := *p.Spec.Replicas
		if status.UpdatedReplicas == expectedReplicas && status.AvailableReplicas >= expectedReplicas {
			return true, nil
		}

		return false, nil
	})
}

func (c *Client) WaitForAlertmanager(a *monv1.Alertmanager) error {
	return wait.Poll(time.Second*10, time.Minute*5, func() (bool, error) {
		a, err := c.mclient.MonitoringV1().Alertmanagers(a.GetNamespace()).Get(a.GetName(), metav1.GetOptions{})
		if err != nil {
			return false, errors.Wrap(err, "retrieving Alertmanager object failed")
		}
		status, _, err := alertmanager.AlertmanagerStatus(c.kclient.(*kubernetes.Clientset), a)
		if err != nil {
			return false, errors.Wrap(err, "retrieving Alertmanager status failed")
		}

		expectedReplicas := *a.Spec.Replicas
		if status.UpdatedReplicas == expectedReplicas && status.AvailableReplicas >= expectedReplicas {
			return true, nil
		}

		return false, nil
	})
}

func (c *Client) CreateOrUpdateDeployment(dep *appsv1.Deployment) error {
	_, err := c.kclient.AppsV1beta2().Deployments(dep.GetNamespace()).Get(dep.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		err = c.CreateDeployment(dep)
		return errors.Wrap(err, "creating deployment object failed")
	}
	if err != nil {
		return errors.Wrap(err, "retrieving deployment object failed")
	}

	err = c.UpdateDeployment(dep)
	return errors.Wrap(err, "updating deployment object failed")
}

func (c *Client) CreateDeployment(dep *appsv1.Deployment) error {
	d, err := c.kclient.AppsV1beta2().Deployments(dep.GetNamespace()).Create(dep)
	if err != nil {
		return err
	}

	return c.WaitForDeploymentRollout(d)
}

func (c *Client) UpdateDeployment(dep *appsv1.Deployment) error {
	updated, err := c.kclient.AppsV1beta2().Deployments(dep.GetNamespace()).Update(dep)
	if err != nil {
		return err
	}

	return c.WaitForDeploymentRollout(updated)
}

func (c *Client) WaitForDeploymentRollout(dep *appsv1.Deployment) error {
	return wait.Poll(time.Second, deploymentCreateTimeout, func() (bool, error) {
		d, err := c.kclient.AppsV1beta2().Deployments(dep.GetNamespace()).Get(dep.GetName(), metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if d.Generation <= d.Status.ObservedGeneration && d.Status.UpdatedReplicas == d.Status.Replicas && d.Status.UnavailableReplicas == 0 {
			return true, nil
		}
		return false, nil
	})
}

func (c *Client) WaitForRouteReady(r *routev1.Route) (string, error) {
	host := ""
	err := wait.Poll(time.Second, deploymentCreateTimeout, func() (bool, error) {
		newRoute, err := c.osrclient.RouteV1().Routes(r.GetNamespace()).Get(r.GetName(), metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if len(newRoute.Status.Ingress) == 0 {
			return false, nil
		}
		for _, c := range newRoute.Status.Ingress[0].Conditions {
			if c.Type == "Admitted" && c.Status == "True" {
				host = newRoute.Spec.Host
				return true, nil
			}
		}
		return false, nil
	})

	return host, err
}

func (c *Client) CreateOrUpdateDaemonSet(ds *appsv1.DaemonSet) error {
	_, err := c.kclient.AppsV1beta2().DaemonSets(ds.GetNamespace()).Get(ds.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		err = c.CreateDaemonSet(ds)
		return errors.Wrap(err, "creating DaemonSet object failed")
	}
	if err != nil {
		return errors.Wrap(err, "retrieving DaemonSet object failed")
	}

	err = c.UpdateDaemonSet(ds)
	return errors.Wrap(err, "updating DaemonSet object failed")
}

func (c *Client) CreateDaemonSet(ds *appsv1.DaemonSet) error {
	d, err := c.kclient.AppsV1beta2().DaemonSets(ds.GetNamespace()).Create(ds)
	if err != nil {
		return err
	}

	return c.WaitForDaemonSetRollout(d)
}

func (c *Client) UpdateDaemonSet(ds *appsv1.DaemonSet) error {
	updated, err := c.kclient.AppsV1beta2().DaemonSets(ds.GetNamespace()).Update(ds)
	if err != nil {
		return err
	}

	return c.WaitForDaemonSetRollout(updated)
}

func (c *Client) WaitForDaemonSetRollout(ds *appsv1.DaemonSet) error {
	return wait.Poll(time.Second, deploymentCreateTimeout, func() (bool, error) {
		d, err := c.kclient.AppsV1beta2().DaemonSets(ds.GetNamespace()).Get(ds.GetName(), metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if d.Generation <= d.Status.ObservedGeneration && d.Status.UpdatedNumberScheduled == d.Status.DesiredNumberScheduled && d.Status.NumberUnavailable == 0 {
			return true, nil
		}
		return false, nil
	})
}

func (c *Client) CreateOrUpdateSecret(s *v1.Secret) error {
	sClient := c.kclient.CoreV1().Secrets(s.GetNamespace())
	_, err := sClient.Get(s.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := sClient.Create(s)
		return errors.Wrap(err, "creating Secret object failed")
	}
	if err != nil {
		return errors.Wrap(err, "retrieving Secret object failed")
	}

	_, err = sClient.Update(s)
	return errors.Wrap(err, "updating Secret object failed")
}

func (c *Client) CreateIfNotExistSecret(s *v1.Secret) error {
	sClient := c.kclient.CoreV1().Secrets(s.GetNamespace())
	_, err := sClient.Get(s.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := sClient.Create(s)
		return errors.Wrap(err, "creating Secret object failed")
	}

	return errors.Wrap(err, "retrieving Secret object failed")
}

func (c *Client) CreateOrUpdateConfigMapList(cml *v1.ConfigMapList) error {
	for _, cm := range cml.Items {
		err := c.CreateOrUpdateConfigMap(&cm)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) CreateOrUpdateConfigMap(cm *v1.ConfigMap) error {
	cmClient := c.kclient.CoreV1().ConfigMaps(cm.GetNamespace())
	_, err := cmClient.Get(cm.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := cmClient.Create(cm)
		return errors.Wrap(err, "creating ConfigMap object failed")
	}
	if err != nil {
		return errors.Wrap(err, "retrieving ConfigMap object failed")
	}

	_, err = cmClient.Update(cm)
	return errors.Wrap(err, "updating ConfigMap object failed")
}

func (c *Client) CreateIfNotExistConfigMap(cm *v1.ConfigMap) error {
	cClient := c.kclient.CoreV1().ConfigMaps(cm.GetNamespace())
	_, err := cClient.Get(cm.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := cClient.Create(cm)
		return errors.Wrap(err, "creating ConfigMap object failed")
	}

	return errors.Wrap(err, "retrieving ConfigMap object failed")
}

func (c *Client) CreateOrUpdateService(svc *v1.Service) error {
	sclient := c.kclient.CoreV1().Services(svc.GetNamespace())
	s, err := sclient.Get(svc.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = sclient.Create(svc)
		return errors.Wrap(err, "creating Service object failed")
	}
	if err != nil {
		return errors.Wrap(err, "retrieving Service object failed")
	}

	svc.ResourceVersion = s.ResourceVersion
	if svc.Spec.Type == v1.ServiceTypeClusterIP {
		svc.Spec.ClusterIP = s.Spec.ClusterIP
	}
	_, err = sclient.Update(svc)
	return errors.Wrap(err, "updating Service object failed")
}

func (c *Client) CreateOrUpdateEndpoints(endpoints *v1.Endpoints) error {
	eclient := c.kclient.CoreV1().Endpoints(endpoints.GetNamespace())
	e, err := eclient.Get(endpoints.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = eclient.Create(endpoints)
		return errors.Wrap(err, "creating Endpoints object failed")
	}
	if err != nil {
		return errors.Wrap(err, "retrieving Endpoints object failed")
	}

	endpoints.ResourceVersion = e.ResourceVersion
	_, err = eclient.Update(endpoints)
	return errors.Wrap(err, "updating Endpoints object failed")
}

func (c *Client) CreateOrUpdateRoleBinding(rb *rbacv1beta1.RoleBinding) error {
	rbClient := c.kclient.RbacV1beta1().RoleBindings(rb.GetNamespace())
	_, err := rbClient.Get(rb.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := rbClient.Create(rb)
		return errors.Wrap(err, "creating RoleBinding object failed")
	}
	if err != nil {
		return errors.Wrap(err, "retrieving RoleBinding object failed")
	}

	_, err = rbClient.Update(rb)
	return errors.Wrap(err, "updating RoleBinding object failed")
}

func (c *Client) CreateOrUpdateRole(r *rbacv1beta1.Role) error {
	rClient := c.kclient.RbacV1beta1().Roles(r.GetNamespace())
	_, err := rClient.Get(r.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := rClient.Create(r)
		return errors.Wrap(err, "creating Role object failed")
	}
	if err != nil {
		return errors.Wrap(err, "retrieving Role object failed")
	}

	_, err = rClient.Update(r)
	return errors.Wrap(err, "updating Role object failed")
}

func (c *Client) CreateOrUpdateClusterRole(cr *rbacv1beta1.ClusterRole) error {
	crClient := c.kclient.RbacV1beta1().ClusterRoles()
	_, err := crClient.Get(cr.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := crClient.Create(cr)
		return errors.Wrap(err, "creating ClusterRole object failed")
	}
	if err != nil {
		return errors.Wrap(err, "retrieving ClusterRole object failed")
	}

	_, err = crClient.Update(cr)
	return errors.Wrap(err, "updating ClusterRole object failed")
}

func (c *Client) CreateOrUpdateClusterRoleBinding(crb *rbacv1beta1.ClusterRoleBinding) error {
	crbClient := c.kclient.RbacV1beta1().ClusterRoleBindings()
	_, err := crbClient.Get(crb.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := crbClient.Create(crb)
		return errors.Wrap(err, "creating ClusterRoleBinding object failed")
	}
	if err != nil {
		return errors.Wrap(err, "retrieving ClusterRoleBinding object failed")
	}

	_, err = crbClient.Update(crb)
	return errors.Wrap(err, "updating ClusterRoleBinding object failed")
}

func (c *Client) CreateOrUpdateServiceAccount(sa *v1.ServiceAccount) error {
	sClient := c.kclient.CoreV1().ServiceAccounts(sa.GetNamespace())
	_, err := sClient.Get(sa.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := sClient.Create(sa)
		return errors.Wrap(err, "creating ServiceAccount object failed")
	}
	return errors.Wrap(err, "retrieving ServiceAccount object failed")

	// TODO(brancz): Use Patch instead of Update
	//
	// ServiceAccounts get a new secret generated whenever they are updated, even
	// if nothing has changed. This is likely due to "Update" performing a PUT
	// call signifying, that this may be a new ServiceAccount, therefore a new
	// token is needed. The expectation is that Patch does not cause this,
	// however, currently there has been no need to update ServiceAccounts,
	// therefore we are skipping this effort for now until we actually need to
	// change the ServiceAccount.
	//
	//if err != nil {
	//	return errors.Wrap(err, "retrieving ServiceAccount object failed")
	//}
	//
	//_, err = sClient.Update(sa)
	//return errors.Wrap(err, "updating ServiceAccount object failed")
}

func (c *Client) CreateOrUpdateServiceMonitor(sm *monv1.ServiceMonitor) error {
	smClient := c.mclient.MonitoringV1().ServiceMonitors(sm.GetNamespace())
	_, err := smClient.Get(sm.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := smClient.Create(sm)
		return errors.Wrap(err, "creating ServiceMonitor object failed")
	}
	if err != nil {
		return errors.Wrap(err, "retrieving ServiceMonitor object failed")
	}

	_, err = smClient.Update(sm)
	return errors.Wrap(err, "updating ServiceMonitor object failed")
}

func (c *Client) CreateOrUpdateIngress(ing *v1betaextensions.Ingress) error {
	ic := c.kclient.ExtensionsV1beta1().Ingresses(ing.GetNamespace())
	_, err := ic.Get(ing.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = ic.Create(ing)
		return errors.Wrap(err, "creating Ingress object failed")
	}
	if err != nil {
		return errors.Wrap(err, "retrieving Ingress object failed")
	}

	_, err = ic.Update(ing)
	return errors.Wrap(err, "creating Ingress object failed")
}

func (c *Client) WaitForCRDReady(crd *extensionsobj.CustomResourceDefinition) error {
	return wait.Poll(5*time.Second, 5*time.Minute, func() (bool, error) {
		return c.CRDReady(crd)
	})
}

func (c *Client) CRDReady(crd *extensionsobj.CustomResourceDefinition) (bool, error) {
	crdClient := c.eclient.ApiextensionsV1beta1().CustomResourceDefinitions()

	crdEst, err := crdClient.Get(crd.ObjectMeta.Name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	for _, cond := range crdEst.Status.Conditions {
		switch cond.Type {
		case extensionsobj.Established:
			if cond.Status == extensionsobj.ConditionTrue {
				return true, err
			}
		case extensionsobj.NamesAccepted:
			if cond.Status == extensionsobj.ConditionFalse {
				return false, fmt.Errorf("CRD naming conflict (%s): %v", crd.ObjectMeta.Name, cond.Reason)
			}
		}
	}
	return false, err
}
