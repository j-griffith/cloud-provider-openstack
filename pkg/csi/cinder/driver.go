/*
Copyright 2017 The Kubernetes Authors.

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

package cinder

import (
	"github.com/container-storage-interface/spec/lib/go/csi/v0"
	"github.com/golang/glog"

	"github.com/kubernetes-csi/drivers/pkg/csi-common"
	"k8s.io/cloud-provider-openstack/pkg/csi/cinder/openstack"
)

type driver struct {
	csiDriver   *csicommon.CSIDriver
	endpoint    string
	cloudconfig string

	ids *csicommon.DefaultIdentityServer
	cs  *controllerServer
	ns  *nodeServer

	cap   []*csi.VolumeCapability_AccessMode
	cscap []*csi.ControllerServiceCapability
}

const (
	driverName = "csi-cinderplugin"
)

var (
	version = "0.2.0"
)

// NewDriver initializes a new csi-cinder driver
func NewDriver(nodeID, endpoint string, cloudconfig string) *driver {
	glog.Infof("Driver: %v version: %v", driverName, version)

	d := &driver{}

	d.endpoint = endpoint
	d.cloudconfig = cloudconfig

	csiDriver := csicommon.NewCSIDriver(driverName, version, nodeID)
	csiDriver.AddControllerServiceCapabilities(
		[]csi.ControllerServiceCapability_RPC_Type{
			csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
			csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
		})
	csiDriver.AddVolumeCapabilityAccessModes([]csi.VolumeCapability_AccessMode_Mode{csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER})

	d.csiDriver = csiDriver

	return d
}

// NewControllerServer initializes a new csi-controller-server
func NewControllerServer(d *driver) *controllerServer {
	return &controllerServer{
		DefaultControllerServer: csicommon.NewDefaultControllerServer(d.csiDriver),
	}
}

// NewNodeServer initializes a new csi-node-server
func NewNodeServer(d *driver) *nodeServer {
	return &nodeServer{
		DefaultNodeServer: csicommon.NewDefaultNodeServer(d.csiDriver),
	}
}

// Run starts up the Node and Controller grpc servers
func (d *driver) Run() {
	openstack.InitOpenStackProvider(d.cloudconfig)
	csicommon.RunControllerandNodePublishServer(d.endpoint, d.csiDriver, NewControllerServer(d), NewNodeServer(d))
}
