/*
 * Licensed to the Apache Software Foundation (ASF) under one or more
 * contributor license agreements.  See the NOTICE file distributed with
 * this work for additional information regarding copyright ownership.
 * The ASF licenses this file to You under the Apache License, Version 2.0
 * (the "License"); you may not use this file except in compliance with
 * the License.  You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package event

import (
	"reflect"
)

import (
	gxset "github.com/dubbogo/gost/container/set"
)

import (
	"github.com/apache/dubbo-go/common"
	"github.com/apache/dubbo-go/common/constant"
	"github.com/apache/dubbo-go/common/extension"
	"github.com/apache/dubbo-go/common/logger"
	"github.com/apache/dubbo-go/common/observer"
	"github.com/apache/dubbo-go/registry"
	"github.com/apache/dubbo-go/remoting"
)

// The Service Discovery Changed  Event Listener
type ServiceInstancesChangedListener struct {
	registry.ServiceInstancesChangedListenerBase
	ServiceNames       *gxset.HashSet
	listeners          map[string]registry.NotifyListener
	serviceUrls        map[string][]*common.URL
	revisionToMetadata map[string]*common.MetadataInfo
	allInstances       map[string][]registry.ServiceInstance
}

func NewServiceInstancesChangedListener(services *gxset.HashSet) *ServiceInstancesChangedListener {
	return &ServiceInstancesChangedListener{
		ServiceNames:       services,
		listeners:          make(map[string]registry.NotifyListener),
		serviceUrls:        make(map[string][]*common.URL),
		revisionToMetadata: make(map[string]*common.MetadataInfo),
		allInstances:       make(map[string][]registry.ServiceInstance),
	}
}

// OnEvent on ServiceInstancesChangedEvent the service instances change event
func (lstn *ServiceInstancesChangedListener) OnEvent(e observer.Event) error {
	ce, ok := e.(*registry.ServiceInstancesChangedEvent)
	if !ok {
		return nil
	}
	var err error
	lstn.allInstances[ce.ServiceName] = ce.Instances
	revisionToInstances := make(map[string][]registry.ServiceInstance)
	newRevisionToMetadata := make(map[string]*common.MetadataInfo)
	localServiceToRevisions := make(map[*common.ServiceInfo]*gxset.HashSet)
	protocolRevisionsToUrls := make(map[string]map[*gxset.HashSet][]*common.URL)
	newServiceURLs := make(map[string][]*common.URL)

	for _, instances := range lstn.allInstances {
		for _, instance := range instances {
			if instance.GetMetadata() == nil {
				logger.Warnf("Instance metadata is nil: %s", instance.GetHost())
				continue
			}
			revision := instance.GetMetadata()[constant.EXPORTED_SERVICES_REVISION_PROPERTY_NAME]
			if "0" == revision {
				logger.Infof("Find instance without valid service metadata: %s", instance.GetHost())
				continue
			}
			subInstances := revisionToInstances[revision]
			if subInstances == nil {
				subInstances = make([]registry.ServiceInstance, 8)
			}
			revisionToInstances[revision] = append(subInstances, instance)
			metadataInfo := lstn.revisionToMetadata[revision]
			if metadataInfo == nil {
				metadataInfo, err = lstn.getMetadataInfo(instance, metadataInfo, revision)
				if err != nil {
					return err
				}
			}
			instance.SetServiceMetadata(metadataInfo)
			for _, service := range metadataInfo.Services {
				if localServiceToRevisions[service] == nil {
					localServiceToRevisions[service] = gxset.NewSet()
				}
				localServiceToRevisions[service].Add(revision)
			}

			newRevisionToMetadata[revision] = metadataInfo
		}
		lstn.revisionToMetadata = newRevisionToMetadata

		for serviceInstance, revisions := range localServiceToRevisions {
			revisionsToUrls := protocolRevisionsToUrls[serviceInstance.Protocol]
			if revisionsToUrls == nil {
				protocolRevisionsToUrls[serviceInstance.Protocol] = make(map[*gxset.HashSet][]*common.URL)
				revisionsToUrls = protocolRevisionsToUrls[serviceInstance.Protocol]
			}
			urls := revisionsToUrls[revisions]
			if urls != nil {
				newServiceURLs[serviceInstance.GetMatchKey()] = urls
			} else {
				urls = make([]*common.URL, 0, 8)
				for _, v := range revisions.Values() {
					r := v.(string)
					for _, i := range revisionToInstances[r] {
						if i != nil {
							urls = append(urls, i.ToURLs()...)
						}
					}
				}
				revisionsToUrls[revisions] = urls
				newServiceURLs[serviceInstance.GetMatchKey()] = urls
			}
		}
		lstn.serviceUrls = newServiceURLs

		for key, notifyListener := range lstn.listeners {
			urls := lstn.serviceUrls[key]
			for _, url := range urls {
				notifyListener.Notify(&registry.ServiceEvent{
					Action:  remoting.EventTypeAdd,
					Service: url,
				})
			}
		}
	}
	return nil
}

// getMetadataInfo get metadata info when METADATA_STORAGE_TYPE_PROPERTY_NAME is null
func (lstn *ServiceInstancesChangedListener) getMetadataInfo(instance registry.ServiceInstance, metadataInfo *common.MetadataInfo, revision string) (*common.MetadataInfo, error) {
	var metadataStorageType string
	if instance.GetMetadata() == nil {
		metadataStorageType = constant.DEFAULT_METADATA_STORAGE_TYPE
	} else {
		metadataStorageType = instance.GetMetadata()[constant.METADATA_STORAGE_TYPE_PROPERTY_NAME]
	}
	if metadataStorageType == constant.REMOTE_METADATA_STORAGE_TYPE {
		remoteMetadataServiceImpl, err := extension.GetRemoteMetadataService()
		if err != nil {
			return nil, err
		}
		metadataInfo, err = remoteMetadataServiceImpl.GetMetadata(instance)
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		proxyFactory := extension.GetMetadataServiceProxyFactory(constant.DEFAULT_KEY)
		metadataService := proxyFactory.GetProxy(instance)
		metadataInfo, err = metadataService.GetMetadataInfo(revision)
		if err != nil {
			return nil, err
		}
	}
	return metadataInfo, nil
}

// AddListenerAndNotify add notify listener and notify to listen service event
func (lstn *ServiceInstancesChangedListener) AddListenerAndNotify(serviceKey string, notify registry.NotifyListener) {
	lstn.listeners[serviceKey] = notify
	urls := lstn.serviceUrls[serviceKey]
	for _, url := range urls {
		notify.Notify(&registry.ServiceEvent{
			Action:  remoting.EventTypeAdd,
			Service: url,
		})
	}
}

// RemoveListener remove notify listener
func (lstn *ServiceInstancesChangedListener) RemoveListener(serviceKey string) {
	delete(lstn.listeners, serviceKey)
}

// Accept return true if the name is the same
func (lstn *ServiceInstancesChangedListener) Accept(e observer.Event) bool {
	if ce, ok := e.(*registry.ServiceInstancesChangedEvent); ok {
		return lstn.ServiceNames.Contains(ce.ServiceName)
	}
	return false
}

// GetPriority returns -1, it will be the first invoked listener
func (lstn *ServiceInstancesChangedListener) GetPriority() int {
	return -1
}

// GetEventType returns ServiceInstancesChangedEvent
func (lstn *ServiceInstancesChangedListener) GetEventType() reflect.Type {
	return reflect.TypeOf(&registry.ServiceInstancesChangedEvent{})
}
