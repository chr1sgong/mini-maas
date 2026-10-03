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

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	maasv1alpha1 "github.com/chr1sgong/mini-maas/operator/api/v1alpha1"
)

var _ = Describe("Model Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			// Model 名允许点号（DNS 1123 subdomain），Service 名不允许，用来验证名称转换
			resourceName      = "test.resource"
			resourceNamespace = "default"
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: resourceNamespace,
		}
		model := &maasv1alpha1.Model{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind Model")
			err := k8sClient.Get(ctx, typeNamespacedName, model)
			if err != nil && errors.IsNotFound(err) {
				resource := &maasv1alpha1.Model{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: resourceNamespace,
					},
					Spec: maasv1alpha1.ModelSpec{
						ArtifactURI: "hf://Qwen/Qwen2.5-0.5B-Instruct",
						Engine:      "vllm",
						Image:       "vllm/vllm-openai:v0.30.0",
						EngineConfig: maasv1alpha1.EngineConfig{
							MaxModelLen: 8192,
							MaxNumSeqs:  64,
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &maasv1alpha1.Model{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance Model")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &ModelReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Creating the Deployment named after the model (subdomain name keeps dots)")
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "vllm-test.resource",
				Namespace: resourceNamespace,
			}, deployment)).To(Succeed())

			By("Creating the Service with an RFC 1123 label name (dots replaced by dashes)")
			service := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "vllm-test-resource",
				Namespace: resourceNamespace,
			}, service)).To(Succeed())

			By("Setting the model phase")
			Expect(k8sClient.Get(ctx, typeNamespacedName, model)).To(Succeed())
			Expect(model.Status.Phase).To(Equal("Pending"))
		})
	})
})
