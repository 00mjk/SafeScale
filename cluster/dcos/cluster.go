package dcos
/*
* Copyright 2015-2018, CS Systemes d'Information, http://www.c-s.fr
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

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"html/template"
	"time"

	rice "github.com/GeertJohan/go.rice"
	clusterapi "github.com/SafeScale/cluster/api"
	"github.com/SafeScale/cluster/api/ClusterState"
	"github.com/SafeScale/cluster/api/NodeType"
	"github.com/SafeScale/providers"

	providerapi "github.com/SafeScale/providers/api"
)

//go:generate rice embed-go

//ClusterDefinition defines the values we want to keep in Object Storage
type ClusterDefinition struct {
	//Common cluster data
	Common clusterapi.Cluster

	//NetworkID is the network identifier where the cluster is created
	NetworkID string

	//BootstrapID is the identifier of the VM acting as bootstrap/upgrade server
	BootstrapID string

	//MasterIDs is a slice of VMIDs of the master
	MasterIDs []string

	//BootstrapIP contains the IP of the bootstrap server reachable by all master and agents
	BootstrapIP string

	//masterIPs contains a list of IP of the master servers
	MasterIPs []string

	//PublicAgentIDs is a slice of VMIDs of the public agents
	PublicAgentIDs []string

	//PrivateAgentIDs is a slice of VMIDs of the private agents
	PrivateAgentIDs []string

	//StateCollectInterval in seconds
	StateCollectInterval time.Duration
}

//Cluster is the object describing a cluster created by ClusterManagerAPI.CreateCluster
type Cluster struct {
	//Manager is the cluster manager used to create the cluster
	Manager *Manager

	//Definition contains data defining the cluster
	definition ClusterDefinition

	//LastStateCollect contains the date of the last state collection
	lastStateCollection time.Time

	//templateBox contains the rice box if needed
	templateBox *rice.Box
}

//getService returns a pointer to the infrastructure service of the cluster
func (c *Cluster) getService() *providers.Service {
	return c.Manager.GetService()
}

//GetName returns the name of the cluster
func (c *Cluster) GetName() string {
	return c.definition.Common.Name
}

//Start starts the cluster named 'name'
func (c *Cluster) Start() error {
	state, _ := c.ForceGetState()
	if state == ClusterState.Stopped {
		// 1st starts the masters
		// 2nd start the agents
		// 3nd start the nodes
	}
	return fmt.Errorf("Can't start an already started cluster")
}

//Stop stops the cluster is its current state is compatible
func (c *Cluster) Stop() error {
	state, _ := c.ForceGetState()
	if state != ClusterState.Stopped && state != ClusterState.Creating {
		return c.Stop()
	}
	return nil
}

//GetState returns the current state of the cluster
func (c *Cluster) GetState() (ClusterState.Enum, error) {
	now := time.Now()
	if now.After(c.lastStateCollection.Add(c.definition.StateCollectInterval)) {
		return c.ForceGetState()
	}
	return c.definition.Common.State, nil
}

//ForceGetState returns the current state of the cluster
// This method will trigger a effective state collection at each call
func (c *Cluster) ForceGetState() (ClusterState.Enum, error) {
	// Do effective state collection
	return ClusterState.Error, nil
}

//AddNode adds a node
func (c *Cluster) AddNode(nodeType NodeType.Enum, req providerapi.VMRequest) (*clusterapi.Node, error) {
	switch nodeType {
	case NodeType.Bootstrap:
		return c.addBootstrapNode(req)
	case NodeType.Master:
		if c.definition.Common.State != ClusterState.Creating {
			return nil, fmt.Errorf("The DCOS flavor of Cluster doesn't allow to add master node after initial setup")
		}
		node, err := c.addAgentNode(req, nodeType)
		if err != nil {
			return nil, err
		}
		err = c.initializeAgentNode(node.ID, nodeType)
		if err != nil {
			return nil, err
		}
		return node, nil
	case NodeType.PrivateAgent:
		node, err := c.addAgentNode(req, nodeType)
		if err != nil {
			return nil, err
		}
		err = c.initializeAgentNode(node.ID, nodeType)
		if err != nil {
			return nil, err
		}
		return node, nil
	}
	return nil, fmt.Errorf("unknown node type")
}

//addBootstrapNode
func (c *Cluster) addBootstrapNode(req providerapi.VMRequest) (*clusterapi.Node, error) {
	svc := c.getService()

	req.Name = c.definition.Common.Name + ".dcos.bootstrap"
	req.NetworkIDs = []string{c.definition.NetworkID}
	req.PublicIP = true
	req.KeyPair = c.definition.Common.Keypair
	bootstrapVM, err := svc.CreateVM(req)
	if err != nil {
		return nil, fmt.Errorf("failed to create bootstrap server: %s", err.Error())
	}

	// Prepares Bootstrap server
	err = c.configureBootstrap(bootstrapVM)
	if err != nil {
		svc.DeleteVM(bootstrapVM.ID)
		return nil, fmt.Errorf("failed to prepare bootstrap server: %s", err.Error())
	}

	c.definition.BootstrapID = bootstrapVM.ID
	c.definition.BootstrapIP = bootstrapVM.PrivateIPsV4[0]

	// Update cluster definition in Object Storage
	err = c.SaveDefinition()
	if err != nil {
		// Removes the ID we just added to the cluster struct
		c.definition.BootstrapID = ""
		svc.DeleteVM(bootstrapVM.ID)
		return nil, fmt.Errorf("failed to update cluster definition: %s", err.Error())
	}

	return c.toNode(NodeType.Master, req.TemplateID, bootstrapVM), nil
}

//addMasterNode adds a master node
func (c *Cluster) addMasterNode(req providerapi.VMRequest) (*clusterapi.Node, error) {
	svc := c.getService()

	i := len(c.definition.MasterIDs) + 1
	req.Name = c.definition.Common.Name + ".dcos.master-" + string(i)
	req.NetworkIDs = []string{c.definition.NetworkID}
	req.PublicIP = false
	req.KeyPair = c.definition.Common.Keypair
	masterVM, err := svc.CreateVM(req)
	if err != nil {
		return nil, fmt.Errorf("failed to create master server %d: %s", i, err.Error())
	}
	err = c.configureMaster(masterVM)
	if err != nil {
		svc.DeleteVM(masterVM.ID)
		return nil, fmt.Errorf("failed to install master node '%d': %s", i, err.Error())
	}

	// Registers the new Master in the cluster struct
	c.definition.MasterIDs = append(c.definition.MasterIDs, masterVM.ID)
	c.definition.MasterIPs = append(c.definition.MasterIPs, masterVM.PrivateIPsV4[0])

	// Update cluster definition in Object Storage
	err = c.SaveDefinition()
	if err != nil {
		// Removes the ID we just added to the cluster struct
		c.definition.MasterIDs = c.definition.MasterIDs[:len(c.definition.MasterIDs)-1]
		svc.DeleteVM(masterVM.ID)
		return nil, fmt.Errorf("failed to update cluster definition: %s", err.Error())
	}

	return c.toNode(NodeType.Master, req.TemplateID, masterVM), nil
}

//toNode converts a VM struct to a Node struct
func (c *Cluster) toNode(nodeType NodeType.Enum, tmplID string, vm *providerapi.VM) *clusterapi.Node {
	return &clusterapi.Node{
		ID:         vm.ID,
		TemplateID: tmplID,
		State:      vm.State,
		Type:       nodeType,
	}
}

//addAgentNode adds a Public Agent Node to the cluster
func (c *Cluster) addAgentNode(req providerapi.VMRequest, nodeType NodeType.Enum) (*clusterapi.Node, error) {
	svc := c.getService()

	var publicIP bool
	coreName := "-agent"
	if nodeType == NodeType.PublicAgent {
		publicIP = true
		coreName = "public" + coreName
	} else {
		publicIP = false
		coreName = "private" + coreName
	}

	i := len(c.definition.PublicAgentIDs) + 1
	req.PublicIP = publicIP
	req.NetworkIDs = []string{c.definition.NetworkID}
	req.Name = c.definition.Common.Name + ".dcos." + coreName + "-" + string(i)
	req.KeyPair = c.definition.Common.Keypair
	agentVM, err := svc.CreateVM(req)
	if err != nil {
		return nil, fmt.Errorf("Failed to create Public Agent node '%s': %s", req.Name, err.Error())
	}

	// Installs DCOS on agent node
	err = c.configureAgent(agentVM, nodeType)
	if err != nil {
		svc.DeleteVM(agentVM.ID)
		return nil, fmt.Errorf("Failed to install DCOS on Agent Node: %s", err.Error())

	}

	// Registers the new Agent in the cluster struct
	if nodeType == NodeType.PublicAgent {
		c.definition.PublicAgentIDs = append(c.definition.PublicAgentIDs, agentVM.ID)
	} else {
		c.definition.PrivateAgentIDs = append(c.definition.PrivateAgentIDs, agentVM.ID)
	}

	// Update cluster definition in Object Storage
	err = c.SaveDefinition()
	if err != nil {
		// Removes the ID we just added to the cluster struct
		if nodeType == NodeType.PublicAgent {
			c.definition.PublicAgentIDs = c.definition.PublicAgentIDs[:len(c.definition.PublicAgentIDs)-1]
		} else {
			c.definition.PrivateAgentIDs = c.definition.PrivateAgentIDs[:len(c.definition.PrivateAgentIDs)-1]
		}
		svc.DeleteVM(agentVM.ID)
		return nil, fmt.Errorf("failed to update cluster definition: %s", err.Error())
	}

	return c.toNode(nodeType, req.TemplateID, agentVM), nil
}

//configureBootstrap prepares the bootstrap server for duty
func (c *Cluster) configureBootstrap(targetVM *providerapi.VM) error {
	_, err := c.executeScript(targetVM, "dcos_install_bootstrap_node.sh", map[string]interface{}{
		"BootstrapIP":   c.definition.BootstrapIP,
		"BootstrapPort": "80",
		"ClusterName":   c.definition.Common.Name,
		"MasterIPs":     c.definition.MasterIPs,
		"DNSServerIPs":  []string{"", ""},
	})
	return err
}

//configureMaster installs and configures DCOS master on targetVM
func (c *Cluster) configureMaster(targetVM *providerapi.VM) error {
	svc := c.getService()
	bootstrapVM, err := svc.GetVM(c.definition.BootstrapID)
	if err != nil {
		return fmt.Errorf("failed to load data of bootstrap server: %s", err.Error())
	}

	_, err = c.executeScript(targetVM, "dcos_install_master_node.sh", map[string]interface{}{
		"bootstrap_ip":   bootstrapVM.AccessIPv4,
		"bootstrap_port": "80",
	})
	return err
}

//configureAgent installs and configure DCOS agent on targetVM
func (c *Cluster) configureAgent(targetVM *providerapi.VM, nodeType NodeType.Enum) error {
	svc := c.getService()
	bootstrapVM, err := svc.GetVM(c.definition.BootstrapID)
	if err != nil {
		return fmt.Errorf("failed to load data of bootstrap server: %s", err.Error())
	}

	var typeStr string
	if nodeType == NodeType.PublicAgent {
		typeStr = "yes"
	} else {
		typeStr = "no"
	}

	_, err = c.executeScript(targetVM, "dcos_install_agent_node.sh", map[string]interface{}{
		"public_node":    typeStr,
		"bootstrap_ip":   bootstrapVM.AccessIPv4,
		"bootstrap_port": "80",
	})
	return err
}

//executeScript executes the script template with the parameters on targetVM
func (c *Cluster) executeScript(targetVM *providerapi.VM, script string, data map[string]interface{}) (*string, error) {
	svc := c.getService()

	ssh, err := svc.GetSSHConfig(targetVM.ID)
	if err != nil {
		return nil, fmt.Errorf("[%s] error reading SSHConfig: %s", targetVM.Name, err.Error())
	}
	ssh.WaitServerReady(60 * time.Second)

	// find the rice.Box
	if c.templateBox == nil {
		b, err := rice.FindBox("../dcos/scripts")
		if err != nil {
			return nil, fmt.Errorf("[%s] error loading script folder: %s", targetVM.Name, err.Error())
		}
		c.templateBox = b
	}
	// get file contents as string
	templateString, err := c.templateBox.String(script)
	if err != nil {
		return nil, fmt.Errorf("[%s] error loading script template: %s", targetVM.Name, err.Error())
	}
	// parse and execute the template
	tmplCmd, err := template.New("cmd").Parse(templateString)
	if err != nil {
		return nil, fmt.Errorf("[%s] error parsing script template: %s", targetVM.Name, err.Error())
	}

	dataBuffer := bytes.NewBufferString("")
	err = tmplCmd.Execute(dataBuffer, data)
	if err != nil {
		return nil, fmt.Errorf("[%s] error realizing script template: %s", targetVM.Name, err.Error())
	}
	cmd := dataBuffer.String()
	cmdResult, err := ssh.Command(cmd)
	if err != nil {
		return nil, fmt.Errorf("[%s] error executing script '%s': %s", targetVM.Name, script, err.Error())
	}
	out, err := cmdResult.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("[%s] error fetching output of script '%s': %s", targetVM.Name, script, err.Error())
	}

	strOut := string(out)
	return &strOut, nil
}

//Initialize initializes DCOS
func (c *Cluster) Initialize() error {
	if c.definition.Common.State != ClusterState.Created {
		return fmt.Errorf("cluster '%s' isn't in a state allowing initialization", c.definition.Common.Name)
	}

	c.definition.Common.State = ClusterState.Initializing
	err := c.SaveDefinition()
	if err != nil {
		c.definition.Common.State = ClusterState.Created
		return fmt.Errorf("failed to save cluster state: %s", err.Error())
	}

	svc := c.getService()

	// Installs DCOS on the masters first
	data := map[string]interface{}{
		"bootstrapIP":   c.definition.BootstrapIP,
		"bootstrapPort": "80",
		"nodeType":      "master",
	}
	for _, m := range c.definition.MasterIDs {
		vm, err := svc.GetVM(m)
		if err != nil {
			c.definition.Common.State = ClusterState.Created
			return fmt.Errorf("failed to get information about master: %s", err.Error())
		}
		_, err = c.executeScript(vm, "dcos_initialize_cluster_node.sh", data)
		if err != nil {
			c.definition.Common.State = ClusterState.Created
			return fmt.Errorf("failed to initialize master '%s': %s", vm.Name, err.Error())
		}
	}

	// Installs DCOS on the Private Agents next
	data["nodeType"] = "slave"
	for _, a := range c.definition.PrivateAgentIDs {
		err := c.initializeAgentNode(a, NodeType.PrivateAgent)
		if err != nil {
			c.definition.Common.State = ClusterState.Created
			return fmt.Errorf("failed to initialize private agent: %s", err.Error())
		}
	}

	// Installs DCOS on the Public Agents next
	data["nodeType"] = "slave_public"
	for _, a := range c.definition.PublicAgentIDs {
		err := c.initializeAgentNode(a, NodeType.PublicAgent)
		if err != nil {
			c.definition.Common.State = ClusterState.Created
			return fmt.Errorf("failed to initialize public agent: %s", err.Error())
		}
	}

	return nil
}

func (c *Cluster) initializeAgentNode(ID string, nodeType NodeType.Enum) error {
	svc := c.getService()

	vm, err := svc.GetVM(ID)
	if err != nil {
		c.definition.Common.State = ClusterState.Created
		return fmt.Errorf("failed to get information about VM identified by '%s': %s", ID, err.Error())
	}
	typeStr := "slave"
	if nodeType == NodeType.PublicAgent {
		typeStr = typeStr + "_public"
	}
	_, err = c.executeScript(vm, "dcos_initialize_cluster_node.sh", map[string]interface{}{
		"bootstrapIP":   c.definition.BootstrapIP,
		"bootstrapPort": "80",
		"nodeType":      typeStr,
	})
	if err != nil {
		c.definition.Common.State = ClusterState.Created
		return fmt.Errorf("failed to initialize agent '%s': %s", vm.Name, err.Error())
	}
	return nil
}

//DeleteNode deletes an Agent node
func (c *Cluster) DeleteNode(ID string) error {
	return fmt.Errorf("DeleteNode not yet implemented")
}

//ListMasters lists the master nodes in the cluster
func (c *Cluster) ListMasters() (*[]clusterapi.Node, error) {
	return nil, fmt.Errorf("ListMasters not yet implemented")
}

//ListNodes lists the nodes in the cluster
func (c *Cluster) ListNodes() (*[]clusterapi.Node, error) {
	return nil, fmt.Errorf("ListNodes not yet implemented")
}

//GetNode returns a node based on its ID
func (*Cluster) GetNode(ID string) (*clusterapi.Node, error) {
	return nil, fmt.Errorf("ListNodes not yet implemented")
}

//SaveDefinition writes cluster definition in Object Storage
func (c *Cluster) SaveDefinition() error {
	var buffer bytes.Buffer
	enc := gob.NewEncoder(&buffer)
	err := enc.Encode(c.definition)
	if err != nil {
		return err
	}
	return c.getService().PutObject(clusterapi.DeployContainerName, providerapi.Object{
		Name:    clusterapi.ClusterContainerNamePrefix + c.definition.Common.Name,
		Content: bytes.NewReader(buffer.Bytes()),
	})
}

//ReadDefinition reads definition of cluster named 'name' in Object Storage
// Returns (true, nil) if found and loaded, (false, nil) if not found, and (false, error) in case of error
func (c *Cluster) ReadDefinition() (bool, error) {
	svc := c.getService()

	path := clusterapi.ClusterContainerNamePrefix + c.definition.Common.Name
	list, err := svc.ListObjects(clusterapi.DeployContainerName, providerapi.ObjectFilter{
		Path: path,
	})
	if err != nil {
		return false, err
	}
	found := false
	for _, i := range list {
		if i == path {
			found = true
			break
		}
	}
	if found {
		o, err := svc.GetObject(clusterapi.DeployContainerName, clusterapi.ClusterContainerNamePrefix+c.definition.Common.Name, nil)
		if err != nil {
			return false, err
		}
		var buffer bytes.Buffer
		buffer.ReadFrom(o.Content)
		enc := gob.NewDecoder(&buffer)
		var d ClusterDefinition
		err = enc.Decode(&d)
		if err != nil {
			return false, err
		}
		c.definition = d
		return true, nil
	}
	return false, nil
}

//RemoveDefinition removes definition of cluster from Object Storage
func (c *Cluster) RemoveDefinition() error {
	if len(c.definition.MasterIDs) > 0 ||
		len(c.definition.PublicAgentIDs) > 0 ||
		len(c.definition.PrivateAgentIDs) > 0 ||
		c.definition.NetworkID != "" {
		return fmt.Errorf("can't remove a definition of a cluster with infrastructure still existing")
	}

	svc := c.getService()

	path := clusterapi.ClusterContainerNamePrefix + c.definition.Common.Name
	list, err := svc.ListObjects(clusterapi.DeployContainerName, providerapi.ObjectFilter{
		Path: path,
	})
	if err != nil {
		return err
	}
	found := false
	for _, i := range list {
		if i == path {
			found = true
			break
		}
	}
	if found {
		err := c.getService().DeleteObject(clusterapi.DeployContainerName, clusterapi.ClusterContainerNamePrefix+c.definition.Common.Name)
		if err != nil {
			return fmt.Errorf("failed to remove cluster definition in Object Storage: %s", err.Error())
		}
		c.definition.Common.State = ClusterState.Removed
	}
	return nil
}
