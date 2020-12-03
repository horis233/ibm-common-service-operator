//
// Copyright 2020 IBM Corporation
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	utilyaml "github.com/ghodss/yaml"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv3 "github.com/IBM/ibm-common-service-operator/api/v3"
	"github.com/IBM/ibm-common-service-operator/controllers/bootstrap"
	util "github.com/IBM/ibm-common-service-operator/controllers/common"
	"github.com/IBM/ibm-common-service-operator/controllers/constant"
	"github.com/IBM/ibm-common-service-operator/controllers/deploy"
	"github.com/IBM/ibm-common-service-operator/controllers/rules"
	"github.com/IBM/ibm-common-service-operator/controllers/size"
)

// CommonServiceReconciler reconciles a CommonService object
type CommonServiceReconciler struct {
	client.Client
	client.Reader
	*deploy.Manager
	*bootstrap.Bootstrap
	Log    logr.Logger
	Scheme *runtime.Scheme
}

var ctx = context.Background()

func (r *CommonServiceReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {

	klog.Infof("Reconciling CommonService: %s", req.NamespacedName)

	// Fetch the CommonService instance
	instance := &apiv3.CommonService{}

	if err := r.Client.Get(ctx, req.NamespacedName, instance); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Init common service bootstrap resource
	if err := r.Bootstrap.InitResources(instance.Spec.ManualManagement); err != nil {
		klog.Errorf("Failed to initialize resources: %v", err)
		return ctrl.Result{}, err
	}

	newConfigs, err := r.getNewConfigs(req)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err = r.updateOpcon(newConfigs); err != nil {
		return ctrl.Result{}, err
	}

	klog.Infof("Finished reconciling CommonService: %s", req.NamespacedName)
	return ctrl.Result{}, nil
}

func (r *CommonServiceReconciler) getNewConfigs(req ctrl.Request) ([]interface{}, error) {
	cs := util.NewUnstructured("operator.ibm.com", "CommonService", "v3")
	if err := r.Client.Get(ctx, req.NamespacedName, cs); err != nil {
		return nil, err
	}
	var newConfigs []interface{}
	switch cs.Object["spec"].(map[string]interface{})["size"] {
	case "small":
		if cs.Object["spec"].(map[string]interface{})["services"] == nil {
			newConfigs = deepMerge(newConfigs, size.Small)
		} else {
			newConfigs = deepMerge(cs.Object["spec"].(map[string]interface{})["services"].([]interface{}), size.Small)
		}
	case "medium":
		if cs.Object["spec"].(map[string]interface{})["services"] == nil {
			newConfigs = deepMerge(newConfigs, size.Medium)
		} else {
			newConfigs = deepMerge(cs.Object["spec"].(map[string]interface{})["services"].([]interface{}), size.Medium)
		}
	case "large":
		if cs.Object["spec"].(map[string]interface{})["services"] == nil {
			newConfigs = deepMerge(newConfigs, size.Large)
		} else {
			newConfigs = deepMerge(cs.Object["spec"].(map[string]interface{})["services"].([]interface{}), size.Large)
		}
	default:
		if cs.Object["spec"].(map[string]interface{})["services"] != nil {
			newConfigs = cs.Object["spec"].(map[string]interface{})["services"].([]interface{})
		}
	}

	return newConfigs, nil
}

func (r *CommonServiceReconciler) updateOpcon(newConfigs []interface{}) error {
	opcon := util.NewUnstructured("operator.ibm.com", "OperandConfig", "v1alpha1")
	opconKey := types.NamespacedName{
		Name:      "common-service",
		Namespace: constant.MasterNamespace,
	}
	if err := r.Reader.Get(ctx, opconKey, opcon); err != nil {
		klog.Errorf("failed to get OperandConfig %s: %v", opconKey.String(), err)
		return err
	}
	services := opcon.Object["spec"].(map[string]interface{})["services"].([]interface{})
	// Convert rules string to slice
	rules, err := convertStringtoInterfaceSlice(rules.ConfigurationRules)
	if err != nil {
		return err
	}

	if err != nil {
		klog.Errorf("failed to convert string to slice: %v", err)
	}
	for _, service := range services {
		for index, size := range newConfigs {
			if service.(map[string]interface{})["name"].(string) == size.(map[string]interface{})["name"].(string) {
				for cr, spec := range service.(map[string]interface{})["spec"].(map[string]interface{}) {
					if size.(map[string]interface{})["spec"].(map[string]interface{})[cr] == nil {
						continue
					}
					service.(map[string]interface{})["spec"].(map[string]interface{})[cr] = mergeConfigwithRule(spec.(map[string]interface{}), size.(map[string]interface{})["spec"].(map[string]interface{})[cr].(map[string]interface{}), rules[index].(map[string]interface{})["spec"].(map[string]interface{})[cr].(map[string]interface{}))
				}
			}
		}
	}

	opcon.Object["spec"].(map[string]interface{})["services"] = services

	if err := r.Update(ctx, opcon); err != nil {
		klog.Errorf("failed to update OperandConfig %s: %v", opconKey.String(), err)
		return err
	}

	return nil
}

func convertStringtoInterfaceSlice(str string) ([]interface{}, error) {

	jsonSpec, err := utilyaml.YAMLToJSON([]byte(str))
	if err != nil {
		return nil, fmt.Errorf("failed to convert yaml to json: ", err)
	}

	// Create a slice
	var slice []interface{}
	// Convert sizes string to slice
	err = json.Unmarshal(jsonSpec, &slice)
	if err != nil {
		return nil, fmt.Errorf("failed to convert string to slice: ", err)
	}

	return slice, nil
}

func deepMerge(src []interface{}, dest string) []interface{} {

	// Convert sizes string to slice
	sizes, err := convertStringtoInterfaceSlice(dest)

	if err != nil {
		klog.Errorf("convert size to interface slice: ",err)
	}

	for _, configSize := range sizes {
		for _, config := range src {
			if config.(map[string]interface{})["name"].(string) == configSize.(map[string]interface{})["name"].(string) {
				if config == nil {
					continue
				}
				if configSize == nil {
					configSize = config
					continue
				}
				for cr, size := range mergeConfig(configSize.(map[string]interface{})["spec"].(map[string]interface{}), config.(map[string]interface{})["spec"].(map[string]interface{})) {
					configSize.(map[string]interface{})["spec"].(map[string]interface{})[cr] = size
				}
			}
		}
	}
	return sizes
}

// mergeConfig deep merge two configs
func mergeConfig(defaultMap map[string]interface{}, changedMap map[string]interface{}) map[string]interface{} {
	for key := range defaultMap {
		checkKeyBeforeMerging(key, defaultMap[key], changedMap[key], changedMap)
	}
	return changedMap
}

// mergeConfigwithRule deep merge two configs by specific rules
func mergeConfigwithRule(defaultMap map[string]interface{}, changedMap map[string]interface{}, rules map[string]interface{}) map[string]interface{} {
	for key := range defaultMap {
		checkKeyBeforeMergingwithRules(key, defaultMap[key], changedMap[key], rules[key], changedMap)
	}
	return changedMap
}

func checkKeyBeforeMerging(key string, defaultMap interface{}, changedMap interface{}, finalMap map[string]interface{}) {
	if !reflect.DeepEqual(defaultMap, changedMap) {
		switch defaultMap := defaultMap.(type) {
		case map[string]interface{}:
			//Check that the changed map value doesn't contain this map at all and is nil
			if changedMap == nil {
				finalMap[key] = defaultMap
			} else if _, ok := changedMap.(map[string]interface{}); ok { //Check that the changed map value is also a map[string]interface
				defaultMapRef := defaultMap
				changedMapRef := changedMap.(map[string]interface{})
				for newKey := range defaultMapRef {
					checkKeyBeforeMerging(newKey, defaultMapRef[newKey], changedMapRef[newKey], finalMap[key].(map[string]interface{}))
				}
			}
		default:
			//Check if the value was set, otherwise set it
			if changedMap == nil {
				finalMap[key] = defaultMap
			}
		}
	}
}

func checkKeyBeforeMergingwithRules(key string, defaultMap interface{}, changedMap interface{}, rules interface{},finalMap map[string]interface{}) {
	if !reflect.DeepEqual(defaultMap, changedMap) {
		switch defaultMap := defaultMap.(type) {
		case map[string]interface{}:
			//Check that the changed map value doesn't contain this map at all and is nil
			if changedMap == nil {
				finalMap[key] = defaultMap
			} else if _, ok := changedMap.(map[string]interface{}); ok { //Check that the changed map value is also a map[string]interface
				defaultMapRef := defaultMap
				changedMapRef := changedMap.(map[string]interface{})
				for newKey := range defaultMapRef {
					checkKeyBeforeMerging(newKey, defaultMapRef[newKey], changedMapRef[newKey], finalMap[key].(map[string]interface{}))
				}
			}
		default:
			//Check if the value was set, otherwise set it
			if changedMap == nil {
				finalMap[key] = defaultMap
			}
		}
	}
}

func (r *CommonServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv3.CommonService{}).
		Complete(r)
}
