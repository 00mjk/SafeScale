/*
 * Copyright 2018-2020, CS Systemes d'Information, http://www.c-s.fr
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

package openstack

import (
	"fmt"
	"strings"
	"time"

	"github.com/CS-SI/SafeScale/lib/server/resources/operations/converters"

	"github.com/davecgh/go-spew/spew"
	uuid "github.com/satori/go.uuid"
	"github.com/sirupsen/logrus"

	"github.com/gophercloud/gophercloud"
	az "github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/availabilityzones"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/floatingips"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/keypairs"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/startstop"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/regions"
	"github.com/gophercloud/gophercloud/openstack/imageservice/v2/images"
	"github.com/gophercloud/gophercloud/pagination"

	"github.com/CS-SI/SafeScale/lib/server/iaas/stacks"
	"github.com/CS-SI/SafeScale/lib/server/iaas/userdata"
	"github.com/CS-SI/SafeScale/lib/server/resources/abstract"
	"github.com/CS-SI/SafeScale/lib/server/resources/enums/hoststate"
	"github.com/CS-SI/SafeScale/lib/server/resources/enums/ipversion"
	"github.com/CS-SI/SafeScale/lib/utils"
	"github.com/CS-SI/SafeScale/lib/utils/concurrency"
	"github.com/CS-SI/SafeScale/lib/utils/debug"
	"github.com/CS-SI/SafeScale/lib/utils/fail"
	"github.com/CS-SI/SafeScale/lib/utils/retry"
	"github.com/CS-SI/SafeScale/lib/utils/strprocess"
	"github.com/CS-SI/SafeScale/lib/utils/temporal"
)

// ListRegions ...
func (s *Stack) ListRegions() ([]string, fail.Error) {
	if s == nil {
		return nil, fail.InvalidInstanceError()
	}

	defer concurrency.NewTracer(nil, true, "").WithStopwatch().Entering().OnExitTrace()

	listOpts := regions.ListOpts{
		ParentRegionID: "RegionOne",
	}

	var results []string
	allPages, err := regions.List(s.ComputeClient, listOpts).AllPages()
	if err != nil {
		return results, fail.ToError(err)
	}

	allRegions, err := regions.ExtractRegions(allPages)
	if err != nil {
		return results, fail.ToError(err)
	}

	for _, reg := range allRegions {
		results = append(results, reg.ID)
	}

	return results, nil
}

// ListAvailabilityZones lists the usable AvailabilityZones
func (s *Stack) ListAvailabilityZones() (list map[string]bool, xerr fail.Error) {
	if s == nil {
		return nil, fail.InvalidInstanceError()
	}

	tracer := concurrency.NewTracer(nil, true, "").Entering()
	defer tracer.OnExitTrace()
	defer fail.OnExitLogError(tracer.TraceMessage(""), &xerr)

	allPages, err := az.List(s.ComputeClient).AllPages()
	if err != nil {
		return nil, fail.ToError(err)
	}

	content, err := az.ExtractAvailabilityZones(allPages)
	if err != nil {
		return nil, fail.ToError(err)
	}

	azList := map[string]bool{}
	for _, zone := range content {
		if zone.ZoneState.Available {
			azList[zone.ZoneName] = zone.ZoneState.Available
		}
	}

	// VPL: what's the point if there ios
	if len(azList) == 0 {
		logrus.Warnf("no Availability Zones detected !")
	}

	return azList, nil
}

// ListImages lists available OS images
func (s *Stack) ListImages() (imgList []abstract.Image, xerr fail.Error) {
	if s == nil {
		return nil, fail.InvalidInstanceError()
	}

	tracer := concurrency.NewTracer(nil, true, "").WithStopwatch().Entering()
	defer tracer.OnExitTrace()
	defer fail.OnExitLogError(tracer.TraceMessage(""), &xerr)

	opts := images.ListOpts{}

	// Retrieve a pager (i.e. a paginated collection)
	pager := images.List(s.ComputeClient, opts)

	// Define an anonymous function to be executed on each page's iteration
	err := pager.EachPage(func(page pagination.Page) (bool, error) {
		imageList, err := images.ExtractImages(page)
		if err != nil {
			return false, err
		}

		for _, img := range imageList {
			imgList = append(imgList, abstract.Image{ID: img.ID, Name: img.Name})
		}
		return true, nil
	})
	if (len(imgList) == 0) || (err != nil) {
		if err != nil {
			return nil, fail.Wrap(err, fmt.Sprintf("error listing images: %s", ProviderErrorToString(err)))
		}
		logrus.Debugf("Image list empty !")
	}
	return imgList, nil
}

// GetImage returns the Image referenced by id
func (s *Stack) GetImage(id string) (image *abstract.Image, xerr fail.Error) {
	if s == nil {
		return nil, fail.InvalidInstanceError()
	}
	if id == "" {
		return nil, fail.InvalidParameterError("id", "cannot be empty string")
	}

	tracer := concurrency.NewTracer(nil, debug.ShouldTrace("stack.compute"), "(%s)", id).WithStopwatch().Entering()
	defer tracer.OnExitTrace()
	defer fail.OnExitLogError(tracer.TraceMessage(""), &xerr)

	img, err := images.Get(s.ComputeClient, id).Extract()
	if err != nil {
		return nil, fail.Wrap(err, fmt.Sprintf("error getting image: %s", ProviderErrorToString(err)))
	}
	return &abstract.Image{ID: img.ID, Name: img.Name}, nil
}

// GetTemplate returns the Template referenced by id
func (s *Stack) GetTemplate(id string) (template *abstract.HostTemplate, xerr fail.Error) {
	if s == nil {
		return nil, fail.InvalidInstanceError()
	}
	if id == "" {
		return nil, fail.InvalidParameterError("id", "cannot be empty string")
	}

	tracer := concurrency.NewTracer(nil, debug.ShouldTrace("stack.compute"), "(%s)", id).WithStopwatch().Entering()
	defer tracer.OnExitTrace()
	defer fail.OnExitLogError(tracer.TraceMessage(""), &xerr)

	// Try 10 seconds to get template
	var flv *flavors.Flavor
	retryErr := retry.WhileUnsuccessfulDelay1Second(
		func() error {
			var err error
			flv, err = flavors.Get(s.ComputeClient, id).Extract()
			return err
		},
		2*temporal.GetDefaultDelay(),
	)
	if retryErr != nil {
		return nil, fail.Wrap(retryErr, "error getting template: %s", ProviderErrorToString(retryErr))
	}
	return &abstract.HostTemplate{
		Cores:    flv.VCPUs,
		RAMSize:  float32(flv.RAM) / 1000.0,
		DiskSize: flv.Disk,
		ID:       flv.ID,
		Name:     flv.Name,
	}, nil
}

// ListTemplates lists available Host templates
// Host templates are sorted using Dominant Resource Fairness Algorithm
func (s *Stack) ListTemplates() ([]abstract.HostTemplate, fail.Error) {
	if s == nil {
		return nil, fail.InvalidInstanceError()
	}

	tracer := concurrency.NewTracer(nil, debug.ShouldTrace("stack.compute"), "").WithStopwatch().Entering()
	defer tracer.OnExitTrace()

	opts := flavors.ListOpts{}

	// Retrieve a pager (i.e. a paginated collection)
	var (
		flvList []abstract.HostTemplate
		pager   pagination.Pager
	)

	// Define an anonymous function to be executed on each page's iteration
	retryErr := retry.WhileUnsuccessfulDelay1Second(
		func() error {
			pager = flavors.ListDetail(s.ComputeClient, opts)
			return pager.EachPage(func(page pagination.Page) (bool, error) {
				flavorList, err := flavors.ExtractFlavors(page)
				if err != nil {
					return false, err
				}
				for _, flv := range flavorList {
					flvList = append(flvList, abstract.HostTemplate{
						Cores:    flv.VCPUs,
						RAMSize:  float32(flv.RAM) / 1000.0,
						DiskSize: flv.Disk,
						ID:       flv.ID,
						Name:     flv.Name,
					})
				}
				return true, nil
			})
		},
		time.Minute*2,
	)
	if retryErr != nil {
		switch retryErr.(type) {
		case *fail.ErrTimeout:
			return nil, retryErr
		default:
			return nil, fail.Wrap(retryErr, "error listing templates")
		}
	}
	if len(flvList) == 0 {
		logrus.Debugf("Template list empty")
	}
	return flvList, nil
}

// TODO: replace with code to create KeyPair on provider side if it exists
// CreateKeyPair creates and import a key pair
func (s *Stack) CreateKeyPair(name string) (*abstract.KeyPair, fail.Error) {
	if s == nil {
		return nil, fail.InvalidInstanceError()
	}
	if name == "" {
		return nil, fail.InvalidParameterError("name", "cannot be empty string")
	}

	tracer := concurrency.NewTracer(nil, debug.ShouldTrace("stack.compute"), "(%s)", name).WithStopwatch().Entering()
	defer tracer.OnExitTrace()

	return abstract.NewKeyPair(name)
}

// TODO: replace with openstack code to get keypair (if it exits)
// GetKeyPair returns the key pair identified by id
func (s *Stack) GetKeyPair(id string) (*abstract.KeyPair, fail.Error) {
	if s == nil {
		return nil, fail.InvalidInstanceError()
	}
	if id == "" {
		return nil, fail.InvalidParameterError("id", "cannot be nil")
	}

	tracer := concurrency.NewTracer(nil, debug.ShouldTrace("stack.compute"), "(%s)", id).WithStopwatch().Entering()
	defer tracer.OnExitTrace()

	kp, err := keypairs.Get(s.ComputeClient, id).Extract()
	if err != nil {
		return nil, fail.Wrap(err, "error getting keypair")
	}
	return &abstract.KeyPair{
		ID:         kp.Name,
		Name:       kp.Name,
		PrivateKey: kp.PrivateKey,
		PublicKey:  kp.PublicKey,
	}, nil
}

// ListKeyPairs lists available key pairs
// Returned list can be empty
func (s *Stack) ListKeyPairs() ([]abstract.KeyPair, fail.Error) {
	if s == nil {
		return nil, fail.InvalidInstanceError()
	}

	defer concurrency.NewTracer(nil, debug.ShouldTrace("stack.compute"), "").WithStopwatch().Entering().OnExitTrace()

	// Retrieve a pager (i.e. a paginated collection)
	pager := keypairs.List(s.ComputeClient)

	var kpList []abstract.KeyPair

	// Define an anonymous function to be executed on each page's iteration
	err := pager.EachPage(func(page pagination.Page) (bool, error) {
		keyList, err := keypairs.ExtractKeyPairs(page)
		if err != nil {
			return false, err
		}

		for _, kp := range keyList {
			kpList = append(kpList, abstract.KeyPair{
				ID:         kp.Name,
				Name:       kp.Name,
				PublicKey:  kp.PublicKey,
				PrivateKey: kp.PrivateKey,
			})
		}
		return true, nil
	})
	if (len(kpList) == 0) || (err != nil) {
		if err != nil {
			return nil, fail.Wrap(err, "error listing keypairs")
		}
	}
	return kpList, nil
}

// DeleteKeyPair deletes the key pair identified by id
func (s *Stack) DeleteKeyPair(id string) fail.Error {
	if s == nil {
		return fail.InvalidInstanceError()
	}
	if id == "" {
		return fail.InvalidParameterError("id", "cannot be empty string")
	}

	defer concurrency.NewTracer(nil, debug.ShouldTrace("stack.compute"), "(%s)", id).WithStopwatch().Entering().OnExitTrace()

	err := keypairs.Delete(s.ComputeClient, id).ExtractErr()
	if err != nil {
		return fail.Wrap(err, "error deleting key pair: %s", ProviderErrorToString(err))
	}
	return nil
}

// toHostSize converts flavor attributes returned by OpenStack driver into abstract.HostEffectiveSizing
func (s *Stack) toHostSize(flavor map[string]interface{}) (ahes *abstract.HostEffectiveSizing) {
	hostSizing := abstract.NewHostEffectiveSizing()
	if i, ok := flavor["id"]; ok {
		fid, ok := i.(string)
		if !ok {
			return hostSizing
		}
		tpl, xerr := s.GetTemplate(fid)
		if xerr != nil {
			return hostSizing
		}
		hostSizing.Cores = tpl.Cores
		hostSizing.DiskSize = tpl.DiskSize
		hostSizing.RAMSize = tpl.RAMSize
	} else if _, ok := flavor["vcpus"]; ok {
		hostSizing.Cores = flavor["vcpus"].(int)
		hostSizing.DiskSize = flavor["disk"].(int)
		hostSizing.RAMSize = flavor["ram"].(float32) / 1000.0
	}
	return hostSizing
}

// toHostState converts host status returned by OpenStack driver into HostState enum
func toHostState(status string) hoststate.Enum {
	switch strings.ToLower(status) {
	case "build", "building":
		return hoststate.STARTING
	case "active":
		return hoststate.STARTED
	case "rescued":
		return hoststate.STOPPING
	case "stopped", "shutoff":
		return hoststate.STOPPED
	default:
		return hoststate.ERROR
	}
}

// InspectHost gathers host information from provider
func (s *Stack) InspectHost(hostParam stacks.HostParameter) (*abstract.HostFull, fail.Error) {
	nullAhf := abstract.NewHostFull()
	if s == nil {
		return nullAhf, fail.InvalidInstanceError()
	}
	ahf, hostRef, xerr := stacks.ValidateHostParameter(hostParam)
	if xerr != nil {
		return nullAhf, xerr
	}

	defer concurrency.NewTracer(nil, debug.ShouldTrace("stack.compute"), "(%s)", hostRef).WithStopwatch().Entering().OnExitTrace()

	server, xerr := s.WaitHostState(ahf, hoststate.STARTED, 2*temporal.GetBigDelay())
	if xerr != nil {
		return nullAhf, xerr
	}
	if server == nil {
		return nullAhf, abstract.ResourceNotFoundError("host", hostRef)
	}

	ahf.Core.LastState = toHostState(server.Status)

	if !ahf.OK() {
		logrus.Warnf("[TRACE] Unexpected host status: %s", spew.Sdump(ahf))
	}

	return ahf, nil
}

func (s *Stack) queryServer(id string) (*servers.Server, fail.Error) {
	return s.WaitHostState(id, hoststate.STARTED, 2*temporal.GetBigDelay())
}

// interpretAddresses converts adresses returned by the OpenStack driver
// Returns string slice containing the name of the networks, string map of IP addresses
// (indexed on network name), public ipv4 and ipv6 (if they exists)
func (s *Stack) interpretAddresses(
	addresses map[string]interface{},
) ([]string, map[ipversion.Enum]map[string]string, string, string, fail.Error) {
	var (
		networks    []string
		addrs       = map[ipversion.Enum]map[string]string{}
		AcccessIPv4 string
		AcccessIPv6 string
	)

	addrs[ipversion.IPv4] = map[string]string{}
	addrs[ipversion.IPv6] = map[string]string{}

	for n, obj := range addresses {
		networks = append(networks, n)
		for _, networkAddresses := range obj.([]interface{}) {
			address, ok := networkAddresses.(map[string]interface{})
			if !ok {
				return networks, addrs, AcccessIPv4, AcccessIPv6, fail.InconsistentError("invalid network address")
			}
			version, ok := address["version"].(float64)
			if !ok {
				return networks, addrs, AcccessIPv4, AcccessIPv6, fail.InconsistentError("invalid version")
			}
			fixedIP, ok := address["addr"].(string)
			if !ok {
				return networks, addrs, AcccessIPv4, AcccessIPv6, fail.InconsistentError("invalid addr")
			}
			if n == s.cfgOpts.ProviderNetwork {
				switch version {
				case 4:
					AcccessIPv4 = fixedIP
				case 6:
					AcccessIPv6 = fixedIP
				}
			} else {
				switch version {
				case 4:
					addrs[ipversion.IPv4][n] = fixedIP
				case 6:
					addrs[ipversion.IPv6][n] = fixedIP
				}
			}

		}
	}
	return networks, addrs, AcccessIPv4, AcccessIPv6, nil
}

// complementHost complements Host data with content of server parameter
func (s *Stack) complementHost(hostCore *abstract.HostCore, server servers.Server) (host *abstract.HostFull, xerr fail.Error) {
	defer fail.OnPanic(&xerr)

	networks, addresses, ipv4, ipv6, xerr := s.interpretAddresses(server.Addresses)
	if xerr != nil {
		return nil, xerr
	}

	// Updates intrinsic data of host if needed
	if hostCore.ID == "" {
		hostCore.ID = server.ID
	}
	if hostCore.Name == "" {
		hostCore.Name = server.Name
	}

	hostCore.LastState = toHostState(server.Status)
	if hostCore.LastState == hoststate.ERROR || hostCore.LastState == hoststate.STARTING {
		logrus.Warnf("[TRACE] Unexpected host's last state: %v", hostCore.LastState)
	}

	host = abstract.NewHostFull()
	host.Core = hostCore
	host.Description = &abstract.HostDescription{
		Created: server.Created,
		Updated: server.Updated,
	}

	host.Sizing = s.toHostSize(server.Flavor)

	var errors []error
	networksByID := map[string]string{}
	ipv4Addresses := map[string]string{}
	ipv6Addresses := map[string]string{}

	// Parse networks and fill fields
	for _, netname := range networks {
		// Ignore ProviderNetwork
		if s.cfgOpts.ProviderNetwork == netname {
			continue
		}

		net, xerr := s.GetNetworkByName(netname)
		if xerr != nil {
			logrus.Debugf("failed to get data for network '%s'", netname)
			errors = append(errors, xerr)
			continue
		}
		networksByID[net.ID] = ""

		if ip, ok := addresses[ipversion.IPv4][netname]; ok {
			ipv4Addresses[net.ID] = ip
		} else {
			ipv4Addresses[net.ID] = ""
		}

		if ip, ok := addresses[ipversion.IPv6][netname]; ok {
			ipv6Addresses[net.ID] = ip
		} else {
			ipv6Addresses[net.ID] = ""
		}
	}

	// Updates network name and relationships if needed
	config := s.GetConfigurationOptions()
	networksByName := map[string]string{}
	for netid, netname := range networksByID {
		if netname == "" {
			net, xerr := s.GetNetwork(netid)
			if xerr != nil {
				switch xerr.(type) {
				case *fail.ErrNotFound:
					logrus.Errorf(xerr.Error())
					errors = append(errors, xerr)
				default:
					logrus.Errorf("failed to get network '%s': %v", netid, xerr)
					errors = append(errors, xerr)
				}
				continue
			}
			if net.Name == config.ProviderNetwork {
				continue
			}
			networksByID[netid] = net.Name
			networksByName[net.Name] = netid
		}
	}
	if len(errors) > 0 {
		return nil, fail.NewErrorList(errors)
	}
	host.Network = &abstract.HostNetwork{
		PublicIPv4:     ipv4,
		PublicIPv6:     ipv6,
		NetworksByID:   networksByID,
		NetworksByName: networksByName,
		IPv4Addresses:  ipv4Addresses,
		IPv6Addresses:  ipv6Addresses,
	}
	return host, nil
}

// GetHostByName returns the host using the name passed as parameter
// returns id of the host if found
// returns abstract.ErrResourceNotFound if not found
// returns abstract.ErrResourceNotAvailable if provider doesn't provide the id of the host in its response
func (s *Stack) GetHostByName(name string) (*abstract.HostCore, fail.Error) {
	if s == nil {
		return nil, fail.InvalidInstanceError()
	}
	if name == "" {
		return nil, fail.InvalidParameterError("name", "cannot be empty string")
	}

	defer concurrency.NewTracer(nil, debug.ShouldTrace("stack.compute"), "('%s')", name).WithStopwatch().Entering().OnExitTrace()

	// Gophercloud doesn't propose the way to get a host by name, but OpenStack knows how to do it...
	r := servers.GetResult{}
	_, r.Err = s.ComputeClient.Get(s.ComputeClient.ServiceURL("servers?name="+name), &r.Body, &gophercloud.RequestOpts{
		OkCodes: []int{200, 203},
	})
	if r.Err != nil {
		return nil, fail.NewError("failed to get data of host '%s': %v", name, r.Err)
	}
	serverList, found := r.Body.(map[string]interface{})["servers"].([]interface{})
	if found && len(serverList) > 0 {
		for _, anon := range serverList {
			entry := anon.(map[string]interface{})
			if entry["name"].(string) == name {
				host := abstract.NewHostCore()
				host.ID = entry["id"].(string)
				host.Name = name
				hostFull, err := s.InspectHost(host)
				if err != nil {
					return nil, err
				}
				return hostFull.Core, nil
			}
		}
	}
	return nil, abstract.ResourceNotFoundError("host", name)
}

// CreateHost creates an host satisfying request
func (s *Stack) CreateHost(request abstract.HostRequest) (host *abstract.HostFull, userData *userdata.Content, xerr fail.Error) {
	if s == nil {
		return nil, nil, fail.InvalidInstanceError()
	}

	defer concurrency.NewTracer(nil, debug.ShouldTrace("stack.compute"), "(%s)", request.ResourceName).WithStopwatch().Entering().OnExitTrace()
	defer fail.OnPanic(&xerr)

	userData = userdata.NewContent()

	msgFail := "failed to create Host resource: %s"
	msgSuccess := fmt.Sprintf("Host resource '%s' created successfully", request.ResourceName)

	if len(request.Networks) == 0 && !request.PublicIP {
		return nil, userData, abstract.ResourceInvalidRequestError("host creation", "cannot create a host without public IP or without attached network")
	}

	// The Default Network is the first of the provided list, by convention
	defaultNetwork := request.Networks[0]
	defaultNetworkID := defaultNetwork.ID

	var nets []servers.Network
	// If floating IPs are not used and host is public
	// then add provider network to host networks
	if !s.cfgOpts.UseFloatingIP && request.PublicIP {
		nets = append(nets, servers.Network{
			UUID: s.ProviderNetworkID,
		})
	}
	// Add private networks
	for _, n := range request.Networks {
		nets = append(nets, servers.Network{
			UUID: n.ID,
		})
	}

	// If no key pair is supplied create one
	if request.KeyPair == nil {
		id, err := uuid.NewV4()
		if err != nil {
			xerr = fail.Wrap(err, "failed to create host UUID")
			logrus.Debugf(strprocess.Capitalize(xerr.Error()))
			return nil, userData, xerr
		}

		name := fmt.Sprintf("%s_%s", request.ResourceName, id)
		request.KeyPair, err = s.CreateKeyPair(name)
		if err != nil {
			xerr = fail.Wrap(err, "failed to create host key pair")
			logrus.Debugf(strprocess.Capitalize(xerr.Error()))
			return nil, userData, xerr
		}
	}
	if request.Password == "" {
		password, err := utils.GeneratePassword(16)
		if err != nil {
			return nil, userData, fail.Wrap(err, "failed to generate password")
		}
		request.Password = password
	}

	// --- prepares data structures for Provider usage ---

	// Constructs userdata content
	xerr = userData.Prepare(s.cfgOpts, request, defaultNetwork.CIDR, "")
	if xerr != nil {
		xerr = fail.Wrap(xerr, "failed to prepare user data content")
		logrus.Debugf(strprocess.Capitalize(xerr.Error()))
		return nil, userData, xerr
	}

	template, xerr := s.GetTemplate(request.TemplateID)
	if xerr != nil {
		return nil, userData, fail.NewError("failed to get image: %s", ProviderErrorToString(xerr))
	}

	// Select usable availability zone, the first one in the list
	azone, err := s.SelectedAvailabilityZone()
	if err != nil {
		return nil, userData, err
	}

	// Sets provider parameters to create host
	userDataPhase1, err := userData.Generate(userdata.PHASE1_INIT)
	if err != nil {
		return nil, userData, err
	}
	srvOpts := servers.CreateOpts{
		Name:             request.ResourceName,
		SecurityGroups:   []string{s.SecurityGroup.Name},
		Networks:         nets,
		FlavorRef:        request.TemplateID,
		ImageRef:         request.ImageID,
		UserData:         userDataPhase1,
		AvailabilityZone: azone,
	}

	// --- Initializes abstract.HostCore ---

	ahc := abstract.NewHostCore()
	ahc.PrivateKey = request.KeyPair.PrivateKey
	ahc.Password = request.Password

	// --- query provider for host creation ---

	logrus.Debugf("requesting host '%s' resource creation...", request.ResourceName)
	// Retry creation until success, for 10 minutes
	var server *servers.Server
	retryErr := retry.WhileUnsuccessfulDelay5Seconds(
		func() error {
			var innerErr error
			server, innerErr = servers.Create(s.ComputeClient, keypairs.CreateOptsExt{
				CreateOptsBuilder: srvOpts,
			}).Extract()
			if innerErr != nil {
				if server != nil {
					servers.Delete(s.ComputeClient, server.ID)
				}
				msg := ProviderErrorToString(err)
				logrus.Warnf(msg)
				return fail.NewError(msg)
			}
			if server == nil {
				return fail.NewError("failed to create server")
			}

			creationZone, zoneErr := s.GetAvailabilityZoneOfServer(server.ID)
			if zoneErr != nil {
				logrus.Tracef("Host successfully created but can't confirm AZ: %s", zoneErr)
			} else {
				logrus.Tracef("Host successfully created in requested AZ '%s'", creationZone)
				if creationZone != srvOpts.AvailabilityZone {
					if srvOpts.AvailabilityZone != "" {
						logrus.Warnf("Host created in the WRONG availability zone: requested '%s' and got instead '%s'", srvOpts.AvailabilityZone, creationZone)
					}
				}
			}

			// Starting from here, delete host if exiting with error
			defer func() {
				if innerErr != nil {
					servers.Delete(s.ComputeClient, server.ID)
				}
			}()

			ahc.ID = server.ID
			ahc.Name = server.Name

			// Wait that host is ready, not just that the build is started
			server, innerErr = s.WaitHostState(ahc, hoststate.STARTED, temporal.GetHostTimeout())
			if innerErr != nil {
				switch innerErr.(type) {
				case fail.ErrNotAvailable:
					return fail.NewError("host '%s' is in ERROR state", request.ResourceName)
				default:
					return fail.NewError("timeout waiting host '%s' ready: %s", request.ResourceName, ProviderErrorToString(innerErr))
				}
			}
			return nil
		},
		temporal.GetLongOperationTimeout(),
	)
	if retryErr != nil {
		return nil, userData, fail.Wrap(retryErr, "error creating host")
	}

	logrus.Debugf("host resource created.")

	// Starting from here, delete host if exiting with error
	defer func() {
		if xerr != nil {
			logrus.Infof("Cleaning up on failure, deleting host '%s'", ahc.Name)
			derr := s.DeleteHost(ahc.ID)
			if derr != nil {
				switch derr.(type) {
				case *fail.ErrNotFound:
					logrus.Errorf("Cleaning up on failure, failed to delete host, resource not found: '%v'", derr)
				case *fail.ErrTimeout:
					logrus.Errorf("Cleaning up on failure, failed to delete host, timeout: '%v'", derr)
				default:
					logrus.Errorf("Cleaning up on failure, failed to delete host: '%v'", derr)
				}
				_ = fail.AddConsequence(xerr, derr)
			}
		}
	}()

	newHost, err := s.complementHost(ahc, *server)
	if err != nil {
		return nil, nil, err
	}
	newHost.Network.DefaultNetworkID = defaultNetworkID
	// newHost.Network.DefaultGatewayID = defaultGatewayID
	// newHost.Network.DefaultGatewayPrivateIP = request.DefaultRouteIP
	newHost.Network.IsGateway = request.IsGateway
	newHost.Sizing = converters.HostTemplateToHostEffectiveSizing(*template)

	// if Floating IP are used and public address is requested
	if s.cfgOpts.UseFloatingIP && request.PublicIP {
		// Create the floating IP
		ip, err := floatingips.Create(s.ComputeClient, floatingips.CreateOpts{
			Pool: s.authOpts.FloatingIPPool,
		}).Extract()
		if err != nil {
			return nil, userData, fail.Wrap(err, msgFail, ProviderErrorToString(err))
		}

		// Starting from here, delete Floating IP if exiting with error
		defer func() {
			if err != nil {
				logrus.Debugf("Cleaning up on failure, deleting floating ip '%s'", ip.ID)
				derr := floatingips.Delete(s.ComputeClient, ip.ID).ExtractErr()
				if derr != nil {
					logrus.Errorf("Error deleting Floating IP: %v", derr)
					err = fail.AddConsequence(err, derr)
				}
			}
		}()

		// Associate floating IP to host
		err = floatingips.AssociateInstance(s.ComputeClient, newHost.Core.ID, floatingips.AssociateOpts{
			FloatingIP: ip.IP,
		}).ExtractErr()
		if err != nil {
			msg := fmt.Sprintf(msgFail, ProviderErrorToString(err))
			return nil, userData, fail.Wrap(err, msg)
		}

		if ipversion.IPv4.Is(ip.IP) {
			newHost.Network.PublicIPv4 = ip.IP
		} else if ipversion.IPv6.Is(ip.IP) {
			newHost.Network.PublicIPv6 = ip.IP
		}
		userData.PublicIP = ip.IP
	}

	logrus.Infoln(msgSuccess)
	return newHost, userData, nil
}

// GetAvailabilityZoneOfServer retrieves the availability zone of server 'serverID'
func (s *Stack) GetAvailabilityZoneOfServer(serverID string) (string, fail.Error) {
	type ServerWithAZ struct {
		servers.Server
		az.ServerAvailabilityZoneExt
	}
	var allServers []ServerWithAZ
	allPages, err := servers.List(s.ComputeClient, nil).AllPages()
	if err != nil {
		return "", fail.Wrap(err, "unable to retrieve servers")
	}
	err = servers.ExtractServersInto(allPages, &allServers)
	if err != nil {
		return "", fail.Wrap(err, "unable to extract servers")
	}
	for _, server := range allServers {
		if server.ID == serverID {
			return server.AvailabilityZone, nil
		}
	}

	return "", fail.NotFoundError("unable to find availability zone information for server '%s'", serverID)
}

// SelectedAvailabilityZone returns the selected availability zone
func (s *Stack) SelectedAvailabilityZone() (string, fail.Error) {
	if s == nil {
		return "", fail.InvalidInstanceError()
	}

	if s.selectedAvailabilityZone == "" {
		s.selectedAvailabilityZone = s.GetAuthenticationOptions().AvailabilityZone
		if s.selectedAvailabilityZone == "" {
			azList, xerr := s.ListAvailabilityZones()
			if xerr != nil {
				return "", xerr
			}
			var azone string
			for azone = range azList {
				break
			}
			s.selectedAvailabilityZone = azone
		}
		logrus.Debugf("Selected Availability Zone: '%s'", s.selectedAvailabilityZone)
	}
	return s.selectedAvailabilityZone, nil
}

// WaitHostReady waits an host achieve ready state
// hostParam can be an ID of host, or an instance of *abstract.HostCore; any other type will return an utils.ErrInvalidParameter
func (s *Stack) WaitHostReady(hostParam stacks.HostParameter, timeout time.Duration) (*abstract.HostCore, fail.Error) {
	nullAhc := abstract.NewHostCore()
	if s == nil {
		return nullAhc, fail.InvalidInstanceError()
	}

	ahf, _, xerr := stacks.ValidateHostParameter(hostParam)
	if xerr != nil {
		return nullAhc, xerr
	}
	server, xerr := s.WaitHostState(hostParam, hoststate.STARTED, timeout)
	if xerr != nil {
		return nullAhc, xerr
	}
	ahf, xerr = s.complementHost(ahf.Core, *server)
	if xerr != nil {
		return nullAhc, xerr
	}
	return ahf.Core, nil
}

// WaitHostState waits an host achieve defined state
// hostParam can be an ID of host, or an instance of *abstract.HostCore; any other type will return an utils.ErrInvalidParameter
func (s *Stack) WaitHostState(hostParam stacks.HostParameter, state hoststate.Enum, timeout time.Duration) (server *servers.Server, xerr fail.Error) {
	if s == nil {
		return nil, fail.InvalidInstanceError()
	}

	ahf, hostRef, err := stacks.ValidateHostParameter(hostParam)
	if err != nil {
		return nil, fail.ToError(err)
	}

	defer concurrency.NewTracer(nil, true, "(%s, %s, %v)", hostRef, state.String(), timeout).WithStopwatch().Entering().OnExitTrace()

	retryErr := retry.WhileUnsuccessful(
		func() error {
			var err error
			server, err = servers.Get(s.ComputeClient, ahf.Core.ID).Extract()
			if err != nil {
				switch err.(type) {
				case gophercloud.ErrDefault404:
					// If error is "resource not found", we want to return GopherCloud error as-is to be able
					// to behave differently in this special case. To do so, stop the retry
					return retry.StopRetryError(abstract.ResourceNotFoundError("host", hostRef), "")
				case gophercloud.ErrDefault408:
					// server timeout, retries
					return err
				case gophercloud.ErrDefault409:
					// specific handling for error 409
					return retry.StopRetryError(nil, "error getting host '%s': %s", hostRef, ProviderErrorToString(err))
				case gophercloud.ErrDefault429:
					// rate limiting defined by provider, retry
					return err
				case gophercloud.ErrDefault503:
					// Service Unavailable, retry
					return err
				case gophercloud.ErrDefault500:
					// When the response is "Internal Server Error", retries
					return err
				}

				errorCode, failed := GetUnexpectedGophercloudErrorCode(err)
				if failed == nil {
					switch errorCode {
					case 408:
						return err
					case 429:
						return err
					case 500:
						return err
					case 503:
						return err
					default:
						return retry.StopRetryError(nil, "error getting host '%s': code: %d, reason: %s", hostRef, errorCode, err)
					}
				}

				if IsServiceUnavailableError(err) {
					return err
				}

				// Any other error stops the retry
				return retry.StopRetryError(nil, "error getting host '%s': %s", hostRef, ProviderErrorToString(err))
			}

			if server == nil {
				return fail.NotFoundError("provider did not send information for host '%s'", hostRef)
			}

			lastState := toHostState(server.Status)
			// If state matches, we consider this a success no matter what
			if lastState == state {
				return nil
			}

			if lastState == hoststate.ERROR {
				return retry.StopRetryError(abstract.ResourceNotAvailableError("host", hostRef), "")
			}

			if lastState != hoststate.STARTING && lastState != hoststate.STOPPING {
				return retry.StopRetryError(nil, "host status of '%s' is in state '%s', and that's not a transition state", hostRef, server.Status)
			}

			return fmt.Errorf("server '%s' not ready yet", hostRef)
		},
		temporal.GetMinDelay(),
		timeout,
	)
	if retryErr != nil {
		switch retryErr.(type) {
		case *fail.ErrTimeout:
			return nil, fail.TimeoutError(retryErr.Cause(), timeout, "timeout waiting to get host '%s' information after %v", hostRef, timeout)
		case *fail.ErrAborted:
			return nil, retryErr
		default:
			return nil, retryErr
		}
	}
	return server, nil
}

// GetHostState returns the current state of host identified by id
// hostParam can be a string or an instance of *abstract.HostCore; any other type will return an fail.InvalidParameterError
func (s *Stack) GetHostState(hostParam stacks.HostParameter) (hoststate.Enum, fail.Error) {
	if s == nil {
		return hoststate.ERROR, fail.InvalidInstanceError()
	}

	defer concurrency.NewTracer(nil, false, "").WithStopwatch().Entering().OnExitTrace()

	host, err := s.InspectHost(hostParam)
	if err != nil {
		return hoststate.ERROR, err
	}
	return host.Core.LastState, nil
}

// ListHosts lists all hosts
func (s *Stack) ListHosts(details bool) (abstract.HostList, fail.Error) {
	if s == nil {
		return nil, fail.InvalidInstanceError()
	}

	defer concurrency.NewTracer(nil, debug.ShouldTrace("stack.compute"), "").WithStopwatch().Entering().OnExitTrace()

	pager := servers.List(s.ComputeClient, servers.ListOpts{})
	hostList := abstract.HostList{}
	err := pager.EachPage(func(page pagination.Page) (bool, error) {
		list, err := servers.ExtractServers(page)
		if err != nil {
			return false, err
		}

		for _, srv := range list {
			ahc := abstract.NewHostCore()
			ahc.ID = srv.ID
			var ahf *abstract.HostFull
			if details {
				ahf, err = s.complementHost(ahc, srv)
				if err != nil {
					return false, err
				}
			} else {
				ahf = abstract.NewHostFull()
				ahf.Core = ahc
			}
			hostList = append(hostList, ahf)
		}
		return true, nil
	})
	if err != nil {
		return nil, fail.Wrap(err, "error listing hosts : %s", ProviderErrorToString(err))
	}
	return hostList, nil
}

// getFloatingIP returns the floating IP associated with the host identified by hostID
// By convention only one floating IP is allocated to an host
func (s *Stack) getFloatingIP(hostID string) (*floatingips.FloatingIP, fail.Error) {
	if s == nil {
		return nil, fail.InvalidInstanceError()
	}

	pager := floatingips.List(s.ComputeClient)
	var fips []floatingips.FloatingIP
	err := pager.EachPage(func(page pagination.Page) (bool, error) {
		list, err := floatingips.ExtractFloatingIPs(page)
		if err != nil {
			return false, err
		}

		for _, fip := range list {
			if fip.InstanceID == hostID {
				fips = append(fips, fip)
			}
		}
		return true, nil
	})
	if len(fips) == 0 {
		if err != nil {
			return nil, fail.Wrap(err, "No floating IP found for host '%s': %s", hostID, ProviderErrorToString(err))
		}
		return nil, fail.Wrap(err, "No floating IP found for host '%s'", hostID)

	}
	if len(fips) > 1 {
		return nil, fail.Wrap(err, "Configuration error, more than one Floating IP associated to host '%s'", hostID)
	}
	return &fips[0], nil
}

// DeleteHost deletes the host identified by id
func (s *Stack) DeleteHost(hostParam stacks.HostParameter) fail.Error {
	if s == nil {
		return fail.InvalidInstanceError()
	}
	ahf, hostRef, xerr := stacks.ValidateHostParameter(hostParam)
	if xerr != nil {
		return xerr
	}

	defer concurrency.NewTracer(nil, debug.ShouldTrace("stack.compute"), "(%s)", hostRef).WithStopwatch().Entering().OnExitTrace()

	if s.cfgOpts.UseFloatingIP {
		fip, xerr := s.getFloatingIP(ahf.Core.ID)
		if xerr != nil {
			return fail.Wrap(xerr, "error retrieving floating ip of host '%s'", hostRef)
		}
		if fip != nil {
			err := floatingips.DisassociateInstance(s.ComputeClient, ahf.Core.ID, floatingips.DisassociateOpts{
				FloatingIP: fip.IP,
			}).ExtractErr()
			if err != nil {
				return fail.Wrap(NormalizeError(err), "error deleting host '%s': %s", hostRef, ProviderErrorToString(err))
			}
			err = floatingips.Delete(s.ComputeClient, fip.ID).ExtractErr()
			if err != nil {
				return fail.Wrap(NormalizeError(err), "error deleting host '%s' : %s", hostRef, ProviderErrorToString(err))
			}
		}
	}

	// Try to remove host for 3 minutes
	resourcePresent := true
	outerRetryErr := retry.WhileUnsuccessful(
		func() error {
			// 1st, send delete host order
			if resourcePresent {
				innerErr := servers.Delete(s.ComputeClient, ahf.Core.ID).ExtractErr()
				if innerErr != nil {
					switch innerErr.(type) {
					case gophercloud.ErrDefault404:
						// Resource not found, consider deletion successful
						logrus.Debugf("Host '%s' not found, deletion considered successful", hostRef)
						resourcePresent = false
						return nil
					default:
						return fail.NewError("failed to submit host '%s' deletion: %s", hostRef, ProviderErrorToString(innerErr))
					}
				}
			}
			// 2nd, check host status every 5 seconds until check failed.
			// If check succeeds but state is Error, retry the deletion.
			// If check fails and error isn't 'resource not found', retry
			if resourcePresent {
				innerErr := retry.WhileUnsuccessfulDelay5Seconds(
					func() error {
						host, err := servers.Get(s.ComputeClient, ahf.Core.ID).Extract()
						if err == nil {
							if toHostState(host.Status) == hoststate.ERROR {
								return nil
							}
							return fail.NotAvailableError("host '%s' state is '%s'", host.Name, host.Status)
						}

						switch err.(type) { // nolint
						case gophercloud.ErrDefault404:
							resourcePresent = false
							return nil
						}
						return err
					},
					temporal.GetContextTimeout(),
				)
				if innerErr != nil {
					if _, ok := innerErr.(*fail.ErrTimeout); ok {
						// retry deletion...
						return abstract.ResourceTimeoutError("host", hostRef, temporal.GetContextTimeout())
					}
					return innerErr
				}
			}
			if !resourcePresent {
				// logrus.Debugf("Host '%s' not found, deletion considered successful after a few retries", id)
				return nil
			}
			return fail.NotAvailableError("host '%s' in state 'ERROR', retrying to delete", hostRef)
		},
		0,
		temporal.GetHostCleanupTimeout(),
	)
	if outerRetryErr != nil {
		return fail.Wrap(outerRetryErr, "error deleting host: retry error")
	}
	if !resourcePresent {
		return abstract.ResourceNotFoundError("host", hostRef)
	}
	return nil
}

// StopHost stops the host identified by id
func (s *Stack) StopHost(hostParam stacks.HostParameter) fail.Error {
	if s == nil {
		return fail.InvalidInstanceError()
	}
	ahf, hostRef, xerr := stacks.ValidateHostParameter(hostParam)
	if xerr != nil {
		return xerr
	}

	defer concurrency.NewTracer(nil, debug.ShouldTrace("stack.compute"), "(%s)", hostRef).WithStopwatch().Entering().OnExitTrace()

	err := startstop.Stop(s.ComputeClient, ahf.Core.ID).ExtractErr()
	if err != nil {
		return fail.Wrap(NormalizeError(err), "error stopping host")
	}
	return nil
}

// RebootHost reboots unconditionally the host identified by id
func (s *Stack) RebootHost(hostParam stacks.HostParameter) fail.Error {
	if s == nil {
		return fail.InvalidInstanceError()
	}
	ahf, hostRef, xerr := stacks.ValidateHostParameter(hostParam)
	if xerr != nil {
		return xerr
	}

	defer concurrency.NewTracer(nil, debug.ShouldTrace("stack.compute"), "(%s)", hostRef).WithStopwatch().Entering().OnExitTrace()

	// Try first a soft reboot, and if it fails (because host isn't in ACTIVE state), tries a hard reboot
	err := servers.Reboot(s.ComputeClient, ahf.Core.ID, servers.RebootOpts{Type: servers.SoftReboot}).ExtractErr()
	if err != nil {
		err = servers.Reboot(s.ComputeClient, ahf.Core.ID, servers.RebootOpts{Type: servers.HardReboot}).ExtractErr()
	}
	if err != nil {
		return fail.Wrap(NormalizeError(err), "error rebooting host '%s'", hostRef)
	}
	return nil
}

// StartHost starts the host identified by id
func (s *Stack) StartHost(hostParam stacks.HostParameter) fail.Error {
	if s == nil {
		return fail.InvalidInstanceError()
	}
	ahf, hostRef, xerr := stacks.ValidateHostParameter(hostParam)
	if xerr != nil {
		return xerr
	}

	defer concurrency.NewTracer(nil, debug.ShouldTrace("stack.compute"), "(%s)", hostRef).WithStopwatch().Entering().OnExitTrace()

	err := startstop.Start(s.ComputeClient, ahf.Core.ID).ExtractErr()
	if err != nil {
		return fail.Wrap(NormalizeError(err), "error starting host '%s'", hostRef)
	}

	return nil
}

// ResizeHost ...
func (s *Stack) ResizeHost(hostParam stacks.HostParameter, request abstract.HostSizingRequirements) (*abstract.HostFull, fail.Error) {
	if s == nil {
		return nil, fail.InvalidInstanceError()
	}
	_/*ahf*/, hostRef, xerr := stacks.ValidateHostParameter(hostParam)
	if xerr != nil {
		return nil, xerr
	}

	defer concurrency.NewTracer(nil, debug.ShouldTrace("stack.compute"), "(%s)", hostRef).WithStopwatch().Entering().OnExitTrace()

	// TODO: RESIZE Resize Host HERE
	logrus.Warn("Trying to resize a Host...")

	// TODO: RESIZE Call this
	// servers.Resize()

	return nil, fail.NotImplementedError("ResizeHost() not implemented yet") // FIXME: Technical debt
}
