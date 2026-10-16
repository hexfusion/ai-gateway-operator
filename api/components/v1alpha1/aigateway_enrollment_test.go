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

package v1alpha1

import (
	"encoding/json"
	"testing"

	. "github.com/onsi/gomega"
)

func TestOptionalGridEnrollment(t *testing.T) {
	for _, input := range []string{
		`{"aiGrid":{"managementState":"Managed"}}`,
		`{"aiGrid":{"managementState":"Managed","grid-enrollment":{}}}`,
		`{"aiGrid":{"managementState":"Managed","grid-enrollment":{"enabled":false}}}`,
		`{"aiGrid":{"managementState":"Managed","grid-enrollment":{"enabled":true}}}`,
	} {
		t.Run(input, func(t *testing.T) {
			g := NewWithT(t)
			var spec AIGatewaySpec
			g.Expect(json.Unmarshal([]byte(input), &spec)).To(Succeed())
			copy := spec.DeepCopy()
			g.Expect(copy).To(Equal(&spec))
			if spec.AIGrid.GridEnrollment == nil {
				encoded, err := json.Marshal(spec)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(encoded)).NotTo(ContainSubstring("grid-enrollment"))
			} else {
				original := spec.AIGrid.GridEnrollment.Enabled
				copy.AIGrid.GridEnrollment.Enabled = !original
				g.Expect(spec.AIGrid.GridEnrollment.Enabled).To(Equal(original))
			}
		})
	}
}
