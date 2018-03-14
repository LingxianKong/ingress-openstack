/*
Copyright 2018 Catalyst IT Ltd.

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
	"crypto/sha256"
	"fmt"

	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/l7policies"
	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/pools"
	apiv1 "k8s.io/api/core/v1"
	ext_v1beta1 "k8s.io/api/extensions/v1beta1"
)

func hash(data string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(data)))
}

func getResourceName(ing *ext_v1beta1.Ingress, suffix string) string {
	name := fmt.Sprintf("k8s-%s-%s-%s", ing.ObjectMeta.Namespace, ing.ObjectMeta.Name, suffix)
	return name
}

func policyExists(policies []l7policies.L7Policy, policyName string, poolID string) bool {
	for _, p := range policies {
		if p.Name == policyName && p.RedirectPoolID == poolID {
			return true
		}
	}
	return false
}

func popPolicy(policies []l7policies.L7Policy, policyName string) []l7policies.L7Policy {
	for i, p := range policies {
		if p.Name == policyName {
			policies[i] = policies[len(policies)-1]
			policies = policies[:len(policies)-1]
		}
	}
	return policies
}

func poolExists(curPools []pools.Pool, poolName string) bool {
	for _, p := range curPools {
		if p.Name == poolName {
			return true
		}
	}
	return false
}

func popPool(curPools []pools.Pool, poolName string) []pools.Pool {
	for i, p := range curPools {
		if p.Name == poolName {
			curPools[i] = curPools[len(curPools)-1]
			curPools = curPools[:len(curPools)-1]
		}
	}
	return curPools
}

func loadBalancerStatusDeepCopy(lb *apiv1.LoadBalancerStatus) *apiv1.LoadBalancerStatus {
	c := &apiv1.LoadBalancerStatus{}
	c.Ingress = make([]apiv1.LoadBalancerIngress, len(lb.Ingress))
	for i := range lb.Ingress {
		c.Ingress[i] = lb.Ingress[i]
	}
	return c
}

func loadBalancerStatusEqual(l, r *apiv1.LoadBalancerStatus) bool {
	return ingressSliceEqual(l.Ingress, r.Ingress)
}

func ingressSliceEqual(lhs, rhs []apiv1.LoadBalancerIngress) bool {
	if len(lhs) != len(rhs) {
		return false
	}
	for i := range lhs {
		if !ingressEqual(&lhs[i], &rhs[i]) {
			return false
		}
	}
	return true
}

func ingressEqual(lhs, rhs *apiv1.LoadBalancerIngress) bool {
	if lhs.IP != rhs.IP {
		return false
	}
	if lhs.Hostname != rhs.Hostname {
		return false
	}
	return true
}
