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

package openstack

import (
	"errors"
	"fmt"
	"time"

	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/l7policies"
	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/listeners"
	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/loadbalancers"
	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/pools"
	"github.com/gophercloud/gophercloud/pagination"
	log "github.com/sirupsen/logrus"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	loadbalancerActiveInitDealy = 5 * time.Second
	loadbalancerActiveFactor    = 1
	loadbalancerActiveSteps     = 240

	activeStatus = "ACTIVE"
	errorStatus  = "ERROR"
)

var (
	// ErrNotFound is used to inform that the object is missing
	ErrNotFound = errors.New("failed to find object")

	// ErrMultipleResults is used when we unexpectedly get back multiple results
	ErrMultipleResults = errors.New("multiple results where only one expected")
)

func memberExists(members []pools.Member, addr string, port int) bool {
	for _, member := range members {
		if member.Address == addr && member.ProtocolPort == port {
			return true
		}
	}
	return false
}

func popMember(members []pools.Member, addr string, port int) []pools.Member {
	for i, member := range members {
		if member.Address == addr && member.ProtocolPort == port {
			members[i] = members[len(members)-1]
			members = members[:len(members)-1]
		}
	}

	return members
}

func ruleExists(rules []l7policies.Rule, ruleType, value string) bool {
	for _, rule := range rules {
		if rule.RuleType == ruleType && rule.Value == value {
			return true
		}
	}
	return false
}

func popRule(rules []l7policies.Rule, ruleType, value string) []l7policies.Rule {
	for i, rule := range rules {
		if rule.RuleType == ruleType && rule.Value == value {
			rules[i] = rules[len(rules)-1]
			rules = rules[:len(rules)-1]
		}
	}

	return rules
}

func getNodeAddressForLB(node *apiv1.Node) (string, error) {
	addrs := node.Status.Addresses
	if len(addrs) == 0 {
		return "", errors.New("no address found for host")
	}

	for _, addr := range addrs {
		if addr.Type == apiv1.NodeInternalIP {
			return addr.Address, nil
		}
	}

	return addrs[0].Address, nil
}

// GetLoadbalancerByName retrieves loadbalancer object
func (os *OpenStack) GetLoadbalancerByName(name string) (*loadbalancers.LoadBalancer, error) {
	opts := loadbalancers.ListOpts{
		Name: name,
	}
	pager := loadbalancers.List(os.octavia, opts)
	loadbalancerList := make([]loadbalancers.LoadBalancer, 0, 1)

	err := pager.EachPage(func(page pagination.Page) (bool, error) {
		v, err := loadbalancers.ExtractLoadBalancers(page)
		if err != nil {
			return false, err
		}
		loadbalancerList = append(loadbalancerList, v...)
		if len(loadbalancerList) > 1 {
			return false, ErrMultipleResults
		}
		return true, nil
	})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	if len(loadbalancerList) == 0 {
		return nil, ErrNotFound
	}

	return &loadbalancerList[0], nil
}

func (os *OpenStack) getListenerByName(name string, lbID string) (*listeners.Listener, error) {
	opts := listeners.ListOpts{
		Name:           name,
		LoadbalancerID: lbID,
	}
	pager := listeners.List(os.octavia, opts)
	listenerList := make([]listeners.Listener, 0, 1)

	err := pager.EachPage(func(page pagination.Page) (bool, error) {
		v, err := listeners.ExtractListeners(page)
		if err != nil {
			return false, err
		}
		listenerList = append(listenerList, v...)
		if len(listenerList) > 1 {
			return false, ErrMultipleResults
		}
		return true, nil
	})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	if len(listenerList) == 0 {
		return nil, ErrNotFound
	}

	return &listenerList[0], nil
}

func (os *OpenStack) getPoolByName(name string, lbID string) (*pools.Pool, error) {
	listenerPools := make([]pools.Pool, 0, 1)
	opts := pools.ListOpts{
		Name:           name,
		LoadbalancerID: lbID,
	}
	err := pools.List(os.octavia, opts).EachPage(func(page pagination.Page) (bool, error) {
		v, err := pools.ExtractPools(page)
		if err != nil {
			return false, err
		}
		listenerPools = append(listenerPools, v...)
		if len(listenerPools) > 1 {
			return false, ErrMultipleResults
		}
		return true, nil
	})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	if len(listenerPools) == 0 {
		return nil, ErrNotFound
	} else if len(listenerPools) > 1 {
		return nil, ErrMultipleResults
	}

	return &listenerPools[0], nil
}

// GetSharedPools retrives all shared pools belong to the loadbalancer.
func (os *OpenStack) GetSharedPools(lbID string) ([]pools.Pool, error) {
	var sharedPools []pools.Pool

	opts := pools.ListOpts{
		LoadbalancerID: lbID,
	}
	err := pools.List(os.octavia, opts).EachPage(func(page pagination.Page) (bool, error) {
		v, err := pools.ExtractPools(page)
		if err != nil {
			return false, err
		}
		for _, p := range v {
			// Make sure we only get pools with empty listeners list.
			if len(p.Listeners) == 0 {
				sharedPools = append(sharedPools, p)
			}
		}

		return true, nil
	})
	if err != nil {
		return nil, err
	}

	return sharedPools, nil
}

func (os *OpenStack) getMembersByPoolID(id string) ([]pools.Member, error) {
	var members []pools.Member
	err := pools.ListMembers(os.octavia, id, pools.ListMembersOpts{}).EachPage(func(page pagination.Page) (bool, error) {
		membersList, err := pools.ExtractMembers(page)
		if err != nil {
			return false, err
		}
		members = append(members, membersList...)

		return true, nil
	})
	if err != nil {
		return nil, err
	}

	return members, nil
}

func (os *OpenStack) getL7policy(name string, listenerID, poolID string) (*l7policies.L7Policy, error) {
	policies := make([]l7policies.L7Policy, 0, 1)
	opts := l7policies.ListOpts{
		Name:           name,
		ListenerID:     listenerID,
		RedirectPoolID: poolID,
	}
	err := l7policies.List(os.octavia, opts).EachPage(func(page pagination.Page) (bool, error) {
		v, err := l7policies.ExtractL7Policies(page)
		if err != nil {
			return false, err
		}
		policies = append(policies, v...)
		if len(policies) > 1 {
			return false, ErrMultipleResults
		}
		return true, nil
	})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	if len(policies) == 0 {
		return nil, ErrNotFound
	}

	return &policies[0], nil
}

// GetL7policies retrieves all l7 policies for the given listener.
func (os *OpenStack) GetL7policies(listenerID string) ([]l7policies.L7Policy, error) {
	var policies []l7policies.L7Policy
	opts := l7policies.ListOpts{
		ListenerID: listenerID,
	}
	err := l7policies.List(os.octavia, opts).EachPage(func(page pagination.Page) (bool, error) {
		v, err := l7policies.ExtractL7Policies(page)
		if err != nil {
			return false, err
		}
		policies = append(policies, v...)
		return true, nil
	})
	if err != nil {
		return nil, err
	}

	return policies, nil
}

func (os *OpenStack) getL7PolicyRule(policyID string, ruleType l7policies.RuleType, value string) error {
	return nil
}

func (os *OpenStack) getL7PolicyRules(policyID string) ([]l7policies.Rule, error) {
	//TODO
	var rules []l7policies.Rule
	// TODO
	return rules, nil
}

func (os *OpenStack) waitLoadbalancerActiveProvisioningStatus(loadbalancerID string) (string, error) {
	backoff := wait.Backoff{
		Duration: loadbalancerActiveInitDealy,
		Factor:   loadbalancerActiveFactor,
		Steps:    loadbalancerActiveSteps,
	}

	var provisioningStatus string
	err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		loadbalancer, err := loadbalancers.Get(os.octavia, loadbalancerID).Extract()
		if err != nil {
			return false, err
		}
		provisioningStatus = loadbalancer.ProvisioningStatus
		if loadbalancer.ProvisioningStatus == activeStatus {
			return true, nil
		} else if loadbalancer.ProvisioningStatus == errorStatus {
			return true, fmt.Errorf("loadbalancer has gone into ERROR state")
		} else {
			return false, nil
		}

	})

	if err == wait.ErrWaitTimeout {
		err = fmt.Errorf("loadbalancer failed to go into ACTIVE provisioning status within alloted time")
	}
	return provisioningStatus, err
}

// DeleteLoadbalancer deletes a loadbalancer with all its child objects.
func (os *OpenStack) DeleteLoadbalancer(lbID string) error {
	log.WithFields(log.Fields{"lbID": lbID}).Info("deleting loadbalancer with all child objects")

	err := loadbalancers.Delete(os.octavia, lbID, loadbalancers.DeleteOpts{Cascade: true}).ExtractErr()
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("error deleting loadbalancer %s: %v", lbID, err)
	}

	log.WithFields(log.Fields{"lbID": lbID}).Info("loadbalancer deleted")
	return nil
}

// EnsureLoadBalancer creates a loadbalancer in octavia if it does not exist, wait for the loadbalancer to be ACTIVE.
func (os *OpenStack) EnsureLoadBalancer(name string, subnetID string) (*loadbalancers.LoadBalancer, error) {
	loadbalancer, err := os.GetLoadbalancerByName(name)
	if err != nil {
		if err != ErrNotFound {
			return nil, fmt.Errorf("error getting loadbalancer %s: %v", name, err)
		}

		log.WithFields(log.Fields{"name": name}).Info("creating loadbalancer")

		createOpts := loadbalancers.CreateOpts{
			Name:        name,
			Description: "Created by Kubernetes",
			VipSubnetID: subnetID,
			Provider:    "octavia",
		}
		loadbalancer, err = loadbalancers.Create(os.octavia, createOpts).Extract()
		if err != nil {
			return nil, fmt.Errorf("error creating loadbalancer %v: %v", createOpts, err)
		}
	} else {
		log.WithFields(log.Fields{"name": name}).Info("loadbalancer exists")
	}

	_, err = os.waitLoadbalancerActiveProvisioningStatus(loadbalancer.ID)
	if err != nil {
		return nil, fmt.Errorf("error creating loadbalancer: %v", err)
	}

	log.WithFields(log.Fields{"name": name, "id": loadbalancer.ID}).Info("loadbalancer created")
	return loadbalancer, nil
}

// EnsureListener creates a loadbalancer listener in octavia if it does not exist, wait for the loadbalancer to be ACTIVE.
func (os *OpenStack) EnsureListener(name string, lbID string) (*listeners.Listener, error) {
	listener, err := os.getListenerByName(name, lbID)
	if err != nil {
		if err != ErrNotFound {
			return nil, fmt.Errorf("error getting listener %s: %v", name, err)
		}

		log.WithFields(log.Fields{"lb": lbID, "listenerName": name}).Info("creating listener")

		listener, err = listeners.Create(os.octavia, listeners.CreateOpts{
			Name:           name,
			Protocol:       "HTTP",
			ProtocolPort:   80, // Ingress Controller only supports http/https for now
			LoadbalancerID: lbID,
		}).Extract()
		if err != nil {
			return nil, fmt.Errorf("error creating listener: %v", err)
		}
	}

	_, err = os.waitLoadbalancerActiveProvisioningStatus(lbID)
	if err != nil {
		return nil, fmt.Errorf("error creating listener: %v", err)
	}

	log.WithFields(log.Fields{"lb": lbID, "listenerName": name}).Info("listener created")
	return listener, nil
}

// EnsurePoolMembers makes sure the pool with correct members exists or delete pool if deleted flag is true.
func (os *OpenStack) EnsurePoolMembers(deleted bool, poolName, lbID, listenerID string, servicePort *int, nodes []*apiv1.Node) (*string, error) {
	if deleted {
		pool, err := os.getPoolByName(poolName, lbID)
		if err != nil {
			if err != ErrNotFound {
				return nil, fmt.Errorf("error getting pool %s: %v", poolName, err)
			}

			log.WithFields(log.Fields{"name": poolName}).Info("pool not exists")
		} else {
			// Delete pool members first
			members, err := os.getMembersByPoolID(pool.ID)
			if err != nil {
				return nil, fmt.Errorf("error getting members for pool %s: %v", poolName, err)
			}

			if len(members) > 0 {
				log.WithFields(log.Fields{"members": members, "length": len(members)}).Debug("start to remove members")

				for _, m := range members {
					err := pools.DeleteMember(os.octavia, pool.ID, m.ID).ExtractErr()
					if err != nil && !isNotFound(err) {
						return nil, fmt.Errorf("error deleting member %s for pool %s: %v", m.ID, pool.ID, err)
					}
					_, err = os.waitLoadbalancerActiveProvisioningStatus(lbID)
					if err != nil {
						return nil, fmt.Errorf("error waiting for loadbalancer %s to be active: %v", lbID, err)
					}
				}

				log.WithFields(log.Fields{"members": members, "length": len(members)}).Debug("members removed")
			} else {
				log.Debug("pool does not contain members")
			}

			// delete pool
			err = pools.Delete(os.octavia, pool.ID).ExtractErr()
			if err != nil && !isNotFound(err) {
				return nil, fmt.Errorf("error deleting pool %s: %v", pool.ID, err)
			}
			_, err = os.waitLoadbalancerActiveProvisioningStatus(lbID)
			if err != nil {
				return nil, fmt.Errorf("error waiting for loadbalancer %s to be active: %v", lbID, err)
			}

			log.WithFields(log.Fields{"name": poolName, "ID": pool.ID}).Info("pool deleted")
		}

		return nil, nil
	}

	pool, err := os.getPoolByName(poolName, lbID)
	if err != nil {
		if err != ErrNotFound {
			return nil, fmt.Errorf("error getting pool %s: %v", poolName, err)
		}

		log.WithFields(log.Fields{"lb": lbID, "listenserID": listenerID, "poolName": poolName}).Info("creating pool")

		var opts pools.CreateOptsBuilder
		if lbID != "" {
			opts = pools.CreateOpts{
				Name:           poolName,
				Protocol:       "HTTP",
				LBMethod:       pools.LBMethodRoundRobin,
				LoadbalancerID: lbID,
				Persistence:    nil,
			}
		} else {
			opts = pools.CreateOpts{
				Name:        poolName,
				Protocol:    "HTTP",
				LBMethod:    pools.LBMethodRoundRobin,
				ListenerID:  listenerID,
				Persistence: nil,
			}
		}
		pool, err = pools.Create(os.octavia, opts).Extract()
		if err != nil {
			return nil, fmt.Errorf("error creating pool for listener %s: %v", listenerID, err)
		}
	}
	_, err = os.waitLoadbalancerActiveProvisioningStatus(lbID)
	if err != nil {
		return nil, fmt.Errorf("error waiting for loadbalancer %s to be active: %v", lbID, err)
	}

	log.WithFields(log.Fields{"lb": lbID, "listenserID": listenerID, "poolName": poolName, "pooID": pool.ID}).Info("pool created")

	// ensure pool members
	members, err := os.getMembersByPoolID(pool.ID)
	if err != nil {
		return nil, fmt.Errorf("error getting members for pool %s: %v", poolName, err)
	}

	for _, node := range nodes {
		addr, err := getNodeAddressForLB(node)
		if err != nil {
			// Node failure, do not create member
			log.WithFields(log.Fields{"node": node.Name, "poolName": poolName, "error": err}).Warn("failed to create LB pool member for node")
			continue
		}

		if !memberExists(members, addr, *servicePort) {
			log.WithFields(log.Fields{"addr": addr, "poolID": pool.ID, "port": *servicePort}).Info("adding new member to pool")

			_, err := pools.CreateMember(os.octavia, pool.ID, pools.CreateMemberOpts{
				ProtocolPort: *servicePort,
				Address:      addr,
			}).Extract()
			if err != nil {
				return nil, fmt.Errorf("error creating pool member %s: %v", addr, err)
			}

			_, err = os.waitLoadbalancerActiveProvisioningStatus(lbID)
			if err != nil {
				return nil, fmt.Errorf("error waiting for loadbalancer %s to be active: %v", lbID, err)
			}

			log.WithFields(log.Fields{"addr": addr, "poolID": pool.ID, "port": *servicePort}).Info("member added to pool")
		} else {
			log.WithFields(log.Fields{"addr": addr, "poolID": pool.ID, "port": *servicePort}).Debug("member exists")
			members = popMember(members, addr, *servicePort)
		}
	}

	// Delete obsolete members for this pool
	for _, member := range members {
		log.WithFields(log.Fields{"memberID": member.ID, "poolID": pool.ID}).Debug("deleting member")

		err = pools.DeleteMember(os.octavia, pool.ID, member.ID).ExtractErr()
		if err != nil && !isNotFound(err) {
			return nil, fmt.Errorf("error deleting member %s for pool %s: %v", member.ID, pool.ID, err)
		}
		_, err = os.waitLoadbalancerActiveProvisioningStatus(lbID)
		if err != nil {
			return nil, fmt.Errorf("error waiting for loadbalancer %s to be active: %v", lbID, err)
		}

		log.WithFields(log.Fields{"memberID": member.ID, "poolID": pool.ID}).Debug("member deleted")
	}

	return &pool.ID, nil
}

// EnsurePolicyRules creates l7 policies for listener or delete policies if deleted flag is true.
func (os *OpenStack) EnsurePolicyRules(deleted bool, policyName, lbID, listenerID, poolID, host, path string) error {
	if deleted {
		policy, err := os.getL7policy(policyName, listenerID, poolID)
		if err != nil {
			if err != ErrNotFound {
				return fmt.Errorf("error getting policy %s: %v", policyName, err)
			}
			log.WithFields(log.Fields{"lb": lbID, "listenserID": listenerID, "policyName": policyName}).Info("policy not exists")
			return nil
		}

		// Delete existing policy and rules
		existingRules, err := os.getL7PolicyRules(policy.ID)
		if err != nil {
			return fmt.Errorf("error getting rules for l7 policy %s: %v", policy.ID, err)
		}
		for _, r := range existingRules {
			log.WithFields(log.Fields{"ruleID": r.ID, "policyID": policy.ID}).Debug("deleting rule")

			// TODO delete rule

			_, err = os.waitLoadbalancerActiveProvisioningStatus(lbID)
			if err != nil {
				return fmt.Errorf("error waiting for loadbalancer %s to be active: %v", lbID, err)
			}

			log.WithFields(log.Fields{"ruleID": r.ID, "policyID": policy.ID}).Debug("rule deleted")
		}

		err = l7policies.Delete(os.octavia, policy.ID).ExtractErr()
		if err != nil && !isNotFound(err) {
			return fmt.Errorf("error deleting policy %s: %v", policy.ID, err)
		}
		_, err = os.waitLoadbalancerActiveProvisioningStatus(lbID)
		if err != nil {
			return fmt.Errorf("error waiting for loadbalancer %s to be active: %v", lbID, err)
		}

		log.WithFields(log.Fields{"lb": lbID, "listenserID": listenerID, "policyName": policyName}).Info("policy deleted")

		return nil
	}

	// Create or update l7 policy and its rules.

	policy, err := os.getL7policy(policyName, listenerID, poolID)
	if err != nil {
		if err != ErrNotFound {
			return fmt.Errorf("error getting policy %s: %v", policyName, err)
		}

		log.WithFields(log.Fields{"lb": lbID, "listenserID": listenerID, "policyName": policyName}).Info("creating policy")

		policy, err = l7policies.Create(os.octavia, l7policies.CreateOpts{
			Name:           policyName,
			ListenerID:     listenerID,
			Action:         l7policies.ActionRedirectToPool,
			Description:    "Created by kubernetes ingress",
			RedirectPoolID: poolID,
		}).Extract()
		if err != nil {
			return fmt.Errorf("error creating l7policy: %v", err)
		}
	}
	_, err = os.waitLoadbalancerActiveProvisioningStatus(lbID)
	if err != nil {
		return fmt.Errorf("error waiting for loadbalancer %s to be active: %v", lbID, err)
	}

	log.WithFields(log.Fields{"lb": lbID, "listenserID": listenerID, "policyName": policyName}).Info("policy created")

	// create l7 policy rules
	existingRules, err := os.getL7PolicyRules(policy.ID)
	if err != nil {
		return fmt.Errorf("error getting rules for l7 policy %s: %v", policy.ID, err)
	}

	if host != "" {
		if !ruleExists(existingRules, "HOST_NAME", host) {
			log.WithFields(log.Fields{"type": l7policies.TypeHostName, "host": host, "policyName": policyName}).Info("creating policy rule")

			// TODO create rule
		} else {
			existingRules = popRule(existingRules, "HOST_NAME", host)
		}

		_, err = os.waitLoadbalancerActiveProvisioningStatus(lbID)
		if err != nil {
			return fmt.Errorf("error waiting for loadbalancer %s to be active: %v", lbID, err)
		}

		log.WithFields(log.Fields{"type": l7policies.TypeHostName, "host": host, "policyName": policyName}).Info("policy rule created")
	}

	if path != "" {
		if !ruleExists(existingRules, "PATH", path) {
			log.WithFields(log.Fields{"type": l7policies.TypePath, "path": path, "policyName": policyName}).Info("creating policy rule")

			// TODO create rule
		} else {
			existingRules = popRule(existingRules, "PATH", path)
		}

		_, err = os.waitLoadbalancerActiveProvisioningStatus(lbID)
		if err != nil {
			return fmt.Errorf("error waiting for loadbalancer %s to be active: %v", lbID, err)
		}

		log.WithFields(log.Fields{"type": l7policies.TypePath, "path": path, "policyName": policyName}).Info("policy rule created")
	}

	// Delete obsolete rules
	for _, r := range existingRules {
		log.WithFields(log.Fields{"ruleID": r.ID, "policyID": policy.ID}).Debug("deleting rule")

		// TODO delete rule

		_, err = os.waitLoadbalancerActiveProvisioningStatus(lbID)
		if err != nil {
			return fmt.Errorf("error waiting for loadbalancer %s to be active: %v", lbID, err)
		}

		log.WithFields(log.Fields{"ruleID": r.ID, "policyID": policy.ID}).Debug("rule deleted")
	}

	return nil
}

// if host {
// 	_, err := c.osClient.CreateL7Rule(l7policies.TypeHostName, l7policies.CompareTypeEqual, host, policy.ID)
// 	if err != nil {
// 		return err
// 	}
// }
// if path.Path {
// 	_, err := c.osClient.CreateL7Rule(l7policies.TypePath, l7policies.CompareTypeStartWith, path.Path, policy.ID)
// 	if err != nil {
// 		return err
// 	}
// }

// // CreateL7Rule creates l7 rule for the policy, wait for the loadbalancer to be ACTIVE.
// func (os *OpenStack) CreateL7Rule(ruleType l7policies.RuleType, compareType l7policies.CompareType, value string, policyID string, lbID string) (*l7policies.Rule, error) {
// 	log.WithFields(log.Fields{"type": ruleType, "compareType": compareType, "value": value, "policyID": policyID}).Info("creating rule")

// 	rule, err := l7policies.CreateRule(os.octavia, policyID, l7policies.CreateRuleOpts{
// 		RuleType:    ruleType,
// 		CompareType: compareType,
// 		Value:       value,
// 	}).Extract()
// 	if err != nil {
// 		return nil, fmt.Errorf("error creating rule: %v", err)
// 	}

// 	_, err = os.waitLoadbalancerActiveProvisioningStatus(lbID)
// 	if err != nil {
// 		return nil, fmt.Errorf("error creating rule %s, failed to wait for lb to be ACTIVE: %v", rule.ID, err)
// 	}

// 	log.WithFields(log.Fields{"type": ruleType, "compareType": compareType, "value": value, "policyID": policyID, "ruleID": rule.ID}).Info("rule created")
// 	return rule, nil
// }
