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

package openstack

import (
	"fmt"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/bootfromvolume"

	"github.com/CS-SI/SafeScale/lib/utils/debug"

	"github.com/davecgh/go-spew/spew"
	"github.com/sirupsen/logrus"

	gc "github.com/gophercloud/gophercloud"
	az "github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/availabilityzones"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/floatingips"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/keypairs"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/startstop"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/regions"
	"github.com/gophercloud/gophercloud/openstack/imageservice/v2/images"
	"github.com/gophercloud/gophercloud/pagination"

	"github.com/CS-SI/SafeScale/lib/server/iaas/abstract"
	"github.com/CS-SI/SafeScale/lib/server/iaas/abstract/enums/hostproperty"
	"github.com/CS-SI/SafeScale/lib/server/iaas/abstract/enums/hoststate"
	"github.com/CS-SI/SafeScale/lib/server/iaas/abstract/enums/ipversion"
	converters "github.com/CS-SI/SafeScale/lib/server/iaas/abstract/properties"
	propsv1 "github.com/CS-SI/SafeScale/lib/server/iaas/abstract/properties/v1"
	"github.com/CS-SI/SafeScale/lib/server/iaas/abstract/userdata"
	"github.com/CS-SI/SafeScale/lib/utils"
	"github.com/CS-SI/SafeScale/lib/utils/data"
	"github.com/CS-SI/SafeScale/lib/utils/fail"
	"github.com/CS-SI/SafeScale/lib/utils/retry"
	"github.com/CS-SI/SafeScale/lib/utils/temporal"
)

// ListRegions ...
func (s *Stack) ListRegions() ([]string, fail.Error) {
	defer debug.NewTracer(nil, "", true).WithStopwatch().GoingIn().OnExitTrace()()

	listOpts := regions.ListOpts{
		ParentRegionID: "RegionOne",
	}

	var results []string
	allPages, err := regions.List(s.ComputeClient, listOpts).AllPages()
	if err != nil {
		return results, err
	}

	allRegions, err := regions.ExtractRegions(allPages)
	if err != nil {
		return results, err
	}

	for _, reg := range allRegions {
		results = append(results, reg.ID)
	}

	return results, nil
}

// ListAvailabilityZones lists the usable AvailabilityZones
func (s *Stack) ListAvailabilityZones() (list map[string]bool, xerr fail.Error) {
	tracer := debug.NewTracer(nil, "", true).GoingIn()
	defer tracer.OnExitTrace()()
	defer fail.OnExitLogError(tracer.TraceMessage(""), &xerr)()

	allPages, err := az.List(s.ComputeClient).AllPages()
	if err != nil {
		return nil, err
	}

	content, err := az.ExtractAvailabilityZones(allPages)
	if err != nil {
		return nil, err
	}

	azList := map[string]bool{}
	for _, zone := range content {
		if zone.ZoneState.Available {
			azList[zone.ZoneName] = zone.ZoneState.Available
		}
	}

	if len(azList) == 0 {
		logrus.Warnf("no Availability Zones detected !")
	}

	return azList, nil
}

// ListImages lists available OS images
func (s *Stack) ListImages() (imgList []abstract.Image, xerr fail.Error) {
	tracer := debug.NewTracer(nil, "", true).WithStopwatch().GoingIn()
	defer tracer.OnExitTrace()()
	defer fail.OnExitLogError(tracer.TraceMessage(""), &xerr)()

	opts := images.ListOpts{}

	// Retrieve a pager (i.e. a paginated collection)
	pager := images.List(s.ComputeClient, opts)

	// Define an anonymous function to be executed on each page's iteration
	xerr = pager.EachPage(
		func(page pagination.Page) (bool, fail.Error) {
			imageList, err := images.ExtractImages(page)
			if err != nil {
				return false, err
			}

			for _, img := range imageList {
				imgList = append(imgList, abstract.Image{ID: img.ID, Name: img.Name, DiskSize: int64(img.MinDiskGigabytes)})

			}
			return true, nil
		},
	)
	if (len(imgList) == 0) || (xerr != nil) {
		if xerr != nil {
			return nil, fail.Wrap(xerr, fmt.Sprintf("error listing images: %s", ProviderErrorToString(xerr)))
		}
		logrus.Debugf("Image list empty !")
	}
	return imgList, nil
}

// GetImage returns the Image referenced by id
func (s *Stack) GetImage(id string) (image *abstract.Image, xerr fail.Error) {
	tracer := debug.NewTracer(nil, fmt.Sprintf("(%s)", id), true).WithStopwatch().GoingIn()
	defer tracer.OnExitTrace()()
	defer fail.OnExitLogError(tracer.TraceMessage(""), &xerr)()

	var img *images.Image

	// Try 10s to get image
	retryErr := retry.WhileUnsuccessfulDelay1Second(
		func() error {
			extractedImg, err := images.Get(s.ComputeClient, id).Extract()
			if err != nil {
				return err
			}

			img = extractedImg
			return nil
		},
		2*temporal.GetDefaultDelay(),
	)
	if retryErr != nil {
		return nil, fail.Wrap(xerr, fmt.Sprintf("error getting image: %s", ProviderErrorToString(xerr)))
	}

	return &abstract.Image{ID: img.ID, Name: img.Name, DiskSize: int64(img.MinDiskGigabytes)}, nil
}

// GetTemplate returns the Template referenced by id
func (s *Stack) GetTemplate(id string) (template *abstract.HostTemplate, xerr fail.Error) {
	tracer := debug.NewTracer(nil, fmt.Sprintf("(%s)", id), true).WithStopwatch().GoingIn()
	defer tracer.OnExitTrace()()
	defer fail.OnExitLogError(tracer.TraceMessage(""), &xerr)()

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
		return nil, fail.Wrap(retryErr, fmt.Sprintf("error getting template: %s", ProviderErrorToString(retryErr)))
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
	tracer := debug.NewTracer(nil, "", true).WithStopwatch().GoingIn()
	defer tracer.OnExitTrace()()

	opts := flavors.ListOpts{}

	// Retrieve a pager (i.e. a paginated collection)
	var (
		flvList []abstract.HostTemplate
		pager   pagination.Pager
	)

	// Define an anonymous function to be executed on each page's iteration
	err := retry.WhileUnsuccessfulDelay1Second(
		func() error {
			pager = flavors.ListDetail(s.ComputeClient, opts)
			return pager.EachPage(
				func(page pagination.Page) (bool, fail.Error) {
					flavorList, err := flavors.ExtractFlavors(page)
					if err != nil {
						return false, err
					}
					for _, flv := range flavorList {
						flvList = append(
							flvList, abstract.HostTemplate{
								Cores:    flv.VCPUs,
								RAMSize:  float32(flv.RAM) / 1000.0,
								DiskSize: flv.Disk,
								ID:       flv.ID,
								Name:     flv.Name,
							},
						)
					}
					return true, nil
				},
			)
		},
		time.Minute*2,
	)
	if err != nil {
		switch err.(type) {
		case fail.ErrTimeout:
			return nil, err
		default:
			return nil, fail.Wrap(err, "error listing templates")
		}
	}
	if len(flvList) == 0 {
		logrus.Debugf("Template list empty.")
	}
	return flvList, nil
}

// TODO: restore code that call Openstack API to create keypair (even if we will not use it in SafeScale)
// CreateKeyPair creates a key pair (no import)
func (s *Stack) CreateKeyPair(name string) (*abstract.KeyPair, fail.Error) {
	tracer := debug.NewTracer(nil, fmt.Sprintf("(%s)", name), true).WithStopwatch().GoingIn()
	defer tracer.OnExitTrace()()

	return abstract.NewKeyPair(name)
}

// ImportKeyPair imports a keypair in OpenStack
func (s *Stack) ImportKeyPair(keypair *abstract.KeyPair) error {
	return fail.NotImplementedError("ImportKeyPair is not implemented yet") // FIXME: Technical debt
}

// GetKeyPair returns the key pair identified by id
func (s *Stack) GetKeyPair(id string) (*abstract.KeyPair, fail.Error) {
	defer debug.NewTracer(nil, fmt.Sprintf("(%s)", id), true).WithStopwatch().GoingIn().OnExitTrace()()

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
	defer debug.NewTracer(nil, "", true).WithStopwatch().GoingIn().OnExitTrace()()

	// Retrieve a pager (i.e. a paginated collection)
	pager := keypairs.List(s.ComputeClient)

	var kpList []abstract.KeyPair

	// Define an anonymous function to be executed on each page's iteration
	err := pager.EachPage(
		func(page pagination.Page) (bool, fail.Error) {
			keyList, err := keypairs.ExtractKeyPairs(page)
			if err != nil {
				return false, err
			}

			for _, kp := range keyList {
				kpList = append(
					kpList, abstract.KeyPair{
						ID:         kp.Name,
						Name:       kp.Name,
						PublicKey:  kp.PublicKey,
						PrivateKey: kp.PrivateKey,
					},
				)
			}
			return true, nil
		},
	)
	if (len(kpList) == 0) || (err != nil) {
		if err != nil {
			return nil, fail.Wrap(err, "error listing keypairs")
		}
	}
	return kpList, nil
}

// DeleteKeyPair deletes the key pair identified by id
func (s *Stack) DeleteKeyPair(id string) error {
	defer debug.NewTracer(nil, fmt.Sprintf("(%s)", id), true).WithStopwatch().GoingIn().OnExitTrace()()

	err := keypairs.Delete(s.ComputeClient, id).ExtractErr()
	if err != nil {
		return fail.Wrap(err, fmt.Sprintf("error deleting key pair: %s", ProviderErrorToString(err)))
	}
	return nil
}

// toHostSize converts flavor attributes returned by OpenStack driver into mdel.Host
func (s *Stack) toHostSize(flavor map[string]interface{}) *propsv1.HostSize {
	hostSize := propsv1.NewHostSize()
	if i, ok := flavor["id"]; ok {
		fid := i.(string)
		tpl, err := s.GetTemplate(fid)
		if err == nil {
			hostSize.Cores = tpl.Cores
			hostSize.DiskSize = tpl.DiskSize
			hostSize.RAMSize = tpl.RAMSize
		}
	} else if _, ok := flavor["vcpus"]; ok {
		hostSize.Cores = flavor["vcpus"].(int)
		hostSize.DiskSize = flavor["disk"].(int)
		hostSize.RAMSize = flavor["ram"].(float32) / 1000.0
	}
	return hostSize
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

// InspectHost updates the data inside host with the data from provider
func (s *Stack) InspectHost(hostParam interface{}) (host *abstract.Host, xerr fail.Error) {
	switch hostParam := hostParam.(type) {
	case string:
		if hostParam == "" {
			return nil, fail.InvalidParameterError("hostParam", "cannot be an empty string")
		}
		host = abstract.NewHost()
		host.ID = hostParam
	case *abstract.Host:
		if hostParam == nil {
			return nil, fail.InvalidParameterError("hostParam", "cannot be nil")
		}
		host = hostParam
	default:
		return nil, fail.InvalidParameterError("hostParam", "must be a string or a *abstract.Host")
	}
	hostRef := host.Name
	if hostRef == "" {
		hostRef = host.ID
	}

	defer debug.NewTracer(nil, fmt.Sprintf("(%s)", hostRef), true).WithStopwatch().GoingIn().OnExitTrace()()

	serverState, err := s.GetHostState(host.ID)
	if err != nil {
		return nil, err
	}

	switch serverState {
	case hoststate.STARTED, hoststate.STOPPED:
		server, err := s.waitHostState(
			host.ID, []hoststate.Enum{hoststate.STARTED, hoststate.STOPPED}, 2*temporal.GetBigDelay(),
		)
		if err != nil {
			return nil, err
		}

		err = s.complementHost(host, server)
		if err != nil {
			return nil, err
		}

		if !host.OK() {
			logrus.Warnf("[TRACE] Unexpected host status: %s", spew.Sdump(host))
		}
	default:
		host.LastState = serverState
	}

	return host, nil
}

// interpretAddresses converts addresses returned by the OpenStack driver
// Returns string slice containing the name of the networks, string map of IP addresses
// (indexed on network name), public ipv4 and ipv6 (if they exists)
func (s *Stack) interpretAddresses(
	addresses map[string]interface{},
) ([]string, map[ipversion.Enum]map[string]string, string, string) {
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
			address := networkAddresses.(map[string]interface{})
			version := address["version"].(float64)
			fixedIP := address["addr"].(string)
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
	return networks, addrs, AcccessIPv4, AcccessIPv6
}

// complementHost complements Host data with content of server parameter
func (s *Stack) complementHost(host *abstract.Host, server *servers.Server) error {
	networks, addresses, ipv4, ipv6 := s.interpretAddresses(server.Addresses)

	// Updates intrinsic data of host if needed
	if host.ID == "" {
		host.ID = server.ID
	}
	if host.Name == "" {
		host.Name = server.Name
	}

	host.LastState = toHostState(server.Status)

	// Updates Host Property propsv1.HostDescription
	err := host.Properties.LockForWrite(hostproperty.DescriptionV1).ThenUse(
		func(clonable data.Clonable) error {
			hpDescriptionV1 := clonable.(*propsv1.HostDescription)
			hpDescriptionV1.Created = server.Created
			hpDescriptionV1.Updated = server.Updated
			return nil
		},
	)
	if err != nil {
		return err
	}

	// Updates Host Property propsv1.HostSizing
	err = host.Properties.LockForWrite(hostproperty.SizingV1).ThenUse(
		func(clonable data.Clonable) error {
			hpSizingV1 := clonable.(*propsv1.HostSizing)
			if hpSizingV1.AllocatedSize == nil {
				hpSizingV1.AllocatedSize = s.toHostSize(server.Flavor)
			}
			return nil
		},
	)
	if err != nil {
		return err
	}

	// Updates Host Property propsv1.HostNetwork
	return host.Properties.LockForWrite(hostproperty.NetworkV1).ThenUse(
		func(clonable data.Clonable) error {
			hostNetworkV1 := clonable.(*propsv1.HostNetwork)
			if hostNetworkV1.PublicIPv4 == "" {
				hostNetworkV1.PublicIPv4 = ipv4
			}
			if hostNetworkV1.PublicIPv6 == "" {
				hostNetworkV1.PublicIPv6 = ipv6
			}
			// networks contains network names, but hostproperty.NetworkV1.IPxAddresses has to be
			// indexed on network ID. Tries to convert if possible, if we already have correspondance
			// between network ID and network Name in Host definition
			if len(hostNetworkV1.NetworksByID) > 0 {
				ipv4Addresses := map[string]string{}
				ipv6Addresses := map[string]string{}
				for netid, netname := range hostNetworkV1.NetworksByID {
					if ip, ok := addresses[ipversion.IPv4][netname]; ok {
						ipv4Addresses[netid] = ip
					} else {
						ipv4Addresses[netid] = ""
					}

					if ip, ok := addresses[ipversion.IPv6][netname]; ok {
						ipv6Addresses[netid] = ip
					} else {
						ipv6Addresses[netid] = ""
					}
				}
				hostNetworkV1.IPv4Addresses = ipv4Addresses
				hostNetworkV1.IPv6Addresses = ipv6Addresses
			} else {
				networksByID := map[string]string{}
				ipv4Addresses := map[string]string{}
				ipv6Addresses := map[string]string{}
				// Parse networks and fill fields
				for _, netname := range networks {
					// Ignore ProviderNetwork
					if s.cfgOpts.ProviderNetwork == netname {
						continue
					}

					net, err := s.GetNetworkByName(netname)
					if err != nil {
						logrus.Debugf(
							"failed to get data for network '%s'", netname,
						) // FIXME: complementHost should be a failure
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
				hostNetworkV1.NetworksByID = networksByID
				// IPvxAddresses are here indexed by names... At least we have them...
				hostNetworkV1.IPv4Addresses = ipv4Addresses
				hostNetworkV1.IPv6Addresses = ipv6Addresses
			}

			// Updates network name and relationships if needed
			config := s.GetConfigurationOptions()
			for netid, netname := range hostNetworkV1.NetworksByID {
				if netname == "" {
					net, err := s.GetNetwork(netid)
					if err != nil {
						switch err.(type) {
						case fail.ErrNotFound:
							logrus.Errorf(err.Error()) // FIXME: complementHost should be a failure
						default:
							logrus.Errorf(
								"failed to get network '%s': %v", netid, err,
							) // FIXME: complementHost should be a failure
						}
						continue
					}
					if net.Name == config.ProviderNetwork {
						continue
					}
					hostNetworkV1.NetworksByID[netid] = net.Name
					hostNetworkV1.NetworksByName[net.Name] = netid
				}
			}

			return nil
		},
	)
}

// GetHostByName returns the host using the name passed as parameter
func (s *Stack) GetHostByName(name string) (*abstract.Host, fail.Error) {
	defer debug.NewTracer(nil, fmt.Sprintf("('%s')", name), true).WithStopwatch().GoingIn().OnExitTrace()()

	// Gophercloud doesn't propose the way to get a host by name, but OpenStack knows how to do it...
	r := servers.GetResult{}
	_, r.Err = s.ComputeClient.Get(
		s.ComputeClient.ServiceURL("servers?name="+name), &r.Body, &gc.RequestOpts{
			OkCodes: []int{200, 203},
		},
	)
	if r.Err != nil {
		return nil, fail.Errorf(fmt.Sprintf("failed to get data of host '%s': %v", name, r.Err), r.Err)
	}
	serverList, found := r.Body.(map[string]interface{})["servers"].([]interface{})
	if found && len(serverList) > 0 {
		for _, anon := range serverList {
			entry := anon.(map[string]interface{})
			if entry["name"].(string) == name {
				host := abstract.NewHost()
				host.ID = entry["id"].(string)
				host.Name = name
				return s.InspectHost(host)
			}
		}
	}
	return nil, abstract.ResourceNotFoundError("host", name)
}

// CreateHost creates an host satisfying request
func (s *Stack) CreateHost(request abstract.HostRequest) (host *abstract.Host, userData *userdata.Content, xerr fail.Error) {
	defer debug.NewTracer(
		nil, fmt.Sprintf("(%s)", request.ResourceName), true,
	).WithStopwatch().GoingIn().OnExitTrace()()

	userData = userdata.NewContent()

	msgFail := "failed to create Host resource: %s"
	msgSuccess := fmt.Sprintf("Host resource '%s' created successfully", request.ResourceName)

	if request.DefaultGateway == nil && !request.PublicIP {
		return nil, userData, abstract.ResourceInvalidRequestError(
			"host creation", "cannot create a host without public IP or without attached network",
		)
	}

	// The Default Network is the first of the provided list, by convention
	defaultNetwork := request.Networks[0]
	defaultNetworkID := defaultNetwork.ID
	defaultGateway := request.DefaultGateway
	isGateway := defaultGateway == nil && defaultNetwork.Name != abstract.SingleHostNetworkName
	defaultGatewayID := ""
	// defaultGatewayPrivateIP := ""
	if defaultGateway != nil {
		defaultGatewayID = defaultGateway.ID
	}

	var nets []servers.Network
	// If floating IPs are not used and host is public
	// then add provider network to host networks
	if !s.cfgOpts.UseFloatingIP && request.PublicIP {
		nets = append(
			nets, servers.Network{
				UUID: s.ProviderNetworkID,
			},
		)
	}
	// Add private networks
	for _, n := range request.Networks {
		nets = append(
			nets, servers.Network{
				UUID: n.ID,
			},
		)
	}

	if request.Password == "" {
		password, err := utils.GeneratePassword(16)
		if err != nil {
			msg := fmt.Sprintf("failed to generate password: %s", err.Error())
			return nil, userData, fail.Errorf(msg, err)
		}
		request.Password = password
	}

	// --- prepares data structures for Provider usage ---

	// Constructs userdata content
	xerr = userData.Prepare(s.cfgOpts, request, defaultNetwork.CIDR, "")
	if xerr != nil {
		msg := fmt.Sprintf("failed to prepare user data content: %+v", xerr)
		return nil, userData, fail.Errorf(msg, xerr)
	}

	// Determine system disk size based on vcpus count
	template, err := s.GetTemplate(request.TemplateID)
	if err != nil {
		msg := fmt.Sprintf("failed to get image: %s", ProviderErrorToString(err))
		return nil, userData, fail.Errorf(msg, err)
	}

	// Select usable availability zone, the first one in the list
	azone, err := s.SelectedAvailabilityZone()
	if err != nil {
		return nil, userData, err
	}

	// Sets provider parameters to create host
	userDataPhase1, err := userData.Generate("phase1")
	if err != nil {
		return nil, userData, err
	}

	// FIXME: Change volume size

	srvOpts := servers.CreateOpts{
		Name:             request.ResourceName,
		SecurityGroups:   []string{s.SecurityGroup.Name},
		Networks:         nets,
		FlavorRef:        request.TemplateID,
		ImageRef:         request.ImageID,
		UserData:         userDataPhase1,
		AvailabilityZone: azone,
	}

	// --- Initializes abstract.Host ---

	host = abstract.NewHost()
	host.PrivateKey = request.KeyPair.PrivateKey // Add PrivateKey to host definition
	host.Password = request.Password

	err = host.Properties.LockForWrite(hostproperty.NetworkV1).ThenUse(
		func(clonable data.Clonable) error {
			hostNetworkV1 := clonable.(*propsv1.HostNetwork)
			hostNetworkV1.DefaultNetworkID = defaultNetworkID
			hostNetworkV1.DefaultGatewayID = defaultGatewayID
			hostNetworkV1.DefaultGatewayPrivateIP = request.DefaultRouteIP
			hostNetworkV1.IsGateway = isGateway
			return nil
		},
	)
	if err != nil {
		return nil, userData, err
	}

	// Adds Host property SizingV1
	err = host.Properties.LockForWrite(hostproperty.SizingV1).ThenUse(
		func(clonable data.Clonable) error {
			hostSizingV1 := clonable.(*propsv1.HostSizing)
			// Note: from there, no idea what was the RequestedSize; caller will have to complement this information
			hostSizingV1.Template = request.TemplateID
			hostSizingV1.AllocatedSize = converters.ModelHostTemplateToPropertyHostSize(template)
			return nil
		},
	)
	if err != nil {
		return nil, userData, err
	}

	// FIXME: Template resize

	rim, err := s.GetImage(request.ImageID)
	if err != nil {
		return nil, userData, err
	}

	if request.DiskSize > template.DiskSize {
		template.DiskSize = request.DiskSize
	}

	if int(rim.DiskSize) > template.DiskSize {
		template.DiskSize = int(rim.DiskSize)
	}

	if template.DiskSize == 0 {
		// Determines appropriate disk size
		if template.Cores < 16 { // nolint
			template.DiskSize = 100
		} else if template.Cores < 32 {
			template.DiskSize = 200
		} else {
			template.DiskSize = 400
		}
	}

	// --- query provider for host creation ---

	logrus.Debugf("requesting host resource creation...")
	// Retry creation until success, for 10 minutes
	retryErr := retry.WhileUnsuccessfulDelay5Seconds(
		func() error {

			bd := []bootfromvolume.BlockDevice{
				bootfromvolume.BlockDevice{
					UUID:       srvOpts.ImageRef,
					SourceType: bootfromvolume.SourceImage,
					VolumeSize: template.DiskSize,
				},
			}
			server, ierr := bootfromvolume.Create(s.ComputeClient, bootfromvolume.CreateOptsExt{
				CreateOptsBuilder: srvOpts,
				BlockDevice:       bd,
			}).Extract()
			if ierr != nil {
				if server != nil {
					servers.Delete(s.ComputeClient, server.ID)
				}
				msg := ProviderErrorToString(ierr)
				return fail.Errorf(msg, ierr)
			}

			creationZone, zoneErr := s.GetAvailabilityZoneOfServer(server.ID)
			if zoneErr != nil {
				logrus.Tracef("Host successfully created but can't confirm AZ: %s", zoneErr)
			} else {
				logrus.Tracef("Host successfully created in requested AZ '%s'", creationZone)
				if creationZone != srvOpts.AvailabilityZone {
					if srvOpts.AvailabilityZone != "" {
						logrus.Warnf(
							"Host created in the WRONG availability zone: requested '%s' and got instead '%s'",
							srvOpts.AvailabilityZone, creationZone,
						)
					}
				}
			}

			if server == nil {
				return fail.Errorf("failed to create server", nil)
			}
			host.ID = server.ID

			defer func() {
				if ierr != nil {
					logrus.Debugf("server destruction due to: %s", ierr.Error())
					servers.Delete(s.ComputeClient, server.ID)
				}
			}()

			// Wait that Host is ready, not just that the build is started
			var srv *servers.Server
			srv, ierr = s.waitHostState(host, []hoststate.Enum{hoststate.STARTED}, temporal.GetHostTimeout())
			if ierr != nil {
				logrus.Debugf("failure waiting for host state")
				return fail.Errorf(ProviderErrorToString(ierr), ierr)
			}

			if ierr = s.complementHost(host, srv); ierr != nil {
				logrus.Debugf("failure complementing host data")
				return fail.Errorf(ProviderErrorToString(ierr), ierr)
			}

			ierr = nil
			return nil
		},
		temporal.GetLongOperationTimeout(),
	)
	if retryErr != nil {
		return nil, userData, fail.Wrap(retryErr, "error creating host")
	}
	if host == nil {
		return nil, userData, fail.Errorf(fmt.Sprintf("unexpected problem creating host"), nil)
	}
	logrus.Debugf("host resource created.")

	// Starting from here, delete host if exiting with error
	newHost := host
	defer func() {
		if err != nil {
			logrus.Infof("Cleanup, deleting host '%s'", newHost.Name)
			derr := s.DeleteHost(newHost.ID)
			if derr != nil {
				switch derr.(type) {
				case fail.ErrNotFound:
					logrus.Errorf("Cleaning up on failure, failed to delete host, resource not found: '%v'", derr)
				case fail.ErrTimeout:
					logrus.Errorf("Cleaning up on failure, failed to delete host, timeout: '%v'", derr)
				default:
					logrus.Errorf("Cleaning up on failure, failed to delete host: '%v'", derr)
				}
				err = fail.AddConsequence(err, derr)
			}
		}
	}()

	if s.cfgOpts.UseFloatingIP && request.PublicIP {
		logrus.Trace("Creating floating ip")

		// if Floating IP are used and public address is requested
		if s.cfgOpts.UseLegacyFloatingIP {
			_, errFip := s.CreateFloatingIP()
			if errFip != nil {
				logrus.Warn("failure creating floating ip with legacy method")
			}
		}

		// Create the floating IP
		ip, ierr := floatingips.Create(
			s.ComputeClient, floatingips.CreateOpts{
				Pool: s.authOpts.FloatingIPPool,
			},
		).Extract()
		if ierr != nil {
			return nil, userData, fail.Wrap(ierr, fmt.Sprintf(msgFail, ProviderErrorToString(ierr)))
		}

		// Starting from here, delete Floating IP if exiting with error
		defer func() {
			if err != nil {
				if ip != nil {
					logrus.Debugf("cleanup, deleting floating ip '%s'", ip.ID)
					derr := floatingips.Delete(s.ComputeClient, ip.ID).ExtractErr()
					if derr != nil {
						logrus.Errorf("error deleting floating IP: %v", derr)
						err = fail.AddConsequence(err, derr)
					}
				}
			}
		}()

		logrus.Trace("Creating floating ip association")

		// Associate floating IP to host
		err = floatingips.AssociateInstance(
			s.ComputeClient, host.ID, floatingips.AssociateOpts{
				FloatingIP: ip.IP,
			},
		).ExtractErr()
		if err != nil {
			msg := fmt.Sprintf(msgFail, ProviderErrorToString(err))
			return nil, userData, fail.Wrap(err, msg)
		}

		logrus.Trace("Editing metadata")

		err = host.Properties.LockForWrite(hostproperty.NetworkV1).ThenUse(
			func(clonable data.Clonable) error {
				hostNetworkV1 := clonable.(*propsv1.HostNetwork)
				if ipversion.IPv4.Is(ip.IP) {
					hostNetworkV1.PublicIPv4 = ip.IP
				} else if ipversion.IPv6.Is(ip.IP) {
					hostNetworkV1.PublicIPv6 = ip.IP
				}
				userData.PublicIP = ip.IP
				return nil
			},
		)
		if err != nil {
			return nil, userData, err
		}
	}

	logrus.Infoln(msgSuccess)
	return host, userData, nil
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
		return "", fail.Errorf(fmt.Sprintf("unable to retrieve servers: %s", err), err)
	}
	err = servers.ExtractServersInto(allPages, &allServers)
	if err != nil {
		return "", fail.Errorf(fmt.Sprintf("unable to extract servers: %s", err), err)
	}
	for _, server := range allServers {
		if server.ID == serverID {
			return server.AvailabilityZone, nil
		}
	}

	return "", fail.Errorf(fmt.Sprintf("unable to find availability zone information for server [%s]", serverID), nil)
}

// SelectedAvailabilityZone returns the selected availability zone
func (s *Stack) SelectedAvailabilityZone() (string, fail.Error) {
	if s.selectedAvailabilityZone == "" {
		s.selectedAvailabilityZone = s.GetAuthenticationOptions().AvailabilityZone
		if s.selectedAvailabilityZone == "" {
			azList, err := s.ListAvailabilityZones()
			if err != nil {
				return "", err
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

// waitHostState waits an host achieve ready state
// hostParam can be an ID of host, or an instance of *abstract.Host; any other type will return an utils.ErrInvalidParameter
func (s *Stack) waitHostState(hostParam interface{}, states []hoststate.Enum, timeout time.Duration) (server *servers.Server, xerr fail.Error) {
	var host *abstract.Host

	switch hostParam := hostParam.(type) {
	case string:
		host = abstract.NewHost()
		host.ID = hostParam
	case *abstract.Host:
		host = hostParam
	}
	if host == nil {
		return nil, fail.InvalidParameterError("hostParam", "must be a not-empty string or a *abstract.Host!")
	}

	hostRef := host.Name
	if hostRef == "" {
		hostRef = host.ID
	}

	defer debug.NewTracer(nil, fmt.Sprintf("(%s)", hostRef), true).WithStopwatch().GoingIn().OnExitTrace()()

	previousIterationState := hoststate.UNKNOWN
	retryErr := retry.WhileUnsuccessful(
		func() error {
			server, xerr = servers.Get(s.ComputeClient, host.ID).Extract()
			if xerr != nil {
				return ReinterpretGophercloudErrorCode(
					xerr, nil, []int64{408, 429, 500, 503}, []int64{404, 409}, func(ferr error) error {
						return fail.AbortedError("", ferr)
					},
				)
			}

			if server == nil {
				return fail.Errorf("error getting host, nil response from gophercloud", nil)
			}

			lastState := toHostState(server.Status)

			// If state matches, we consider this a success no matter what
			for _, state := range states {
				if lastState == state {
					if previousIterationState != hoststate.UNKNOWN {
						logrus.Warnf("Target state of '%s': %s, current state: %s", host.ID, states, lastState)
					}
					return nil
				}
			}

			if lastState != previousIterationState {
				if previousIterationState != hoststate.UNKNOWN {
					logrus.Debugf("Target state of '%s': %s, current state: %s", host.ID, states, lastState)
				}
				previousIterationState = lastState
			}

			if lastState == hoststate.ERROR {
				return fail.AbortedError("", abstract.ResourceNotAvailableError("host", host.ID))
			}

			if !((lastState == hoststate.STARTING) || (lastState == hoststate.STOPPING)) {
				return fail.Errorf(
					fmt.Sprintf(
						"host status of '%s' is in state '%s', and that's not a transition state", host.ID,
						server.Status,
					), nil,
				)
			}

			return fail.Errorf("server not ready yet", nil)
		},
		temporal.GetMinDelay(),
		timeout,
	)
	if retryErr != nil {
		if _, ok := retryErr.(retry.ErrTimeout); ok {
			return nil, abstract.TimeoutError(
				fmt.Sprintf(
					"timeout waiting to reach acceptable host state of '%s' after %v", hostRef, timeout,
				), timeout,
			)
		}

		if aborted, ok := retryErr.(retry.ErrAborted); ok {
			return nil, aborted.Cause()
		}

		return nil, retryErr
	}

	return server, nil
}

// waitHostState waits an host achieve ready state
// hostParam can be an ID of host, or an instance of *abstract.Host; any other type will return an utils.ErrInvalidParameter
func (s *Stack) getHostState(hostParam interface{}, timeout time.Duration) (_ hoststate.Enum, xerr fail.Error) {
	var host *abstract.Host

	switch hostParam := hostParam.(type) {
	case string:
		host = abstract.NewHost()
		host.ID = hostParam
	case *abstract.Host:
		host = hostParam
	}
	if host == nil {
		return hoststate.ERROR, fail.InvalidParameterError(
			"hostParam", "must be a not-empty string or a *abstract.Host!",
		)
	}

	hostRef := host.Name
	if hostRef == "" {
		hostRef = host.ID
	}

	defer debug.NewTracer(nil, fmt.Sprintf("(%s)", hostRef), true).WithStopwatch().GoingIn().OnExitTrace()()
	lastState := hoststate.UNKNOWN

	retryErr := retry.WhileUnsuccessful(
		func() error {
			server, err := servers.Get(s.ComputeClient, host.ID).Extract()
			if err != nil {
				return ReinterpretGophercloudErrorCode(
					err, nil, []int64{408, 429, 500, 503}, []int64{404, 409}, func(ferr error) error {
						return fail.AbortedError("", ferr)
					},
				)
			}

			if server == nil {
				return fail.Errorf("error getting host, nil response from gophercloud", nil)
			}

			lastState = toHostState(server.Status)

			return nil
		},
		temporal.GetMinDelay(),
		timeout,
	)
	if retryErr != nil {
		if _, ok := retryErr.(retry.ErrTimeout); ok {
			return hoststate.ERROR, abstract.TimeoutError(
				fmt.Sprintf(
					"timeout waiting to get host '%s' information after %v", hostRef, timeout,
				), timeout,
			)
		}

		if aborted, ok := retryErr.(retry.ErrAborted); ok {
			return hoststate.ERROR, aborted.Cause()
		}

		return hoststate.ERROR, retryErr
	}

	return lastState, nil
}

// GetHostState returns the current state of host identified by id
// hostParam can be a string or an instance of *abstract.Host; any other type will return an fail.InvalidParameterError
func (s *Stack) GetHostState(hostParam interface{}) (hoststate.Enum, fail.Error) {
	defer debug.NewTracer(nil, "", false).WithStopwatch().GoingIn().OnExitTrace()()

	hostState, err := s.getHostState(hostParam, temporal.GetDefaultDelay())

	return hostState, err
}

// ListHosts lists all hosts
func (s *Stack) ListHosts() ([]*abstract.Host, fail.Error) {
	defer debug.NewTracer(nil, "", true).WithStopwatch().GoingIn().OnExitTrace()()

	pager := servers.List(s.ComputeClient, servers.ListOpts{})
	var hosts []*abstract.Host
	err := pager.EachPage(
		func(page pagination.Page) (bool, fail.Error) {
			list, err := servers.ExtractServers(page)
			if err != nil {
				return false, err
			}

			for _, srv := range list {
				h := abstract.NewHost()
				err := s.complementHost(h, &srv)
				if err != nil {
					return false, err
				}
				hosts = append(hosts, h)
			}
			return true, nil
		},
	)
	if len(hosts) == 0 || err != nil {
		if err != nil {
			return nil, fail.Wrap(err, fmt.Sprintf("error listing hosts : %s", ProviderErrorToString(err)))
		}
		logrus.Warnf("Hosts lists empty !")
	}
	return hosts, nil
}

// DeleteHost deletes the host identified by id
func (s *Stack) DeleteHost(id string) error {
	defer debug.NewTracer(nil, fmt.Sprintf("(%s", id), true).WithStopwatch().GoingIn().OnExitTrace()()

	// Delete floating IP address if there is one
	if s.cfgOpts.UseFloatingIP {
		fip, err := s.getFloatingIP(id)
		if err != nil {
			switch err.(type) {
			case fail.ErrNotFound:
				// Continue
			default:
				return fail.Wrap(err, fmt.Sprintf("error retrieving floating ip for '%s'", id))
			}
		} else if fip != nil {
			err = floatingips.DisassociateInstance(
				s.ComputeClient, id, floatingips.DisassociateOpts{FloatingIP: fip.IP},
			).ExtractErr()
			if err != nil {
				return fail.Wrap(err, fmt.Sprintf("error deleting host '%s' : %s", id, ProviderErrorToString(err)))
			}
			err = floatingips.Delete(s.ComputeClient, fip.ID).ExtractErr()
			if err != nil {
				return fail.Wrap(err, fmt.Sprintf("error deleting host '%s' : %s", id, ProviderErrorToString(err)))
			}
		}
	}

	// Try to remove host for 3 minutes
	outerRetryErr := retry.WhileUnsuccessful(
		func() error {
			resourcePresent := true
			// 1st, send delete host order
			err := servers.Delete(s.ComputeClient, id).ExtractErr()
			if err != nil {
				return ReinterpretGophercloudErrorCode(
					err, []int64{404}, []int64{408, 429, 500, 503}, []int64{409}, func(ferr error) error {
						return fail.Errorf(
							fmt.Sprintf(
								"failed to submit host '%s' deletion: %s", id, ProviderErrorToString(ferr),
							), ferr,
						)
					},
				)
			}

			// 2nd, check host status every 5 seconds until check failed.
			// If check succeeds but state is Error, retry the deletion.
			// If check fails and error isn't 'resource not found', retry
			innerRetryErr := retry.WhileUnsuccessfulDelay5Seconds(
				func() error {
					host, err := servers.Get(s.ComputeClient, id).Extract()
					if err == nil {
						if toHostState(host.Status) == hoststate.ERROR {
							return nil
						}
						return fail.Errorf(fmt.Sprintf("host '%s' state is '%s'", host.Name, host.Status), err)
					}

					rei := ReinterpretGophercloudErrorCode(
						err, []int64{404}, []int64{408, 429, 500, 503}, []int64{409}, func(ferr error) error {
							return fail.Errorf(
								fmt.Sprintf(
									"failed to submit host '%s' deletion: %s", id, ProviderErrorToString(ferr),
								), ferr,
							)
						},
					)
					if rei == nil {
						resourcePresent = false
					}
					return rei
				},
				temporal.GetContextTimeout(),
			)
			if innerRetryErr != nil {
				if _, ok := innerRetryErr.(retry.ErrTimeout); ok {
					// retry deletion...
					return abstract.TimeoutError(
						fmt.Sprintf(
							"failed to acknowledge host '%s' deletion! %s", id, innerRetryErr.Error(),
						), temporal.GetContextTimeout(),
					)
				}

				return innerRetryErr
			}
			if !resourcePresent {
				logrus.Debugf("Host '%s' not found, deletion considered successful after a few retries", id)
				return nil
			}
			return fail.Errorf(fmt.Sprintf("host '%s' in state 'ERROR', retrying to delete", id), err)
		},
		0,
		temporal.GetHostCleanupTimeout(),
	)
	if outerRetryErr != nil {
		return fail.Wrap(outerRetryErr, "error deleting host: retry error")
	}
	return nil
}

// StopHost stops the host identified by id
func (s *Stack) StopHost(id string) error {
	defer debug.NewTracer(nil, fmt.Sprintf("(%s)", id), true).WithStopwatch().GoingIn().OnExitTrace()()

	err := startstop.Stop(s.ComputeClient, id).ExtractErr()
	if err != nil {
		return fail.Wrap(err, fmt.Sprintf("error stopping host : %s", ProviderErrorToString(err)))
	}
	return nil
}

// RebootHost reboots unconditionally the host identified by id
func (s *Stack) RebootHost(id string) error {
	defer debug.NewTracer(nil, fmt.Sprintf("(%s)", id), true).WithStopwatch().GoingIn().OnExitTrace()()

	// Try first a soft reboot, and if it fails (because host isn't in ACTIVE state), tries a hard reboot
	err := servers.Reboot(s.ComputeClient, id, servers.RebootOpts{Type: servers.SoftReboot}).ExtractErr()
	if err != nil {
		err = servers.Reboot(s.ComputeClient, id, servers.RebootOpts{Type: servers.HardReboot}).ExtractErr()
	}
	if err != nil {
		ftErr := fmt.Sprintf("error rebooting host [%s]: %s", id, ProviderErrorToString(err))
		return fail.Wrap(err, ftErr)
	}
	return nil
}

// StartHost starts the host identified by id
func (s *Stack) StartHost(id string) error {
	defer debug.NewTracer(nil, fmt.Sprintf("(%s)", id), true).WithStopwatch().GoingIn().OnExitTrace()()

	err := startstop.Start(s.ComputeClient, id).ExtractErr()
	if err != nil {
		return fail.Wrap(err, fmt.Sprintf("error starting host : %s", ProviderErrorToString(err)))
	}

	return nil
}

// ResizeHost ...
func (s *Stack) ResizeHost(id string, request abstract.SizingRequirements) (*abstract.Host, fail.Error) {
	defer debug.NewTracer(nil, fmt.Sprintf("(%s)", id), true).WithStopwatch().GoingIn().OnExitTrace()()

	// TODO: RESIZE Resize Host HERE
	logrus.Warn("Trying to resize a Host...")

	// TODO: RESIZE Call this
	// servers.Resize()

	return nil, fail.NotImplementedError("ResizeHost() not implemented yet") // FIXME: Technical debt
}
