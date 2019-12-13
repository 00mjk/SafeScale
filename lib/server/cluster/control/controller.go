/*
 * Copyright 2018-2019, CS Systemes d'Information, http://www.c-s.fr
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package control

import (
	"fmt"
	"strings"
	"time"

	"github.com/CS-SI/SafeScale/lib/utils/scerr"
	"github.com/CS-SI/SafeScale/lib/utils/temporal"

	log "github.com/sirupsen/logrus"

	pb "github.com/CS-SI/SafeScale/lib"
	"github.com/CS-SI/SafeScale/lib/client"
	clusterapi "github.com/CS-SI/SafeScale/lib/server/cluster/api"
	clusterpropsv1 "github.com/CS-SI/SafeScale/lib/server/cluster/control/properties/v1"
	clusterpropsv2 "github.com/CS-SI/SafeScale/lib/server/cluster/control/properties/v2"
	"github.com/CS-SI/SafeScale/lib/server/cluster/enums/clusterstate"
	"github.com/CS-SI/SafeScale/lib/server/cluster/enums/property"
	"github.com/CS-SI/SafeScale/lib/server/cluster/identity"
	"github.com/CS-SI/SafeScale/lib/server/iaas"
	"github.com/CS-SI/SafeScale/lib/server/iaas/resources"
	srvutils "github.com/CS-SI/SafeScale/lib/server/utils"
	"github.com/CS-SI/SafeScale/lib/utils"
	"github.com/CS-SI/SafeScale/lib/utils/concurrency"
	"github.com/CS-SI/SafeScale/lib/utils/data"
	"github.com/CS-SI/SafeScale/lib/utils/retry"
	"github.com/CS-SI/SafeScale/lib/utils/serialize"
)

// Controller contains the information about a cluster
type Controller struct {
	identity.Identity
	Properties *serialize.JSONProperties `json:"properties,omitempty"` // Properties contains additional info about the cluster

	foreman  *foreman
	metadata *Metadata
	service  iaas.Service

	lastStateCollection time.Time

	concurrency.TaskedLock
}

// NewController ...
func NewController(svc iaas.Service) (src *Controller, err error) {
	defer scerr.OnPanic(&err)()

	metadata, err := NewMetadata(svc)
	if err != nil {
		return nil, err
	}
	return &Controller{
		service:    svc,
		metadata:   metadata,
		Properties: serialize.NewJSONProperties("clusters"),
		TaskedLock: concurrency.NewTaskedLock(),
	}, nil
}

func (c *Controller) replace(task concurrency.Task, src *Controller) (err error) {
	defer scerr.OnPanic(&err)()

	err = c.Lock(task)
	if err != nil {
		return err
	}
	defer func() {
		unlockErr := c.Unlock(task)
		if unlockErr != nil {
			log.Warn(unlockErr)
		}
		if err == nil && unlockErr != nil {
			err = unlockErr
		}
	}()

	//	(&c.Identity).Replace(&src.Identity)
	c.Properties = src.Properties
	return nil
}

// Restore restores full ability of a Cluster controller by binding with appropriate Foreman
func (c *Controller) Restore(task concurrency.Task, f Foreman) (err error) {
	if c == nil {
		return scerr.InvalidInstanceError()
	}
	defer scerr.OnPanic(&err)()

	if task == nil {
		return scerr.InvalidParameterError("task", "cannot be nil")
	}
	if f == nil {
		return scerr.InvalidParameterError("f", "cannot be nil")
	}
	if task == nil {
		return scerr.InvalidParameterError("task", "cannot be nil")
	}
	if f == nil {
		return scerr.InvalidParameterError("f", "cannot be nil")
	}

	err = c.Lock(task)
	if err != nil {
		return err
	}
	defer func() {
		unlockErr := c.Unlock(task)
		if unlockErr != nil {
			log.Warn(unlockErr)
		}
		if err == nil && unlockErr != nil {
			err = unlockErr
		}
	}()

	c.foreman = f.(*foreman)
	return nil
}

// Create creates the necessary infrastructure of the Cluster
func (c *Controller) Create(task concurrency.Task, req Request, f Foreman) (err error) {
	if c == nil {
		return scerr.InvalidInstanceError()
	}
	if f == nil {
		return scerr.InvalidParameterError("f", "cannot be nil")
	}
	if task == nil {
		task, err = concurrency.NewTask()
		if err != nil {
			return err
		}
	}

	tracer := concurrency.NewTracer(task, "", true).GoingIn()
	defer tracer.OnExitTrace()()
	defer temporal.NewStopwatch().OnExitLogInfo(
		fmt.Sprintf("Starting creation of infrastructure of cluster '%s'...", req.Name),
		fmt.Sprintf("Ending creation of infrastructure of cluster '%s'", req.Name),
	)()
	defer scerr.OnExitLogError(tracer.TraceMessage("creation of infrastructure of cluster:"), &err)()
	defer scerr.OnPanic(&err)()

	err = c.Lock(task)
	if err != nil {
		return err
	}

	// VPL: For now, always disable addition of feature proxycache-client
	err = c.Properties.LockForWrite(property.FeaturesV1).ThenUse(func(v interface{}) error {
		v.(*clusterpropsv1.Features).Disabled["proxycache"] = struct{}{}
		return nil
	})
	if err != nil {
		log.Errorf("failed to disable feature 'proxycache': %v", err)
		return err
	}
	// ENDVPL

	c.foreman = f.(*foreman)
	err = c.Unlock(task)
	if err != nil {
		return err
	}

	err = c.foreman.construct(task, req)
	return err
}

// GetService returns the service from the provider
func (c *Controller) GetService(task concurrency.Task) (srv iaas.Service) {
	var err error
	defer scerr.OnExitLogError(concurrency.NewTracer(task, "", concurrency.IsLogActive("Trace.Controller")).TraceMessage(""), &err)()

	if c == nil {
		err = scerr.InvalidInstanceError()
		return nil
	}

	ignoredErr := c.RLock(task)
	if ignoredErr != nil {
		err = ignoredErr
		return nil
	}
	defer func() {
		unlockErr := c.RUnlock(task)
		if unlockErr != nil {
			log.Warn(unlockErr)
			srv = nil
		}
		if err == nil && unlockErr != nil {
			err = unlockErr
		}
	}()
	return c.service
}

// GetIdentity returns the core data of a cluster
func (c *Controller) GetIdentity(task concurrency.Task) (id identity.Identity) {
	if task == nil {
		task = concurrency.RootTask()
	}

	var err error
	defer scerr.OnExitLogError(concurrency.NewTracer(task, "", concurrency.IsLogActive("Trace.Controller")).TraceMessage(""), &err)()

	if c == nil {
		err = scerr.InvalidInstanceError()
		return identity.Identity{}
	}

	ignoredErr := c.RLock(task)
	if ignoredErr != nil {
		err = ignoredErr
		return identity.Identity{}
	}
	defer func() {
		unlockErr := c.RUnlock(task)
		if unlockErr != nil {
			log.Warn(unlockErr)
			id = identity.Identity{}
		}
		if err == nil && unlockErr != nil {
			err = unlockErr
		}
	}()
	return c.Identity
}

// GetProperties returns the properties of the cluster
func (c *Controller) GetProperties(task concurrency.Task) (props *serialize.JSONProperties) {
	if task == nil {
		task = concurrency.RootTask()
	}

	var err error
	defer scerr.OnExitLogError(concurrency.NewTracer(task, "", concurrency.IsLogActive("Trace.Controller")).TraceMessage(""), &err)()

	if c == nil {
		err = scerr.InvalidInstanceError()
		return nil
	}

	ignoredErr := c.RLock(task)
	if ignoredErr != nil {
		err = ignoredErr
		return nil
	}
	defer func() {
		unlockErr := c.RUnlock(task)
		if unlockErr != nil {
			log.Warn(unlockErr)
			props = nil
		}
		if err == nil && unlockErr != nil {
			err = unlockErr
		}
	}()

	return c.Properties
}

// GetNetworkConfig returns the network configuration of the cluster
func (c *Controller) GetNetworkConfig(task concurrency.Task) (_ clusterpropsv2.Network, err error) {
	defer scerr.OnPanic(&err)()

	config := clusterpropsv2.Network{}
	if task == nil {
		task = concurrency.RootTask()
	}

	defer scerr.OnExitLogError(concurrency.NewTracer(task, "", concurrency.IsLogActive("Trace.Controller")).TraceMessage(""), &err)()

	if c == nil {
		return config, scerr.InvalidInstanceError()
	}

	if c.GetProperties(task).Lookup(property.NetworkV2) {
		_ = c.GetProperties(task).LockForRead(property.NetworkV2).ThenUse(func(v interface{}) error {
			config = *(v.(*clusterpropsv2.Network))
			return nil
		})
	} else {
		err = c.GetProperties(task).LockForRead(property.NetworkV1).ThenUse(func(v interface{}) error {
			networkV1, ok := v.(*clusterpropsv1.Network)
			if !ok {
				return fmt.Errorf("invalid metadata")
			}
			config = clusterpropsv2.Network{
				NetworkID:      networkV1.NetworkID,
				CIDR:           networkV1.CIDR,
				GatewayID:      networkV1.GatewayID,
				GatewayIP:      networkV1.GatewayIP,
				DefaultRouteIP: networkV1.GatewayIP,
				EndpointIP:     networkV1.PublicIP,
			}
			return nil
		})
	}

	return config, nil
}

// CountNodes returns the number of nodes in the cluster
func (c *Controller) CountNodes(task concurrency.Task) (_ uint, err error) {
	if c == nil {
		return 0, scerr.InvalidInstanceError()
	}
	defer scerr.OnPanic(&err)()

	if task == nil {
		task = concurrency.RootTask()
	}

	defer scerr.OnExitLogError(concurrency.NewTracer(task, "", concurrency.IsLogActive("Trace.Controller")).TraceMessage(""), &err)()

	var count uint

	err = c.GetProperties(task).LockForRead(property.NodesV2).ThenUse(func(v interface{}) error {
		count = uint(len(v.(*clusterpropsv2.Nodes).PrivateNodes))
		return nil
	})
	if err != nil {
		log.Debugf("failed to count nodes: %v", err)
		return count, err
	}
	return count, err
}

// ListMasters lists the names of the master nodes in the Cluster
func (c *Controller) ListMasters(task concurrency.Task) (nodelist []*clusterpropsv2.Node, err error) {
	defer scerr.OnPanic(&err)()

	if task == nil {
		task = concurrency.RootTask()
	}

	var list []*clusterpropsv2.Node
	err = c.GetProperties(task).LockForRead(property.NodesV2).ThenUse(func(v interface{}) error {
		list = v.(*clusterpropsv2.Nodes).Masters
		return nil
	})
	if err != nil {
		log.Errorf("failed to get list of master names: %v", err)
		return list, err
	}
	return list, err
}

// ListMasterNames lists the names of the master nodes in the Cluster
func (c *Controller) ListMasterNames(task concurrency.Task) (nodelist clusterapi.NodeList, err error) {
	defer scerr.OnPanic(&err)()
	if task == nil {
		task = concurrency.RootTask()
	}

	list := clusterapi.NodeList{}
	err = c.GetProperties(task).LockForRead(property.NodesV2).ThenUse(func(v interface{}) error {
		nodesV2 := v.(*clusterpropsv2.Nodes).Masters
		for _, v := range nodesV2 {
			list[v.NumericalID] = v.Name
		}
		return nil
	})
	if err != nil {
		// log.Errorf("failed to get list of master names: %v", err)
		return nil, err
	}
	return list, nil
}

// ListMasterIDs lists the IDs of the master nodes in the Cluster
func (c *Controller) ListMasterIDs(task concurrency.Task) (nodelist clusterapi.NodeList, err error) {
	defer scerr.OnPanic(&err)()
	if task == nil {
		task = concurrency.RootTask()
	}

	list := clusterapi.NodeList{}
	err = c.GetProperties(task).LockForRead(property.NodesV2).ThenUse(func(v interface{}) error {
		nodesV2 := v.(*clusterpropsv2.Nodes).Masters
		for _, v := range nodesV2 {
			list[v.NumericalID] = v.ID
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get list of master IDs: %v", err)
	}

	return list, nil
}

// ListMasterIPs lists the IP addresses of the master nodes in the Cluster
func (c *Controller) ListMasterIPs(task concurrency.Task) (nodelist clusterapi.NodeList, err error) {
	defer scerr.OnPanic(&err)()
	if task == nil {
		task = concurrency.RootTask()
	}

	list := clusterapi.NodeList{}
	err = c.GetProperties(task).LockForRead(property.NodesV2).ThenUse(func(v interface{}) error {
		nodesV2 := v.(*clusterpropsv2.Nodes).Masters
		for _, v := range nodesV2 {
			list[v.NumericalID] = v.PrivateIP
		}
		return nil
	})
	if err != nil {
		log.Errorf("failed to get list of master IPs: %v", err)
		return nil, err
	}
	return list, err
}

// ListNodes lists the nodes in the Cluster
func (c *Controller) ListNodes(task concurrency.Task) (nodelist []*clusterpropsv2.Node, err error) {
	defer scerr.OnPanic(&err)()
	if task == nil {
		task = concurrency.RootTask()
	}

	var list []*clusterpropsv2.Node
	err = c.GetProperties(task).LockForRead(property.NodesV2).ThenUse(func(v interface{}) error {
		list = v.(*clusterpropsv2.Nodes).PrivateNodes
		return nil
	})
	if err != nil {
		// log.Errorf("failed to get list of node IDs: %v", err)
		return nil, err
	}
	return list, nil
}

// ListNodeNames lists the names of the nodes in the Cluster
func (c *Controller) ListNodeNames(task concurrency.Task) (nodelist clusterapi.NodeList, err error) {
	defer scerr.OnPanic(&err)()
	if task == nil {
		task = concurrency.RootTask()
	}

	list := clusterapi.NodeList{}
	err = c.GetProperties(task).LockForRead(property.NodesV2).ThenUse(func(v interface{}) error {
		nodesV2 := v.(*clusterpropsv2.Nodes).PrivateNodes
		for _, v := range nodesV2 {
			list[v.NumericalID] = v.Name
		}
		return nil
	})
	if err != nil {
		// log.Errorf("failed to get list of node IDs: %v", err)
		return nil, err
	}
	return list, err
}

// ListNodeIDs lists the IDs of the nodes in the Cluster
func (c *Controller) ListNodeIDs(task concurrency.Task) (nodelist clusterapi.NodeList, err error) {
	defer scerr.OnPanic(&err)()

	if task == nil {
		task = concurrency.RootTask()
	}

	list := clusterapi.NodeList{}
	err = c.GetProperties(task).LockForRead(property.NodesV2).ThenUse(func(v interface{}) error {
		nodesV2 := v.(*clusterpropsv2.Nodes).PrivateNodes
		for _, v := range nodesV2 {
			list[v.NumericalID] = v.ID
		}
		return nil
	})
	if err != nil {
		// log.Errorf("failed to get list of node IDs: %v", err)
		return nil, err
	}
	return list, err
}

// ListNodeIPs lists the IP addresses of the nodes in the Cluster
func (c *Controller) ListNodeIPs(task concurrency.Task) (nodelist clusterapi.NodeList, err error) {
	defer scerr.OnPanic(&err)()

	if task == nil {
		task = concurrency.RootTask()
	}

	var list clusterapi.NodeList
	err = c.GetProperties(task).LockForRead(property.NodesV2).ThenUse(func(v interface{}) error {
		nodesV2 := v.(*clusterpropsv2.Nodes).PrivateNodes
		for _, v := range nodesV2 {
			list[v.NumericalID] = v.PrivateIP
		}
		return nil
	})
	if err != nil {
		// log.Errorf("failed to get list of node IP addresses: %v", err)
		return nil, err
	}
	return list, nil
}

// GetNode returns a node based on its ID
func (c *Controller) GetNode(task concurrency.Task, hostID string) (host *pb.Host, err error) {
	if c == nil {
		return nil, scerr.InvalidInstanceError()
	}
	defer scerr.OnPanic(&err)()

	if hostID == "" {
		return nil, scerr.InvalidParameterError("hostID", "cannot be empty string")
	}
	if task == nil {
		task = concurrency.RootTask()
	}

	tracer := concurrency.NewTracer(task, fmt.Sprintf("(%s)", hostID), true).GoingIn()
	defer tracer.OnExitTrace()()
	defer scerr.OnExitLogError(tracer.TraceMessage(""), &err)()

	found := false
	err = c.GetProperties(task).LockForRead(property.NodesV2).ThenUse(func(v interface{}) error {
		nodesV2 := v.(*clusterpropsv2.Nodes)
		found, _ = contains(nodesV2.PrivateNodes, hostID)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("failed to find node '%s' in Cluster '%s'", hostID, c.Name)
	}
	return client.New().Host.Inspect(hostID, temporal.GetExecutionTimeout())
}

// SearchNode tells if an host ID corresponds to a node of the Cluster
func (c *Controller) SearchNode(task concurrency.Task, hostID string) (found bool, err error) {
	defer scerr.OnPanic(&err)()

	if task == nil {
		task = concurrency.RootTask()
	}

	found = false
	_ = c.GetProperties(task).LockForRead(property.NodesV2).ThenUse(func(v interface{}) error {
		found, _ = contains(v.(*clusterpropsv2.Nodes).PrivateNodes, hostID)
		return nil
	})

	return found, err
}

// FindAvailableMaster returns the *propsv2.Node corresponding to the first available master for execution
func (c *Controller) FindAvailableMaster(task concurrency.Task) (_ *clusterpropsv2.Node, err error) {
	if c == nil {
		return nil, scerr.InvalidInstanceError()
	}
	defer scerr.OnPanic(&err)()

	if task == nil {
		task = concurrency.RootTask()
	}

	tracer := concurrency.NewTracer(task, "", true).GoingIn()
	defer tracer.OnExitTrace()()
	defer scerr.OnExitLogError(tracer.TraceMessage(""), &err)()

	found := false
	clientHost := client.New().Host
	masters, err := c.ListMasters(task)
	if err != nil {
		return nil, err
	}

	var (
		lastError error
		master    *clusterpropsv2.Node
	)
	for _, master = range masters {
		sshCfg, err := clientHost.SSHConfig(master.ID)
		if err != nil {
			lastError = err
			log.Errorf("failed to get ssh config for master '%s': %s", master.ID, err.Error())
			continue
		}

		_, err = sshCfg.WaitServerReady(task, "ready", temporal.GetConnectSSHTimeout())
		if err != nil {
			if _, ok := err.(*retry.ErrTimeout); ok {
				lastError = err
				continue
			}
			return nil, err
		}
		found = true
		break
	}
	if !found {
		return nil, fmt.Errorf("failed to find available master: %v", lastError)
	}
	return master, nil
}

// FindAvailableNode returns the propsv2.Node corresponding to first available node
func (c *Controller) FindAvailableNode(task concurrency.Task) (_ *clusterpropsv2.Node, err error) {
	if task == nil {
		task = concurrency.RootTask()
	}

	defer scerr.OnPanic(&err)()

	tracer := concurrency.NewTracer(task, "", true).GoingIn()
	defer tracer.OnExitTrace()()
	defer scerr.OnExitLogError(tracer.TraceMessage(""), &err)()

	clientHost := client.New().Host
	list, err := c.ListNodes(task)
	if err != nil {
		return nil, err
	}

	found := false
	var node *clusterpropsv2.Node
	for _, node = range list {
		sshCfg, err := clientHost.SSHConfig(node.ID)
		if err != nil {
			log.Errorf("failed to get ssh config of node '%s': %s", node.ID, err.Error())
			continue
		}

		_, err = sshCfg.WaitServerReady(task, "ready", temporal.GetConnectSSHTimeout())
		if err != nil {
			if _, ok := err.(*retry.ErrTimeout); ok {
				continue
			}
			return nil, err
		}
		found = true
		break
	}
	if !found {
		return nil, fmt.Errorf("failed to find available node")
	}
	return node, nil
}

// UpdateMetadata writes Cluster config in Object Storage
func (c *Controller) UpdateMetadata(task concurrency.Task, updatefn func() error) (err error) {
	if c == nil {
		return scerr.InvalidInstanceError()
	}
	defer scerr.OnPanic(&err)()

	if task == nil {
		task = concurrency.RootTask()
	}

	tracer := concurrency.NewTracer(task, "", true).WithStopwatch().GoingIn()
	defer tracer.OnExitTrace()()
	defer scerr.OnExitLogError(tracer.TraceMessage(""), &err)()

	err = c.Lock(task)
	if err != nil {
		return err
	}
	defer func() {
		unlockErr := c.Unlock(task)
		if unlockErr != nil {
			log.Warn(unlockErr)
		}
		if err == nil && unlockErr != nil {
			err = unlockErr
		}
	}()

	c.metadata.Acquire()
	defer c.metadata.Release()

	err = c.metadata.Reload(task)
	if err != nil {
		return err
	}
	if c.metadata.Written() {
		mc, err := c.metadata.Get()
		if err != nil {
			return err
		}
		err = c.replace(task, mc)
		if err != nil {
			return err
		}
	} else {
		c.metadata.Carry(task, c)
	}

	if updatefn != nil {
		err := updatefn()
		if err != nil {
			return err
		}
	}
	return c.metadata.Write()
}

// DeleteMetadata removes Cluster metadata from Object Storage
func (c *Controller) DeleteMetadata(task concurrency.Task) (err error) {
	if c == nil {
		return scerr.InvalidInstanceError()
	}

	defer scerr.OnPanic(&err)()

	if task == nil {
		task = concurrency.RootTask()
	}

	tracer := concurrency.NewTracer(task, "", true).WithStopwatch().GoingIn()
	defer tracer.OnExitTrace()()
	defer scerr.OnExitLogError(tracer.TraceMessage(""), &err)()

	err = c.Lock(task)
	if err != nil {
		return err
	}
	defer func() {
		unlockErr := c.Unlock(task)
		if unlockErr != nil {
			log.Warn(unlockErr)
		}
		if err == nil && unlockErr != nil {
			err = unlockErr
		}
	}()

	c.metadata.Acquire()
	defer c.metadata.Release()

	return c.metadata.Delete()
}

func contains(list []*clusterpropsv2.Node, hostID string) (bool, int) {
	var idx int
	found := false
	for i, v := range list {
		if v.ID == hostID {
			found = true
			idx = i
			break
		}
	}
	return found, idx
}

// Serialize converts cluster data to JSON
func (c *Controller) Serialize() ([]byte, error) {
	return serialize.ToJSON(c)
}

// Deserialize reads json code and reinstantiates cluster
func (c *Controller) Deserialize(buf []byte) error {
	return serialize.FromJSON(buf, c)
}

// AddNode adds one node
func (c *Controller) AddNode(task concurrency.Task, req *pb.HostDefinition) (string, error) {
	// No log enforcement here, delegated to AddNodes()

	hosts, err := c.AddNodes(task, 1, req)
	if err != nil {
		return "", err
	}
	return hosts[0], nil
}

// AddNodes adds <count> nodes
func (c *Controller) AddNodes(task concurrency.Task, count uint, req *pb.HostDefinition) (hosts []string, err error) {
	if c == nil {
		return nil, scerr.InvalidInstanceError()
	}
	defer scerr.OnPanic(&err)()

	if count == 0 {
		return nil, scerr.InvalidParameterError("count", "must be greater than zero")
	}
	if task == nil {
		task = concurrency.RootTask()
	}

	tracer := concurrency.NewTracer(task, fmt.Sprintf("(%d)", count), true).GoingIn()
	defer tracer.OnExitTrace()()
	defer scerr.OnExitLogError(tracer.TraceMessage(""), &err)()

	nodeDef := complementHostDefinition(req, pb.HostDefinition{})
	var hostImage string

	properties := c.GetProperties(concurrency.RootTask())
	if !properties.Lookup(property.DefaultsV2) {
		err := properties.LockForRead(property.DefaultsV1).ThenUse(func(v interface{}) error {
			defaultsV1 := v.(*clusterpropsv1.Defaults)
			return c.UpdateMetadata(task, func() error {
				return properties.LockForWrite(property.DefaultsV2).ThenUse(func(v interface{}) error {
					defaultsV2 := v.(*clusterpropsv2.Defaults)
					convertDefaultsV1ToDefaultsV2(defaultsV1, defaultsV2)
					return nil
				})
			})
		})
		if err != nil {
			return nil, err
		}
	}
	err = properties.LockForRead(property.DefaultsV2).ThenUse(func(v interface{}) error {
		defaultsV2 := v.(*clusterpropsv2.Defaults)
		sizing := srvutils.ToPBHostSizing(defaultsV2.NodeSizing)
		nodeDef.Sizing = &sizing
		hostImage = defaultsV2.Image
		return nil
	})

	if err != nil {
		return nil, err
	}

	if nodeDef.ImageId == "" {
		nodeDef.ImageId = hostImage
	}

	var (
		// nodeType    NodeType.Enum
		nodeTypeStr string
		errors      []string
	)
	netCfg, err := c.GetNetworkConfig(task)
	if err != nil {
		return nil, err
	}
	nodeDef.Network = netCfg.NetworkID

	timeout := temporal.GetExecutionTimeout() + time.Duration(count)*time.Minute

	var subtasks []concurrency.Task
	for i := uint(0); i < count; i++ {
		subtask, err := task.StartInSubTask(c.foreman.taskCreateNode, data.Map{
			"index": i + 1,
			// "type":    nodeType,
			"nodeDef": nodeDef,
			"timeout": timeout,
			"nokeep":  false,
		})
		if err != nil {
			return nil, err
		}
		subtasks = append(subtasks, subtask)
	}
	for _, s := range subtasks {
		result, err := s.Wait()
		if err != nil {
			errors = append(errors, err.Error())
		} else {
			hostName, ok := result.(string)
			if ok {
				if hostName != "" {
					hosts = append(hosts, hostName)
				}
			}
		}
	}
	hostClt := client.New().Host

	// Starting from here, delete nodes if exiting with error
	newHosts := hosts
	defer func() {
		if err != nil {
			if len(newHosts) > 0 {
				derr := hostClt.Delete(newHosts, temporal.GetExecutionTimeout())
				if derr != nil {
					log.Errorf("failed to delete nodes after failure to expand cluster")
				}
				err = scerr.AddConsequence(err, derr)
			}
		}
	}()

	if len(errors) > 0 {
		err = fmt.Errorf("errors occurred on %s node%s addition: %s", nodeTypeStr, utils.Plural(uint(len(errors))), strings.Join(errors, "\n"))
		return nil, err
	}

	// Now configure new nodes
	err = c.foreman.configureNodesFromList(task, hosts)
	if err != nil {
		return nil, err
	}

	// At last join nodes to cluster
	err = c.foreman.joinNodesFromList(task, hosts)
	if err != nil {
		return nil, err
	}

	return hosts, nil
}

func convertDefaultsV1ToDefaultsV2(defaultsV1 *clusterpropsv1.Defaults, defaultsV2 *clusterpropsv2.Defaults) {
	defaultsV2.Image = defaultsV1.Image
	defaultsV2.MasterSizing = resources.SizingRequirements{
		MinCores:    defaultsV1.MasterSizing.Cores,
		MinFreq:     defaultsV1.MasterSizing.CPUFreq,
		MinGPU:      defaultsV1.MasterSizing.GPUNumber,
		MinRAMSize:  defaultsV1.MasterSizing.RAMSize,
		MinDiskSize: defaultsV1.MasterSizing.DiskSize,
		Replaceable: defaultsV1.MasterSizing.Replaceable,
	}
	defaultsV2.NodeSizing = resources.SizingRequirements{
		MinCores:    defaultsV1.NodeSizing.Cores,
		MinFreq:     defaultsV1.NodeSizing.CPUFreq,
		MinGPU:      defaultsV1.NodeSizing.GPUNumber,
		MinRAMSize:  defaultsV1.NodeSizing.RAMSize,
		MinDiskSize: defaultsV1.NodeSizing.DiskSize,
		Replaceable: defaultsV1.NodeSizing.Replaceable,
	}
}

// GetState returns the current state of the Cluster
func (c *Controller) GetState(task concurrency.Task) (state clusterstate.Enum, err error) {
	if c == nil {
		return clusterstate.Unknown, scerr.InvalidInstanceError()
	}
	defer scerr.OnPanic(&err)()

	if task == nil {
		task = concurrency.RootTask()
	}

	tracer := concurrency.NewTracer(task, "", true).GoingIn()
	defer tracer.OnExitTrace()()
	defer scerr.OnExitLogError(tracer.TraceMessage(""), &err)()

	now := time.Now()
	var collectInterval time.Duration

	err = c.GetProperties(task).LockForRead(property.StateV1).ThenUse(func(v interface{}) error {
		stateV1 := v.(*clusterpropsv1.State)
		collectInterval = stateV1.StateCollectInterval
		state = stateV1.State
		return nil
	})
	if err != nil {
		return 0, err
	}
	if now.After(c.lastStateCollection.Add(collectInterval)) {
		return c.ForceGetState(task)
	}
	return state, nil
}

// ForceGetState returns the current state of the Cluster
// Uses the "maker" GetState from Foreman
func (c *Controller) ForceGetState(task concurrency.Task) (state clusterstate.Enum, err error) {
	if c == nil {
		return clusterstate.Unknown, scerr.InvalidInstanceError()
	}
	defer scerr.OnPanic(&err)()

	if task == nil {
		task = concurrency.RootTask()
	}

	tracer := concurrency.NewTracer(task, "", true).GoingIn()
	defer tracer.OnExitTrace()()
	defer scerr.OnExitLogError(tracer.TraceMessage(""), &err)()

	state, err = c.foreman.getState(task)
	if err != nil {
		return clusterstate.Unknown, err
	}

	err = c.UpdateMetadata(task, func() error {
		return c.GetProperties(task).LockForWrite(property.StateV1).ThenUse(func(v interface{}) error {
			stateV1 := v.(*clusterpropsv1.State)
			stateV1.State = state
			c.lastStateCollection = time.Now()
			return nil
		})
	})
	return state, err
}

// deleteMaster deletes the master specified by its ID
func (c *Controller) deleteMaster(task concurrency.Task, hostID string) (err error) {
	if c == nil {
		return scerr.InvalidInstanceError()
	}
	defer scerr.OnPanic(&err)()

	if hostID == "" {
		return scerr.InvalidParameterError("hostID", "cannot be empty string")
	}

	tracer := concurrency.NewTracer(task, fmt.Sprintf("(%s)", hostID), true).GoingIn()
	defer tracer.OnExitTrace()()
	defer scerr.OnExitLogError(tracer.TraceMessage(""), &err)()

	if task == nil {
		task = concurrency.RootTask()
	}

	// Removes master from cluster metadata
	var master *clusterpropsv2.Node
	err = c.UpdateMetadata(task, func() error {
		return c.Properties.LockForWrite(property.NodesV2).ThenUse(func(v interface{}) error {
			nodesV2 := v.(*clusterpropsv2.Nodes)
			found, idx := contains(nodesV2.Masters, hostID)
			if !found {
				return resources.ResourceNotFoundError("host", hostID)
			}
			master = nodesV2.Masters[idx]
			if idx < len(nodesV2.Masters)-1 {
				nodesV2.Masters = append(nodesV2.Masters[:idx], nodesV2.Masters[idx+1:]...)
			} else {
				nodesV2.Masters = nodesV2.Masters[:idx]
			}
			return nil
		})
	})
	if err != nil {
		return err
	}

	// Starting from here, restore master in cluster metadata if exiting with error
	defer func() {
		if err != nil {
			derr := c.UpdateMetadata(task, func() error {
				return c.Properties.LockForWrite(property.NodesV2).ThenUse(func(v interface{}) error {
					nodesV2 := v.(*clusterpropsv2.Nodes)
					nodesV2.Masters = append(nodesV2.Masters, master)
					return nil
				})
			})
			if derr != nil {
				log.Errorf("failed to restore node ownership in cluster")
			}
			err = scerr.AddConsequence(err, derr)
		}
	}()

	// Finally delete host
	err = client.New().Host.Delete([]string{master.ID}, temporal.GetLongOperationTimeout())
	if err != nil {
		return err
	}

	return nil
}

// DeleteLastNode deletes the last Agent node added
func (c *Controller) DeleteLastNode(task concurrency.Task, selectedMasterID string) (err error) {
	if c == nil {
		return scerr.InvalidInstanceError()
	}
	defer scerr.OnPanic(&err)()

	if task == nil {
		task = concurrency.RootTask()
	}

	tracer := concurrency.NewTracer(task, fmt.Sprintf("('%s')", selectedMasterID), true).GoingIn()
	defer tracer.OnExitTrace()()
	defer scerr.OnExitLogError(tracer.TraceMessage(""), &err)()

	var node *clusterpropsv2.Node

	// Removed reference of the node from cluster metadata
	err = c.GetProperties(task).LockForRead(property.NodesV2).ThenUse(func(v interface{}) error {
		nodesV2 := v.(*clusterpropsv2.Nodes)
		node = nodesV2.PrivateNodes[len(nodesV2.PrivateNodes)-1]
		return nil
	})
	if err != nil {
		return err
	}

	if selectedMasterID == "" {
		master, err := c.FindAvailableMaster(task)
		if err != nil {
			errDelNode := c.deleteNode(task, node, "")
			err = scerr.AddConsequence(err, errDelNode)
			return err
		}
		selectedMasterID = master.ID
	}

	return c.deleteNode(task, node, selectedMasterID)
}

// DeleteSpecificNode deletes the node specified by its ID
func (c *Controller) DeleteSpecificNode(task concurrency.Task, hostID string, selectedMasterID string) (err error) {
	if c == nil {
		return scerr.InvalidInstanceError()
	}
	defer scerr.OnPanic(&err)()

	if hostID == "" {
		return scerr.InvalidParameterError("hostID", "cannot be empty string")
	}
	if task == nil {
		task = concurrency.RootTask()
	}

	tracer := concurrency.NewTracer(task, fmt.Sprintf("(%s)", hostID), true).GoingIn()
	defer tracer.OnExitTrace()()
	defer scerr.OnExitLogError(tracer.TraceMessage(""), &err)()

	var (
		node *clusterpropsv2.Node
	)

	err = c.GetProperties(task).LockForRead(property.NodesV2).ThenUse(func(v interface{}) error {
		nodesV2 := v.(*clusterpropsv2.Nodes)
		var (
			idx   int
			found bool
		)
		if found, idx = contains(nodesV2.PrivateNodes, hostID); !found {
			return scerr.NotFoundError(fmt.Sprintf("failed to find node '%s'", hostID))
		}
		node = nodesV2.PrivateNodes[idx]
		return nil
	})
	if err != nil {
		return err
	}

	if selectedMasterID == "" {
		master, err := c.FindAvailableMaster(task)
		if err != nil {
			errDelNode := c.deleteNode(task, node, "")
			err = scerr.AddConsequence(err, errDelNode)
			return err
		}
		selectedMasterID = master.ID
	}

	return c.deleteNode(task, node, selectedMasterID)
}

// deleteNode deletes the node specified by its ID
func (c *Controller) deleteNode(task concurrency.Task, node *clusterpropsv2.Node, selectedMaster string) (err error) {
	if c == nil {
		return scerr.InvalidInstanceError()
	}
	defer scerr.OnPanic(&err)()

	if node == nil {
		return scerr.InvalidParameterError("node", "cannot be nil")
	}
	if task == nil {
		task = concurrency.RootTask()
	}

	tracer := concurrency.NewTracer(task, fmt.Sprintf("(%s, '%s')", node.Name, selectedMaster), true).GoingIn()
	defer tracer.OnExitTrace()()
	defer scerr.OnExitLogError(tracer.TraceMessage(""), &err)()

	// Removes node from cluster metadata (done before really deleting node to prevent operations on the node in parallel)
	err = c.UpdateMetadata(task, func() error {
		return c.Properties.LockForWrite(property.NodesV2).ThenUse(func(v interface{}) error {
			nodesV2 := v.(*clusterpropsv2.Nodes)
			length := len(nodesV2.PrivateNodes)
			_, idx := contains(nodesV2.PrivateNodes, node.ID)
			if idx < length-1 {
				nodesV2.PrivateNodes = append(nodesV2.PrivateNodes[:idx], nodesV2.PrivateNodes[idx+1:]...)
			} else {
				nodesV2.PrivateNodes = nodesV2.PrivateNodes[:idx]
			}
			return nil
		})
	})
	if err != nil {
		return err
	}

	// Starting from here, restore node in cluster metadata if exiting with error
	defer func() {
		if err != nil {
			derr := c.UpdateMetadata(task, func() error {
				return c.Properties.LockForWrite(property.NodesV2).ThenUse(func(v interface{}) error {
					nodesV2 := v.(*clusterpropsv2.Nodes)
					nodesV2.PrivateNodes = append(nodesV2.PrivateNodes, node)
					return nil
				})
			})
			if derr != nil {
				log.Errorf("failed to restore node ownership in cluster")
			}
			err = scerr.AddConsequence(err, derr)
		}
	}()

	// Leave node from cluster (ie leave Docker swarm), if selectedMaster isn't empty
	if selectedMaster != "" {
		err = c.foreman.leaveNodesFromList(task, []string{node.ID}, selectedMaster)
		if err != nil {
			return err
		}

		// Unconfigure node
		err = c.foreman.unconfigureNode(task, node.ID, selectedMaster)
		if err != nil {
			return err
		}
	}

	// Finally delete host
	err = client.New().Host.Delete([]string{node.ID}, temporal.GetLongOperationTimeout())
	if err != nil {
		if _, ok := err.(*scerr.ErrNotFound); ok {
			// host seems already deleted, so it's a success (handles the case where )
			return nil
		}
		return err
	}

	return nil
}

// Delete destroys everything related to the infrastructure built for the Cluster
func (c *Controller) Delete(task concurrency.Task) (err error) {
	if c == nil {
		return scerr.InvalidInstanceError()
	}
	if task == nil {
		return scerr.InvalidParameterError("task", "cannot be nil")
	}

	tracer := concurrency.NewTracer(task, "", true).GoingIn()
	defer tracer.OnExitTrace()()
	defer scerr.OnExitLogError(tracer.TraceMessage(""), &err)()
	defer scerr.OnPanic(&err)()

	// Updates metadata
	err = c.UpdateMetadata(task, func() error {
		return c.Properties.LockForWrite(property.StateV1).ThenUse(func(v interface{}) error {
			v.(*clusterpropsv1.State).State = clusterstate.Removed
			return nil
		})
	})
	if err != nil {
		return err
	}

	deleteNodeFunc := func(t concurrency.Task, params concurrency.TaskParameters) (concurrency.TaskResult, error) {
		hostID, ok := params.(string)
		if !ok {
			return nil, scerr.InvalidParameterError("params", "is not a string")
		}
		funcErr := c.DeleteSpecificNode(t, hostID, "")
		return nil, funcErr
	}
	deleteMasterFunc := func(t concurrency.Task, params concurrency.TaskParameters) (concurrency.TaskResult, error) {
		hostID, ok := params.(string)
		if !ok {
			return nil, scerr.InvalidParameterError("params", "is not a string")
		}
		funcErr := c.deleteMaster(t, hostID)
		return nil, funcErr
	}

	var cleaningErrors []error

	// Deletes the nodes
	list, err := c.ListNodeIDs(task)
	if err != nil {
		return err
	}
	if len(list) > 0 {
		var subtasks []concurrency.Task
		for _, v := range list {
			subtask, err := task.StartInSubTask(deleteNodeFunc, v)
			if err != nil {
				return err
			}
			subtasks = append(subtasks, subtask)
		}
		for _, s := range subtasks {
			_, subErr := s.Wait()
			if subErr != nil {
				cleaningErrors = append(cleaningErrors, subErr)
			}
		}
	}

	// Delete the Masters
	list, err = c.ListMasterIDs(task)
	if err != nil {
		return err
	}
	if len(list) > 0 {
		var subtasks []concurrency.Task
		for _, v := range list {
			subtask, err := task.StartInSubTask(deleteMasterFunc, v)
			if err != nil {
				return err
			}
			subtasks = append(subtasks, subtask)
		}
		for _, s := range subtasks {
			_, subErr := s.Wait()
			if subErr != nil {
				cleaningErrors = append(cleaningErrors, subErr)
			}
		}
	}

	// get access to metadata
	networkID := ""
	if c.GetProperties(task).Lookup(property.NetworkV2) {
		err = c.GetProperties(task).LockForRead(property.NetworkV2).ThenUse(func(v interface{}) error {
			networkID = v.(*clusterpropsv2.Network).NetworkID
			return nil
		})
	} else {
		err = c.GetProperties(task).LockForRead(property.NetworkV1).ThenUse(func(v interface{}) error {
			networkID = v.(*clusterpropsv1.Network).NetworkID
			return nil
		})
	}
	if err != nil {
		cleaningErrors = append(cleaningErrors, err)
		return scerr.ErrListError(cleaningErrors)
	}

	// Deletes the network
	clientNetwork := client.New().Network
	retryErr := retry.WhileUnsuccessfulDelay5SecondsTimeout(
		func() error {
			return clientNetwork.Delete([]string{networkID}, temporal.GetExecutionTimeout())
		},
		temporal.GetHostTimeout(),
	)
	if retryErr != nil {
		cleaningErrors = append(cleaningErrors, retryErr)
		return scerr.ErrListError(cleaningErrors)
	}

	// Deletes the metadata
	err = c.DeleteMetadata(task)
	if err != nil {
		cleaningErrors = append(cleaningErrors, err)
		return scerr.ErrListError(cleaningErrors)
	}

	err = c.Lock(task)
	if err != nil {
		return err
	}
	defer func() {
		unlockErr := c.Unlock(task)
		if unlockErr != nil {
			log.Warn(unlockErr)
		}
		if err == nil && unlockErr != nil {
			err = unlockErr
		}
	}()

	c.service = nil

	return scerr.ErrListError(cleaningErrors)
}

// Stop stops the Cluster is its current state is compatible
func (c *Controller) Stop(task concurrency.Task) (err error) {
	if c == nil {
		return scerr.InvalidInstanceError()
	}
	defer scerr.OnPanic(&err)()

	if task == nil {
		task = concurrency.RootTask()
	}

	tracer := concurrency.NewTracer(task, "", true).GoingIn()
	defer tracer.OnExitTrace()()
	defer scerr.OnExitLogError(tracer.TraceMessage(""), &err)()

	state, _ := c.ForceGetState(task)
	if state == clusterstate.Stopped {
		return nil
	}

	if state != clusterstate.Nominal && state != clusterstate.Degraded {
		return fmt.Errorf("failed to stop Cluster because of it's current state: %s", state.String())
	}

	// Updates metadata to mark the cluster as Stopping
	err = c.UpdateMetadata(task, func() error {
		return c.Properties.LockForWrite(property.StateV1).ThenUse(func(v interface{}) error {
			v.(*clusterpropsv1.State).State = clusterstate.Stopping
			return nil
		})
	})
	if err != nil {
		return err
	}

	// Stops the resources of the cluster

	var (
		nodes                         []*clusterpropsv2.Node
		masters                       []*clusterpropsv2.Node
		gatewayID, secondaryGatewayID string
	)
	err = c.GetProperties(task).LockForRead(property.NodesV2).ThenUse(func(v interface{}) error {
		nodesV2 := v.(*clusterpropsv2.Nodes)
		masters = nodesV2.Masters
		nodes = nodesV2.PrivateNodes
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to get list of hosts: %v", err)
	}
	if c.GetProperties(task).Lookup(property.NetworkV2) {
		err = c.GetProperties(task).LockForRead(property.NetworkV2).ThenUse(func(v interface{}) error {
			networkV2 := v.(*clusterpropsv2.Network)
			gatewayID = networkV2.GatewayID
			secondaryGatewayID = networkV2.SecondaryGatewayID
			return nil
		})
	} else {
		err = c.GetProperties(task).LockForRead(property.NetworkV1).ThenUse(func(v interface{}) error {
			gatewayID = v.(*clusterpropsv1.Network).GatewayID
			return nil
		})
	}
	if err != nil {
		return err
	}

	// Stop nodes
	taskGroup, err := concurrency.NewTaskGroup(task)
	if err != nil {
		return err
	}

	// FIXME introduce status

	for _, n := range nodes {
		_, err = taskGroup.Start(c.asyncStopHost, n.ID)
		if err != nil {
			return err
		}
	}
	// Stop masters
	for _, n := range masters {
		_, err = taskGroup.Start(c.asyncStopHost, n.ID)
		if err != nil {
			return err
		}
	}
	// Stop gateway(s)
	_, err = taskGroup.Start(c.asyncStopHost, gatewayID)
	if err != nil {
		return err
	}
	if secondaryGatewayID != "" {
		_, err = taskGroup.Start(c.asyncStopHost, secondaryGatewayID)
		if err != nil {
			return err
		}
	}

	_, err = taskGroup.Wait()
	if err != nil {
		return err
	}

	// Updates metadata to mark the cluster as Stopped
	return c.UpdateMetadata(task, func() error {
		return c.Properties.LockForWrite(property.StateV1).ThenUse(func(v interface{}) error {
			v.(*clusterpropsv1.State).State = clusterstate.Stopped
			state = clusterstate.Stopped
			return nil
		})
	})
}

func (c *Controller) asyncStopHost(task concurrency.Task, params concurrency.TaskParameters) (concurrency.TaskResult, error) {
	return nil, c.service.StopHost(params.(string))
}

// Start starts the Cluster
func (c *Controller) Start(task concurrency.Task) (err error) {
	if c == nil {
		return scerr.InvalidInstanceError()
	}
	defer scerr.OnPanic(&err)()

	if task == nil {
		task = concurrency.RootTask()
	}

	tracer := concurrency.NewTracer(task, "", true).GoingIn()
	defer tracer.OnExitTrace()()
	defer scerr.OnExitLogError(tracer.TraceMessage(""), &err)()

	state, err := c.ForceGetState(task)
	if err != nil {
		return err
	}
	if state == clusterstate.Nominal || state == clusterstate.Degraded || state == clusterstate.Starting {
		return nil
	}
	if state != clusterstate.Stopped {
		return fmt.Errorf("failed to start Cluster because of it's current state: %s", state.String())
	}

	// Updates metadata to mark the cluster as Starting
	err = c.UpdateMetadata(task, func() error {
		return c.Properties.LockForWrite(property.StateV1).ThenUse(func(v interface{}) error {
			v.(*clusterpropsv1.State).State = clusterstate.Starting
			return nil
		})
	})
	if err != nil {
		return err
	}

	// Starts the resources of the cluster

	var (
		nodes                         []*clusterpropsv2.Node
		masters                       []*clusterpropsv2.Node
		gatewayID, secondaryGatewayID string
	)
	err = c.GetProperties(task).LockForRead(property.NodesV2).ThenUse(func(v interface{}) error {
		nodesV2 := v.(*clusterpropsv2.Nodes)
		masters = nodesV2.Masters
		nodes = nodesV2.PrivateNodes
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to get list of hosts: %v", err)
	}
	if c.GetProperties(task).Lookup(property.NetworkV2) {
		err = c.GetProperties(task).LockForRead(property.NetworkV2).ThenUse(func(v interface{}) error {
			networkV2 := v.(*clusterpropsv2.Network)
			gatewayID = networkV2.GatewayID
			secondaryGatewayID = networkV2.SecondaryGatewayID
			return nil
		})
	} else {
		err = c.GetProperties(task).LockForRead(property.NetworkV1).ThenUse(func(v interface{}) error {
			gatewayID = v.(*clusterpropsv1.Network).GatewayID
			return nil
		})
	}
	if err != nil {
		return err
	}

	// FIXME introduce status

	// Start gateway(s)
	taskGroup, err := concurrency.NewTaskGroup(task)
	if err != nil {
		return err
	}
	_, err = taskGroup.Start(c.asyncStartHost, gatewayID)
	if err != nil {
		return err
	}
	if secondaryGatewayID != "" {
		_, err = taskGroup.Start(c.asyncStartHost, secondaryGatewayID)
		if err != nil {
			return err
		}
	}
	// Start masters
	for _, n := range masters {
		_, err = taskGroup.Start(c.asyncStopHost, n.ID)
		if err != nil {
			return err
		}
	}
	// Start nodes
	for _, n := range nodes {
		_, err = taskGroup.Start(c.asyncStopHost, n.ID)
		if err != nil {
			return err
		}
	}
	_, err = taskGroup.Wait()
	if err != nil {
		return err
	}

	// Updates metadata to mark the cluster as Stopped
	return c.UpdateMetadata(task, func() error {
		return c.Properties.LockForWrite(property.StateV1).ThenUse(func(v interface{}) error {
			v.(*clusterpropsv1.State).State = clusterstate.Nominal
			return nil
		})
	})
}

func (c *Controller) asyncStartHost(task concurrency.Task, params concurrency.TaskParameters) (concurrency.TaskResult, error) {
	return nil, c.service.StartHost(params.(string))
}

// // sanitize tries to rebuild manager struct based on what is available on ObjectStorage
// func (c *Controller) Sanitize(data *Metadata) error {

// 	core := data.Get()
// 	instance := &Cluster{
// 		Core:     core,
// 		metadata: data,
// 	}
// 	instance.reset()

// 	if instance.manager == nil {
// 		var mgw *providermetadata.Gateway
// 		mgw, err := providermetadata.LoadGateway(svc, instance.Core.NetworkID)
// 		if err != nil {
// 			return err
// 		}
// 		gw := mgw.Get()
// 		hm := providermetadata.NewHost(svc)
// 		hosts := []*resources.Host{}
// 		err = hm.Browse(func(h *resources.Host) error {
// 			if strings.HasPrefix(h.Name, instance.Core.Name+"-") {
// 				hosts = append(hosts, h)
// 			}
// 			return nil
// 		})
// 		if err != nil {
// 			return err
// 		}
// 		if len(hosts) == 0 {
// 			return fmt.Errorf("failed to find hosts belonging to cluster")
// 		}

// 		// We have hosts, fill the manager
// 		masterIDs := []string{}
// 		masterIPs := []string{}
// 		privateNodeIPs := []string{}
// 		publicNodeIPs := []string{}
// 		defaultNetworkIP := ""
// 		err = gw.Properties.LockForRead(HostProperty.NetworkV1).ThenUse(func(v interface{}) error {
// 			hostNetworkV1 := v.(*propsv1.HostNetwork)
// 			defaultNetworkIP = hostNetworkV1.IPv4Addresses[hostNetworkV1.DefaultNetworkID]
// 			for _, h := range hosts {
// 				if strings.HasPrefix(h.Name, instance.Core.Name+"-master-") {
// 					masterIDs = append(masterIDs, h.ID)
// 					masterIPs = append(masterIPs, defaultNetworkIP)
// 				} else if strings.HasPrefix(h.Name, instance.Core.Name+"-node-") {
// 					privateNodeIPs = append(privateNodeIPs, defaultNetworkIP)
// 				} else if strings.HasPrefix(h.Name, instance.Core.Name+"-pubnode-") {
// 					publicNodeIPs = append(privateNodeIPs, defaultNetworkIP)
// 				}
// 			}
// 			return nil
// 		})
// 		if err != nil {
// 			return fmt.Errorf("failed to update metadata of cluster '%s': %s", instance.Core.Name, err.Error())
// 		}

// 		newManager := &managerData{
// 			BootstrapID:      gw.ID,
// 			BootstrapIP:      defaultNetworkIP,
// 			MasterIDs:        masterIDs,
// 			MasterIPs:        masterIPs,
// 			PrivateNodeIPs:   privateNodeIPs,
// 			PublicNodeIPs:    publicNodeIPs,
// 			MasterLastIndex:  len(masterIDs),
// 			PrivateLastIndex: len(privateNodeIPs),
// 			PublicLastIndex:  len(publicNodeIPs),
// 		}
// 		log.Debugf("updating metadata...")
// 		err = instance.updateMetadata(func() error {
// 			instance.manager = newManager
// 			return nil
// 		})
// 		if err != nil {
// 			return fmt.Errorf("failed to update metadata of cluster '%s': %s", instance.Core.Name, err.Error())
// 		}
// 	}
// 	return nil
// }
