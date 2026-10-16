//go:build integration

/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package integration

import (
	"testing"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	components "github.com/opendatahub-io/ai-gateway-operator/api/components/v1alpha1"
	"github.com/opendatahub-io/ai-gateway-operator/test/support"
)

// This tests setup, status and retention against the API server. The pause image
// deliberately cannot run bootstrap: failed bootstrap must not report Ready.
func TestAIGateway_GridEnrollment(t *testing.T) {
	g := NewWithT(t)
	t.Setenv("RELATED_IMAGE_ODH_PRAXIS_AI_GRID_ENROLLMENT_IMAGE", operandPauseImage)
	t.Setenv("RELATED_IMAGE_ODH_PRAXIS_AI_GRID_ENROLLMENT_DB_IMAGE", operandPauseImage)
	const ns = "ai-grid-enrollment"
	obj := &components.AIGateway{ObjectMeta: metav1.ObjectMeta{Name: components.AIGatewayInstanceName}, Spec: components.AIGatewaySpec{
		AIGrid: components.AIGridComponent{ManagementState: "Managed"},
	}}
	g.Expect(k8sClient.Create(ctx, obj)).To(Succeed())
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, obj) })
	gridKey := client.ObjectKey{Name: "grid-operator", Namespace: support.IntegrationTestNamespace()}
	g.Eventually(func() error { return k8sClient.Get(ctx, gridKey, &appsv1.Deployment{}) }, timeout, interval).To(Succeed())
	current := &components.AIGateway{}
	g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), current)).To(Succeed())
	g.Expect(current.Spec.AIGrid.GridEnrollment).To(BeNil())
	current.Spec.AIGrid.GridEnrollment = &components.GridEnrollmentConfig{Enabled: true}
	g.Expect(k8sClient.Update(ctx, current)).To(Succeed())
	deploymentKey := client.ObjectKey{Name: "grid-enrollment", Namespace: ns}
	g.Eventually(func() error { return k8sClient.Get(ctx, deploymentKey, &appsv1.Deployment{}) }, timeout, interval).To(Succeed())
	g.Eventually(func() int {
		var jobs batchv1.JobList
		if err := k8sClient.List(ctx, &jobs, client.InNamespace(ns)); err != nil {
			return 0
		}
		return len(jobs.Items)
	}, timeout, interval).To(Equal(1))
	g.Eventually(func() string {
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), current); err != nil {
			return ""
		}
		for _, c := range current.Status.Conditions {
			if c.Type == "AIGridReady" {
				return string(c.Status)
			}
		}
		return ""
	}, timeout, interval).To(Equal("False"))
	pvc := &corev1.PersistentVolumeClaim{}
	pvcKey := client.ObjectKey{Name: "grid-enrollment-db", Namespace: ns}
	g.Eventually(func() error { return k8sClient.Get(ctx, pvcKey, pvc) }, timeout, interval).To(Succeed())
	g.Expect(pvc.OwnerReferences).To(BeEmpty())
	pvcUID := pvc.UID
	namespace := &corev1.Namespace{}
	g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: ns}, namespace)).To(Succeed())
	g.Expect(namespace.OwnerReferences).To(BeEmpty())
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "retained-test-identity", Namespace: ns}, Data: map[string][]byte{"identity": []byte("preserve-existing-material")}}
	g.Expect(k8sClient.Create(ctx, secret)).To(Succeed())
	g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), current)).To(Succeed())
	current.Spec.AIGrid.GridEnrollment = nil
	g.Expect(k8sClient.Update(ctx, current)).To(Succeed())
	g.Eventually(func() bool { return k8serr.IsNotFound(k8sClient.Get(ctx, deploymentKey, &appsv1.Deployment{})) }, timeout, interval).To(BeTrue())
	g.Expect(k8sClient.Get(ctx, gridKey, &appsv1.Deployment{})).To(Succeed())
	g.Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
	g.Expect(pvc.UID).To(Equal(pvcUID))
	retained := &corev1.Secret{}
	g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(secret), retained)).To(Succeed())
	g.Expect(retained.Data).To(Equal(secret.Data))
	g.Expect(k8sClient.Delete(ctx, current)).To(Succeed())
	g.Eventually(func() bool { return k8serr.IsNotFound(k8sClient.Get(ctx, gridKey, &appsv1.Deployment{})) }, timeout, interval).To(BeTrue())
	g.Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
	g.Expect(pvc.UID).To(Equal(pvcUID))
	g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: ns}, namespace)).To(Succeed())
}
