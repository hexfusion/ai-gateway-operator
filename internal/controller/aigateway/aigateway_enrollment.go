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

package aigateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"helm.sh/helm/v4/pkg/chart/common"
	commonutil "helm.sh/helm/v4/pkg/chart/common/util"
	"helm.sh/helm/v4/pkg/chart/loader"
	"helm.sh/helm/v4/pkg/engine"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"

	componentApi "github.com/opendatahub-io/ai-gateway-operator/api/components/v1alpha1"
	"github.com/opendatahub-io/ai-gateway-operator/pkg/controller/status"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/controller/conditions"
	odhtypes "github.com/opendatahub-io/opendatahub-operator/v2/pkg/controller/types"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/metadata/labels"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/resources"
)

const (
	enrollmentNamespace          = "ai-grid-enrollment"
	enrollmentName               = "grid-enrollment"
	enrollmentImageEnv           = "RELATED_IMAGE_ODH_PRAXIS_AI_GRID_ENROLLMENT_IMAGE"
	enrollmentDBImageEnv         = "RELATED_IMAGE_ODH_PRAXIS_AI_GRID_ENROLLMENT_DB_IMAGE"
	enrollmentImagePlaceholder   = "grid-enrollment-image:managed"
	enrollmentDBImagePlaceholder = "grid-enrollment-db-image:managed"
)

func enrollmentEnabled(obj *componentApi.AIGateway) bool {
	return obj.Spec.AIGrid.ManagementState == managedState &&
		obj.Spec.AIGrid.GridEnrollment != nil && obj.Spec.AIGrid.GridEnrollment.Enabled
}

// renderEnrollment uses the vendored chart without Helm release state or hook execution.
// The bootstrap Job is an owned workload; required Secret mounts gate service startup.
func (m *Module) renderEnrollment(_ context.Context, rr *odhtypes.ReconciliationRequest) error {
	obj, ok := rr.Instance.(*componentApi.AIGateway)
	if !ok {
		return errors.New("instance is not an AIGateway")
	}
	if !enrollmentEnabled(obj) {
		return nil
	}
	image, dbImage := os.Getenv(enrollmentImageEnv), os.Getenv(enrollmentDBImageEnv)
	if image == "" || dbImage == "" {
		return fmt.Errorf("grid enrollment requires %s and %s in the module payload", enrollmentImageEnv, enrollmentDBImageEnv)
	}
	rendered, err := renderEnrollmentChart(filepath.Join(m.cfg.ManifestsPath, "gridenrollment"), image, dbImage)
	if err != nil {
		return fmt.Errorf("render Grid enrollment: %w", err)
	}
	rr.Resources = append(rr.Resources, rendered...)
	return nil
}

func renderEnrollmentChart(path, image, dbImage string) (resources.UnstructuredList, error) {
	chart, err := loader.Load(path)
	if err != nil {
		return nil, fmt.Errorf("load chart: %w", err)
	}
	values, err := commonutil.ToRenderValues(chart, map[string]any{
		"fullnameOverride": enrollmentName,
		"image":            map[string]any{"repository": "grid-enrollment-image", "tag": "managed"},
		"db":               map[string]any{"builtin": map[string]any{"image": enrollmentDBImagePlaceholder}},
		"ca":               map[string]any{"bootstrap": map[string]any{"ttlSecondsAfterFinished": nil}},
		"route":            map[string]any{"enabled": false},
		"enrollment":       map[string]any{"csrRecord": map[string]any{"enabled": false}},
	}, common.ReleaseOptions{Name: enrollmentName, Namespace: enrollmentNamespace, Revision: 1, IsInstall: true}, nil)
	if err != nil {
		return nil, fmt.Errorf("chart values: %w", err)
	}
	files, err := engine.Render(chart, values)
	if err != nil {
		return nil, fmt.Errorf("chart templates: %w", err)
	}
	ns, err := resources.ToUnstructured(&corev1.Namespace{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{Name: enrollmentNamespace},
	})
	if err != nil {
		return nil, err
	}
	result := resources.UnstructuredList{*ns}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if !strings.HasSuffix(name, ".yaml") {
			continue
		}
		// Image substitution is restricted to chart placeholders, including init containers.
		content := strings.ReplaceAll(files[name], enrollmentImagePlaceholder, image)
		content = strings.ReplaceAll(content, enrollmentDBImagePlaceholder, dbImage)
		decoder := yamlutil.NewYAMLOrJSONDecoder(strings.NewReader(content), 4096)
		for {
			var resource unstructured.Unstructured
			if err := decoder.Decode(&resource); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return nil, fmt.Errorf("decode %s: %w", name, err)
			}
			if resource.GetKind() == "" {
				continue
			}
			annotations := resource.GetAnnotations()
			for key := range annotations {
				if strings.HasPrefix(key, "helm.sh/hook") {
					delete(annotations, key)
				}
			}
			resource.SetAnnotations(annotations)
			if resource.GetKind() == "Job" {
				// Job pod templates are immutable. Changed inputs create a new bootstrap Job.
				spec, err := json.Marshal(resource.Object["spec"])
				if err != nil {
					return nil, fmt.Errorf("hash bootstrap Job: %w", err)
				}
				hash := sha256.Sum256(spec)
				resource.SetName(fmt.Sprintf("%s-%x", resource.GetName(), hash[:6]))
			}
			result = append(result, resource)
		}
	}
	for i := range result {
		resourceLabels := result[i].GetLabels()
		if resourceLabels == nil {
			resourceLabels = map[string]string{}
		}
		resourceLabels[labels.ODH.Component(componentName)] = labels.True
		resourceLabels[labels.K8SCommon.PartOf] = componentName
		result[i].SetLabels(resourceLabels)
	}
	return result, nil
}

// reportEnrollmentStatus also gates aggregate readiness because enrollment runs
// outside the applications namespace checked by the framework's deployment action.
func (m *Module) reportEnrollmentStatus(ctx context.Context, rr *odhtypes.ReconciliationRequest) error {
	obj, ok := rr.Instance.(*componentApi.AIGateway)
	if !ok {
		return errors.New("instance is not an AIGateway")
	}
	if !enrollmentEnabled(obj) {
		return nil
	}
	message, err := enrollmentPending(ctx, rr)
	if err != nil {
		return err
	}
	if message != "" {
		for _, condition := range []string{status.ConditionAIGridReady, status.ConditionDeploymentsAvailable} {
			rr.Conditions.MarkFalse(condition, conditions.WithReason(status.SubModuleNotReadyReason), conditions.WithMessage("%s", message))
		}
	}
	return nil
}

func enrollmentPending(ctx context.Context, rr *odhtypes.ReconciliationRequest) (string, error) {
	if rr.Client == nil {
		return "Grid enrollment is waiting for its bootstrap Job", nil
	}
	foundJob := false
	for _, resource := range rr.Resources {
		if resource.GetKind() != "Job" || resource.GetNamespace() != enrollmentNamespace {
			continue
		}
		foundJob = true
		var job batchv1.Job
		err := rr.Client.Get(ctx, types.NamespacedName{Name: resource.GetName(), Namespace: enrollmentNamespace}, &job)
		if k8serr.IsNotFound(err) {
			return "Grid enrollment is waiting for its bootstrap Job", nil
		}
		if err != nil {
			return "", fmt.Errorf("read Grid enrollment bootstrap Job: %w", err)
		}
		complete := false
		for _, condition := range job.Status.Conditions {
			if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
				return "Grid enrollment certificate bootstrap failed; inspect the bootstrap Job", nil
			}
			complete = complete || condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue
		}
		if !complete {
			return "Grid enrollment certificate bootstrap is running", nil
		}
	}
	if !foundJob {
		return "Grid enrollment is waiting for its bootstrap Job", nil
	}
	for _, name := range []string{enrollmentName, enrollmentName + "-db"} {
		ready, err := deploymentAvailable(ctx, rr, name, enrollmentNamespace)
		if err != nil {
			return "", fmt.Errorf("check Grid enrollment Deployment %s: %w", name, err)
		}
		if !ready {
			return fmt.Sprintf("Grid enrollment Deployment %s is not available", name), nil
		}
	}
	return "", nil
}
