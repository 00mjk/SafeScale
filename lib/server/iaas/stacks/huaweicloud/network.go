/*
 * Copyright 2018-2020, CS Systemes d'Information, http://csgroup.eu
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

package huaweicloud

import (
	"fmt"
	"net"
	"strings"

	"github.com/CS-SI/SafeScale/lib/server/utils"
	"github.com/CS-SI/SafeScale/lib/utils/debug"

	"github.com/pengux/check"
	log "github.com/sirupsen/logrus"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/layer3/routers"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/networks"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/ports"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/subnets"
	"github.com/gophercloud/gophercloud/pagination"

	"github.com/CS-SI/SafeScale/lib/server/iaas/abstract"
	"github.com/CS-SI/SafeScale/lib/server/iaas/abstract/enums/ipversion"
	"github.com/CS-SI/SafeScale/lib/server/iaas/abstract/userdata"
	"github.com/CS-SI/SafeScale/lib/server/iaas/stacks/openstack"
	"github.com/CS-SI/SafeScale/lib/utils/fail"
	"github.com/CS-SI/SafeScale/lib/utils/retry"
	"github.com/CS-SI/SafeScale/lib/utils/retry/enums/verdict"
	"github.com/CS-SI/SafeScale/lib/utils/temporal"
)

// VPCRequest defines a request to create a VPC
type VPCRequest struct {
	Name string `json:"name"`
	CIDR string `json:"cidr"`
}

// VPC contains information about a VPC
type VPC struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	CIDR    string `json:"cidr,omitempty"`
	Status  string `json:"status,omitempty"`
	Network *networks.Network
	Router  *routers.Router
}

type vpcCommonResult struct {
	gophercloud.Result
}

// Extract is a function that accepts a result and extracts a Network/VPC from FlexibleEngine response.
func (r vpcCommonResult) Extract() (*VPC, fail.Error) {
	var s struct {
		VPC *VPC `json:"vpc"`
	}
	err := r.ExtractInto(&s)
	return s.VPC, err
}

type vpcCreateResult struct {
	vpcCommonResult
}
type vpcGetResult struct {
	vpcCommonResult
}

// CreateVPC creates a network, which is managed by VPC in FlexibleEngine
func (s *Stack) CreateVPC(req VPCRequest) (*VPC, fail.Error) {
	// Only one VPC allowed by client instance
	if s.vpc != nil {
		return nil, fail.Errorf(
			fmt.Sprintf(
				"failed to create VPC '%s', a VPC with this name already exists", req.Name,
			), nil,
		)
	}

	b, err := gophercloud.BuildRequestBody(req, "vpc")
	if err != nil {
		return nil, fail.Errorf(
			fmt.Sprintf(
				"failed to create VPC '%s': %s", req.Name, openstack.ProviderErrorToString(err),
			), err,
		)
	}

	resp := vpcCreateResult{}
	url := s.Stack.NetworkClient.Endpoint + "v1/" + s.authOpts.ProjectID + "/vpcs"
	opts := gophercloud.RequestOpts{
		JSONBody:     b,
		JSONResponse: &resp.Body,
		OkCodes:      []int{200, 201},
	}
	_, err = s.Stack.Driver.Request("POST", url, &opts)
	if err != nil {
		return nil, fail.Errorf(
			fmt.Sprintf(
				"failed to send a POST request to provider '%s': %s", req.Name, openstack.ProviderErrorToString(err),
			), err,
		)
	}
	vpc, err := resp.Extract()
	if err != nil {
		return nil, fail.Errorf(
			fmt.Sprintf(
				"failed to create VPC '%s': %s", req.Name, openstack.ProviderErrorToString(err),
			), err,
		)
	}

	// Searching for the OpenStack Router corresponding to the VPC (router.id == vpc.id)
	router, err := routers.Get(s.Stack.NetworkClient, vpc.ID).Extract()
	if err != nil {
		nerr := s.DeleteVPC(vpc.ID)
		if nerr != nil {
			log.Warnf("Error deleting VPC: %v", nerr)
		}
		return nil, fail.Errorf(
			fmt.Sprintf(
				"failed to create VPC '%s': %s", req.Name, openstack.ProviderErrorToString(err),
			), err,
		)
	}
	vpc.Router = router

	// Searching for the Network binded to the VPC
	network, err := s.findVPCBindedNetwork(vpc.Name)
	if err != nil {
		return nil, fail.Errorf(
			fmt.Sprintf(
				"failed to create VPC '%s': %s", req.Name, openstack.ProviderErrorToString(err),
			), err,
		)
	}
	vpc.Network = network

	return vpc, nil
}

func (s *Stack) findVPCBindedNetwork(vpcName string) (*networks.Network, fail.Error) {
	var router *openstack.Router
	found := false
	routerList, err := s.Stack.ListRouters()
	if err != nil {
		return nil, fail.Errorf(fmt.Sprintf("failed to list routers: %s", openstack.ProviderErrorToString(err)), err)
	}
	for _, r := range routerList {
		if r.Name == vpcName {
			found = true
			router = &r
			break
		}
	}
	if !found || router == nil {
		return nil, fail.Errorf(fmt.Sprintf("failed to find router associated to VPC '%s'", vpcName), nil)
	}

	network, err := networks.Get(s.Stack.NetworkClient, router.NetworkID).Extract()
	if err != nil {
		return nil, fail.Errorf(
			fmt.Sprintf(
				"failed to find binded network of VPC '%s': %s", vpcName, openstack.ProviderErrorToString(err),
			), err,
		)
	}
	return network, nil
}

// GetVPC returns the information about a VPC identified by 'id'
func (s *Stack) GetVPC(id string) (*VPC, fail.Error) {
	r := vpcGetResult{}
	url := s.Stack.NetworkClient.Endpoint + "v1/" + s.authOpts.ProjectID + "/vpcs/" + id
	opts := gophercloud.RequestOpts{
		JSONResponse: &r.Body,
		OkCodes:      []int{200, 201},
	}
	_, err := s.Stack.Driver.Request("GET", url, &opts)
	r.Err = err
	vpc, err := r.Extract()
	if err != nil {
		return nil, fail.Errorf(
			fmt.Sprintf("error getting Network %s: %s", id, openstack.ProviderErrorToString(err)), err,
		)
	}
	return vpc, nil
}

// ListVPCs lists all the VPC created
func (s *Stack) ListVPCs() ([]VPC, fail.Error) {
	var vpcList []VPC
	return vpcList, fail.NotImplementedError("huaweicloud.Stack::ListVPCs() not implemented yet") // FIXME: Technical debt
}

// DeleteVPC deletes a Network (ie a VPC in Huawei Cloud) identified by 'id'
func (s *Stack) DeleteVPC(id string) error {
	return fail.NotImplementedError("huaweicloud.Stack::DeleteVPC() not implemented yet") // FIXME: Technical debt
}

// CreateNetwork creates a network (ie a subnet in the network associated to VPC in FlexibleEngine
func (s *Stack) CreateNetwork(req abstract.NetworkRequest) (network *abstract.Network, xerr fail.Error) {
	tracer := debug.NewTracer(nil, fmt.Sprintf("(%s)", req.Name), true).WithStopwatch().GoingIn()
	defer tracer.OnExitTrace()()

	subnet, err := s.findSubnetByName(req.Name)
	if err != nil {
		if _, ok := err.(fail.ErrNotFound); !ok {
			return nil, err
		}
	}
	if subnet != nil {
		return nil, fail.Errorf(fmt.Sprintf("network '%s' already exists", req.Name), nil)
	}

	if ok, err := validateNetworkName(req); !ok {
		return nil, fail.Errorf(fmt.Sprintf("network name '%s' invalid: %s", req.Name, err), err)
	}

	// Checks if CIDR is valid...
	// _, vpcnetDesc, _ := net.ParseCIDR(s.vpc.CIDR)
	// _, networkDesc, err := net.ParseCIDR(req.CIDR)
	// if err != nil {
	//	return nil, fail.Errorf(fmt.Sprintf("failed to create subnet '%s (%s)': %s", req.Name, req.CIDR, err.Error()), err)
	// }
	// // .. and if CIDR is inside VPC's one
	// if !cidrIntersects(vpcnetDesc, networkDesc) {
	//	return nil, fail.Errorf(fmt.Sprintf("cannot create subnet with CIDR '%s': not inside VPC CIDR '%s'", req.CIDR, s.vpc.CIDR), nil)
	// }
	ok, err := utils.DoCIDRsIntersect(s.vpc.CIDR, req.CIDR)
	if err != nil {
		return nil, fail.Errorf(
			fmt.Sprintf(
				"cannot create subnet with CIDR '%s': not inside VPC CIDR '%s'", req.CIDR, s.vpc.CIDR,
			), nil,
		)
	}
	if !ok {
		return nil, fail.Errorf(
			fmt.Sprintf(
				"cannot create subnet with CIDR '%s': not inside VPC CIDR '%s'", req.CIDR, s.vpc.CIDR,
			), nil,
		)
	}

	// Creates the subnet
	subnet, err = s.createSubnet(req.Name, req.CIDR)
	if err != nil {
		return nil, fail.Errorf(
			fmt.Sprintf(
				"error creating network '%s': %s", req.Name, openstack.ProviderErrorToString(err),
			), err,
		)
	}

	// starting from here delete network
	defer func() {
		if err != nil {
			derr := s.deleteSubnet(subnet.ID)
			if derr != nil {
				log.Errorf("failed to delete subnet '%s': %v", subnet.Name, derr)
				err = fail.AddConsequence(err, derr)
			}
		}
	}()

	network = abstract.NewNetwork()
	network.ID = subnet.ID
	network.Name = subnet.Name
	network.CIDR = subnet.CIDR
	network.IPVersion = fromIntIPVersion(subnet.IPVersion)

	return network, nil
}

// validateNetworkName validates the name of a Network based on known FlexibleEngine requirements
func validateNetworkName(req abstract.NetworkRequest) (bool, fail.Error) {
	s := check.Struct{
		"Name": check.Composite{
			check.NonEmpty{},
			check.Regex{Constraint: `^[a-zA-Z0-9_-]+$`},
			check.MaxChar{Constraint: 64},
		},
	}

	e := s.Validate(req)
	if e.HasErrors() {
		errors, _ := e.GetErrorsByKey("Name")
		var errs []string
		for _, msg := range errors {
			errs = append(errs, msg.Error())
		}
		// FIXME: cause not nil
		return false, fail.Errorf(fmt.Sprintf(strings.Join(errs, "; ")), nil)
	}
	return true, nil
}

// GetNetworkByName ...
func (s *Stack) GetNetworkByName(name string) (*abstract.Network, fail.Error) {
	// Gophercloud doesn't propose the way to get a host by name, but OpenStack knows how to do it...
	r := networks.GetResult{}
	getErr := retry.WhileSuccessfulDelay1Second(
		func() error {
			_, r.Err = s.Stack.NetworkClient.Get(
				s.Stack.NetworkClient.ServiceURL("subnets?name="+name), &r.Body, &gophercloud.RequestOpts{
					OkCodes: []int{200, 203},
				},
			)
			if r.Err != nil {
				return openstack.ReinterpretGophercloudErrorCode(
					r.Err, nil, []int64{408, 429, 500, 503}, []int64{401, 403, 404, 409}, func(ferr error) error {
						return fail.Errorf(fmt.Sprintf("query for network '%s' failed: %v", name, ferr), ferr)
					},
				)
			}
			return nil
		},
		temporal.GetContextTimeout(),
	)

	if getErr != nil {
		return nil, getErr
	}

	subnetworks, found := r.Body.(map[string]interface{})["subnets"].([]interface{})
	if found && len(subnetworks) > 0 {
		var (
			entry map[string]interface{}
			id    string
		)
		for _, s := range subnetworks {
			entry = s.(map[string]interface{})
			id = entry["id"].(string)
		}
		return s.GetNetwork(id)
	}
	return nil, abstract.ResourceNotFoundError("network", name)
}

// GetNetwork returns the network identified by id
func (s *Stack) GetNetwork(id string) (*abstract.Network, fail.Error) {
	subnet, err := s.getSubnet(id)
	if err != nil {
		if !strings.Contains(err.Error(), id) {
			return nil, fail.Errorf(
				fmt.Sprintf(
					"failed getting network id '%s': %s", id, openstack.ProviderErrorToString(err),
				), err,
			)
		}
	}
	if subnet == nil || subnet.ID == "" {
		return nil, abstract.ResourceNotFoundError("subnet", id)
	}

	newNet := abstract.NewNetwork()
	newNet.ID = subnet.ID
	newNet.Name = subnet.Name
	newNet.CIDR = subnet.CIDR
	newNet.IPVersion = fromIntIPVersion(subnet.IPVersion)
	return newNet, nil
}

// ListNetworks lists networks
func (s *Stack) ListNetworks() ([]*abstract.Network, fail.Error) {
	subnetList, err := s.listSubnets()
	if err != nil {
		return nil, fail.Errorf(
			fmt.Sprintf("failed to get networks list: %s", openstack.ProviderErrorToString(err)), err,
		)
	}
	var networkList []*abstract.Network
	for _, subnet := range *subnetList {
		newNet := abstract.NewNetwork()
		newNet.ID = subnet.ID
		newNet.Name = subnet.Name
		newNet.CIDR = subnet.CIDR
		newNet.IPVersion = fromIntIPVersion(subnet.IPVersion)
		networkList = append(networkList, newNet)
	}
	return networkList, nil
}

// DeleteNetwork consists to delete subnet in FlexibleEngine VPC
func (s *Stack) DeleteNetwork(id string) error {
	return s.deleteSubnet(id)
}

type subnetRequest struct {
	Name             string   `json:"name"`
	CIDR             string   `json:"cidr"`
	GatewayIP        string   `json:"gateway_ip"`
	DHCPEnable       *bool    `json:"dhcp_enable,omitempty"`
	PrimaryDNS       string   `json:"primary_dns,omitempty"`
	SecondaryDNS     string   `json:"secondary_dns,omitempty"`
	DNSList          []string `json:"dnsList,omitempty"`
	AvailabilityZone string   `json:"availability_zone,omitempty"`
	VPCID            string   `json:"vpc_id"`
}

type subnetCommonResult struct {
	gophercloud.Result
}

type subnetEx struct {
	subnets.Subnet
	Status string `json:"status"`
}

// Extract is a function that accepts a result and extracts a Subnet from FlexibleEngine response.
func (r subnetCommonResult) Extract() (*subnetEx, fail.Error) {
	var s struct {
		//		Subnet *subnets.Subnet `json:"subnet"`
		Subnet *subnetEx `json:"subnet"`
	}
	err := r.ExtractInto(&s)
	return s.Subnet, err
}

type subnetCreateResult struct {
	subnetCommonResult
}
type subnetGetResult struct {
	subnetCommonResult
}
type subnetDeleteResult struct {
	gophercloud.ErrResult
}

// convertIPv4ToNumber converts a net.IP to a uint32 representation
func convertIPv4ToNumber(ip net.IP) (uint32, fail.Error) {
	if ip.To4() == nil {
		return 0, fail.Errorf(fmt.Sprintf("not an IPv4"), nil)
	}
	n := uint32(ip[0])*0x1000000 + uint32(ip[1])*0x10000 + uint32(ip[2])*0x100 + uint32(ip[3])
	return n, nil
}

// convertNumberToIPv4 converts a uint32 representation of an IPv4 Address to net.IP
func convertNumberToIPv4(n uint32) net.IP {
	a := byte(n >> 24)
	b := byte((n & 0xff0000) >> 16)
	c := byte((n & 0xff00) >> 8)
	d := byte(n & 0xff)
	IP := net.IPv4(a, b, c, d)
	return IP
}

// VPL: replaced by utils.DoCIDRsIntersect
// // cidrIntersects tells if the 2 CIDR passed as parameter intersect
// func cidrIntersects(n1, n2 *net.IPNet) bool {
//	return n2.Contains(n1.IP) || n1.Contains(n2.IP)
// }

// createSubnet creates a subnet using native FlexibleEngine API
func (s *Stack) createSubnet(name string, cidr string) (*subnets.Subnet, fail.Error) {
	network, _, _ := net.ParseCIDR(cidr)

	// Validates CIDR regarding the existing subnets
	subnetworks, err := s.listSubnets()
	if err != nil {
		return nil, err
	}
	for _, s := range *subnetworks {
		// _, sDesc, _ := net.ParseCIDR(s.CIDR)
		intersects, err := utils.DoCIDRsIntersect(cidr, s.CIDR)
		if err != nil {
			return nil, err
		}
		if intersects {
			return nil, fail.Errorf(
				fmt.Sprintf(
					"cannot create subnet '%s (%s)', would intersect with '%s (%s)'", name, cidr, s.Name, s.CIDR,
				), nil,
			)
		}
	}

	// Calculate IP address for gateway
	n, err := convertIPv4ToNumber(network.To4())
	if err != nil {
		return nil, fail.Errorf(
			fmt.Sprintf(
				"failed to choose gateway IP address for the subnet: %s", openstack.ProviderErrorToString(err),
			), err,
		)
	}
	gw := convertNumberToIPv4(n + 1)

	dnsList := s.cfgOpts.DNSList
	if len(dnsList) == 0 {
		dnsList = []string{"1.1.1.1"}
	}
	var (
		primaryDNS   string
		secondaryDNS string
	)
	if len(dnsList) >= 1 {
		primaryDNS = dnsList[0]
	}
	if len(dnsList) >= 2 {
		secondaryDNS = dnsList[1]
	}
	bYes := true
	req := subnetRequest{
		Name:         name,
		CIDR:         cidr,
		VPCID:        s.vpc.ID,
		DHCPEnable:   &bYes,
		GatewayIP:    gw.String(),
		PrimaryDNS:   primaryDNS,
		SecondaryDNS: secondaryDNS,
		DNSList:      dnsList,
	}
	b, err := gophercloud.BuildRequestBody(req, "subnet")
	if err != nil {
		return nil, fail.Errorf(
			fmt.Sprintf(
				"error preparing Subnet %s creation: %s", req.Name, openstack.ProviderErrorToString(err),
			), err,
		)
	}

	respCreate := subnetCreateResult{}
	url := fmt.Sprintf("%sv1/%s/subnets", s.Stack.NetworkClient.Endpoint, s.authOpts.ProjectID)
	opts := gophercloud.RequestOpts{
		JSONBody:     b,
		JSONResponse: &respCreate.Body,
		OkCodes:      []int{200, 201},
	}
	_, err = s.Stack.Driver.Request("POST", url, &opts)
	if err != nil {
		return nil, fail.Errorf(
			fmt.Sprintf(
				"error requesting subnet %s creation: %s", req.Name, openstack.ProviderErrorToString(err),
			), err,
		)
	}
	subnet, err := respCreate.Extract()
	if err != nil {
		return nil, fail.Errorf(
			fmt.Sprintf(
				"error creating Subnet %s: %s", req.Name, openstack.ProviderErrorToString(err),
			), err,
		)
	}

	// Subnet creation started, need to wait the subnet to reach the status ACTIVE
	respGet := subnetGetResult{}
	opts.JSONResponse = &respGet.Body
	opts.JSONBody = nil

	retryErr := retry.WhileUnsuccessfulDelay1SecondWithNotify(
		func() error {
			_, err = s.Stack.Driver.Request("GET", fmt.Sprintf("%s/%s", url, subnet.ID), &opts)
			if err == nil {
				subnet, err = respGet.Extract()
				if err == nil && subnet.Status == "ACTIVE" {
					return nil
				}
			}
			return err
		},
		temporal.GetContextTimeout(),
		func(try retry.Try, v verdict.Enum) {
			if v != verdict.Done {
				log.Debugf("Network '%s' is not in 'ACTIVE' state, retrying...", name)
			}
		},
	)
	return &subnet.Subnet, retryErr
}

// ListSubnets lists available subnet in VPC
func (s *Stack) listSubnets() (*[]subnets.Subnet, fail.Error) {
	url := s.Stack.NetworkClient.Endpoint + "v1/" + s.authOpts.ProjectID + "/subnets?vpc_id=" + s.vpc.ID
	pager := pagination.NewPager(
		s.Stack.NetworkClient, url, func(r pagination.PageResult) pagination.Page {
			return subnets.SubnetPage{LinkedPageBase: pagination.LinkedPageBase{PageResult: r}}
		},
	)
	var subnetList []subnets.Subnet
	paginationErr := pager.EachPage(
		func(page pagination.Page) (bool, fail.Error) {
			list, err := subnets.ExtractSubnets(page)
			if err != nil {
				return false, fail.Errorf(
					fmt.Sprintf(
						"error listing subnets: %s", openstack.ProviderErrorToString(err),
					), err,
				)
			}

			subnetList = append(subnetList, list...)

			return true, nil
		},
	)

	// TODO: previously we ignored the error here, consider returning nil, paginationErr
	if paginationErr != nil {
		log.Warnf("We have a pagination error: %v", paginationErr)
	}

	return &subnetList, nil
}

// getSubnet lists available subnet in VPC
func (s *Stack) getSubnet(id string) (*subnets.Subnet, fail.Error) {
	r := subnetGetResult{}
	url := s.Stack.NetworkClient.Endpoint + "v1/" + s.authOpts.ProjectID + "/subnets/" + id
	opts := gophercloud.RequestOpts{
		JSONResponse: &r.Body,
		OkCodes:      []int{200, 201},
	}
	_, err := s.Stack.Driver.Request("GET", url, &opts)
	r.Err = err
	subnet, err := r.Extract()
	if err != nil {
		return nil, fail.Errorf(
			fmt.Sprintf(
				"failed to get information for subnet id '%s': %s", id, openstack.ProviderErrorToString(err),
			), err,
		)
	}
	return &subnet.Subnet, nil
}

// deleteSubnet deletes a subnet
func (s *Stack) deleteSubnet(id string) error {
	resp := subnetDeleteResult{}
	url := s.Stack.NetworkClient.Endpoint + "v1/" + s.authOpts.ProjectID + "/vpcs/" + s.vpc.ID + "/subnets/" + id
	opts := gophercloud.RequestOpts{
		OkCodes: []int{204},
	}

	// FlexibleEngine has the curious behavior to be able to tell us all Hosts are deleted, but
	// cannot delete the subnet because there is still at least one host...
	// So we retry subnet deletion until all hosts are really deleted and subnet can be deleted
	err := retry.Action(
		func() error {
			r, _ := s.Stack.Driver.Request("DELETE", url, &opts)
			if r == nil {
				return fail.Errorf(fmt.Sprintf("failed to acknowledge DELETE command submission"), nil)
			}
			if r.StatusCode == 204 || r.StatusCode == 404 {
				return nil
			}
			return fail.Errorf(fmt.Sprintf("DELETE command failed with status %d and body %s", r.StatusCode, r.Body), nil)
		},
		retry.PrevailDone(retry.Unsuccessful(), retry.Timeout(temporal.GetHostTimeout())),
		retry.BackoffSelector()(temporal.GetDefaultDelay()),
		nil, nil,
		func(t retry.Try, v verdict.Enum) {
			if t.Err != nil {
				switch t.Err.Error() {
				case "409":
					log.Debugf(
						"network still owns hosts and/or IP addresses, retrying in %s...", temporal.GetDefaultDelay(),
					)
				default:
					log.Debugf(
						"error submitting network deletion (status=%s), retrying in %s...", t.Err.Error(),
						temporal.GetDefaultDelay(),
					)
				}
			}
		},
	)
	if err != nil {
		return fail.Errorf(fmt.Sprintf("failed to submit deletion of subnet id '%s': '%s", id, err.Error()), err)
	}
	// Deletion submit has been executed, checking returned error code
	err = resp.ExtractErr()
	if err != nil {
		return fail.Errorf(
			fmt.Sprintf("error deleting subnet id '%s': %s", id, openstack.ProviderErrorToString(err)), nil,
		)
	}
	return nil
}

// findSubnetByName returns a subnets.Subnet if subnet named as 'name' exists
func (s *Stack) findSubnetByName(name string) (*subnets.Subnet, fail.Error) {
	subnetList, err := s.listSubnets()
	if err != nil {
		return nil, fail.Errorf(
			fmt.Sprintf("failed to find in Subnets: %s", openstack.ProviderErrorToString(err)), err,
		)
	}
	found := false
	var subnet subnets.Subnet
	for _, s := range *subnetList {
		if s.Name == name {
			found = true
			subnet = s
			break
		}
	}
	if !found {
		return nil, abstract.ResourceNotFoundError("subnet", name)
	}
	return &subnet, nil
}

func fromIntIPVersion(v int) ipversion.Enum {
	if v == 4 {
		return ipversion.IPv4
	}
	if v == 6 {
		return ipversion.IPv6
	}
	return -1
}

// CreateGateway creates a gateway for a network.
// By current implementation, only one gateway can exist by Network because the object is intended
// to contain only one hostID
func (s *Stack) CreateGateway(req abstract.GatewayRequest, sizing *abstract.SizingRequirements) (*abstract.Host, *userdata.Content, fail.Error) {
	gwname := strings.Split(req.Name, ".")[0] // req.Name may contain a FQDN...
	if gwname == "" {
		gwname = "gw-" + req.Network.Name
	}

	tracer := debug.NewTracer(nil, fmt.Sprintf("(%s)", gwname), true).WithStopwatch().GoingIn()
	defer tracer.OnExitTrace()()

	hostReq := abstract.HostRequest{
		ImageID:      req.ImageID,
		KeyPair:      req.KeyPair,
		HostName:     req.Name,
		ResourceName: gwname,
		TemplateID:   req.TemplateID,
		Networks:     []*abstract.Network{req.Network},
		PublicIP:     true,
	}
	if sizing != nil && sizing.MinDiskSize > 0 {
		hostReq.DiskSize = sizing.MinDiskSize
	}
	host, userData, err := s.CreateHost(hostReq)
	if err != nil {
		switch err.(type) {
		case fail.ErrInvalidRequest:
			return nil, userData, err
		default:
			return nil, userData, fail.Errorf(
				fmt.Sprintf(
					"error creating gateway : %s", openstack.ProviderErrorToString(err),
				), err,
			)
		}
	}
	return host, userData, err
}

// DeleteGateway deletes the gateway associated with network identified by ID
func (s *Stack) DeleteGateway(id string) error {
	return s.DeleteHost(id)
}

// CreateVIP creates a private virtual IP
// If public is set to true,
func (s *Stack) CreateVIP(networkID string, name string) (*abstract.VirtualIP, fail.Error) {
	asu := true
	sg := []string{s.SecurityGroup.ID}
	options := ports.CreateOpts{
		NetworkID:      networkID,
		AdminStateUp:   &asu,
		Name:           name,
		SecurityGroups: &sg,
	}
	port, err := ports.Create(s.NetworkClient, options).Extract()
	if err != nil {
		return nil, err
	}
	vip := abstract.VirtualIP{
		ID:        port.ID,
		Name:      name,
		NetworkID: networkID,
		PrivateIP: port.FixedIPs[0].IPAddress,
	}
	return &vip, nil
}
