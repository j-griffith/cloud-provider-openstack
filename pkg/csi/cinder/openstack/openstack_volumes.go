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

package openstack

import (
	"fmt"
	"time"

	"github.com/gophercloud/gophercloud/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/volumeattach"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/golang/glog"
)

const (
	VolumeAvailableStatus    = "available"
	VolumeInUseStatus        = "in-use"
	VolumeDeletedStatus      = "deleted"
	VolumeErrorStatus        = "error"
	operationFinishInitDelay = 1 * time.Second
	operationFinishFactor    = 1.1
	operationFinishSteps     = 10
	diskAttachInitDelay      = 1 * time.Second
	diskAttachFactor         = 1.2
	diskAttachSteps          = 15
	diskDetachInitDelay      = 1 * time.Second
	diskDetachFactor         = 1.2
	diskDetachSteps          = 13
)

// CreateVolume creates a volume of given size
func (os *OpenStack) CreateVolume(name string, size int, vtype, availability string, tags *map[string]string) (*volumes.Volume, error) {
	opts := &volumes.CreateOpts{
		Name:             name,
		Size:             size,
		VolumeType:       vtype,
		AvailabilityZone: availability,
	}
	if tags != nil {
		opts.Metadata = *tags
	}

	vol, err := volumes.Create(os.blockstorage, opts).Extract()
	if err != nil {
		return nil, err
	}

	return vol, nil
}

// GetVolumesByName is a wrapper around ListVolumes that creates a Name filter to act as a GetByName
// Returns a list of Volume references with the specified name
func (os *OpenStack) GetVolumesByName(n string) ([]volumes.Volume, error) {
	opts := volumes.ListOpts{Name: n}
	pages, err := volumes.List(os.blockstorage, opts).AllPages()
	if err != nil {
		return nil, err
	}

	vols, err := volumes.ExtractVolumes(pages)
	if err != nil {
		return vols, err
	}
	return vols, nil
}

// DeleteVolume delete a volume
func (os *OpenStack) DeleteVolume(volumeID string) error {
	used, err := os.diskIsUsed(volumeID)
	if err != nil {
		return err
	}
	if used {
		return fmt.Errorf("Cannot delete the volume %q, it's still attached to a node", volumeID)
	}

	err = volumes.Delete(os.blockstorage, volumeID).ExtractErr()
	return err
}

// GetVolume retrieves Volume by its ID.
func (os *OpenStack) GetVolume(volumeID string) (*volumes.Volume, error) {

	vol, err := volumes.Get(os.blockstorage, volumeID).Extract()
	if err != nil {
		return nil, err
	}
	return vol, nil
}

// AttachVolume attaches given cinder volume to the compute
func (os *OpenStack) AttachVolume(instanceID, volumeID string) (string, error) {
	volume, err := os.GetVolume(volumeID)
	if err != nil {
		return "", err
	}

	if len(volume.Attachments) > 0 {
		if isAttachedToServerID(volume.Attachments, instanceID) {
			glog.V(4).Infof("Disk %s is already attached to instance %s", volumeID, instanceID)
			return volume.ID, nil
		}
		// FIXME(JG): This will need to be updated for multiattach
		// note the caller can inspect volume.attachments to get list of attached instances if desired
		return "", fmt.Errorf("disk %s is attached to a different instance", volumeID)
	}

	_, err = volumeattach.Create(os.compute, instanceID, &volumeattach.CreateOpts{
		VolumeID: volume.ID,
	}).Extract()

	if err != nil {
		return "", fmt.Errorf("failed to attach %s volume to %s compute: %v", volumeID, instanceID, err)
	}
	glog.V(2).Infof("Successfully attached %s volume to %s compute", volumeID, instanceID)
	return volume.ID, nil
}

// WaitDiskAttached waits for attched
func (os *OpenStack) WaitDiskAttached(instanceID, volumeID string) error {
	backoff := wait.Backoff{
		Duration: diskAttachInitDelay,
		Factor:   diskAttachFactor,
		Steps:    diskAttachSteps,
	}

	err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		volume, err := os.GetVolume(volumeID)
		// We'll need to deal with multi-attach here, but until we figure out a clean
		// way to add the Micro Versions in gophercloud we'll pretend it doesn't exist
		if err != nil {
			return false, err
		}
		// Is attached isn't good enough, is it attached to the specified Instance is the real question
		if len(volume.Attachments) > 0 {
			for _, a := range volume.Attachments {
				if a.ServerID == instanceID {
					return true, nil
				}
			}

		}
		return false, nil
	})

	if err == wait.ErrWaitTimeout {
		err = fmt.Errorf("Volume %q failed to be attached within the alloted time", volumeID)
	}

	return err
}

// DetachVolume detaches given cinder volume from the compute
func (os *OpenStack) DetachVolume(instanceID, volumeID string) error {
	volume, err := os.GetVolume(volumeID)
	if err != nil {
		return err
	}
	if volume.Status == VolumeAvailableStatus {
		glog.V(2).Infof("volume: %s has been detached from compute: %s ", volume.ID, instanceID)
		return nil
	}

	if volume.Status != VolumeInUseStatus {
		return fmt.Errorf("can not detach volume %s, its status is %s", volume.Name, volume.Status)
	}

	if !isAttachedToServerID(volume.Attachments, instanceID) {
		return fmt.Errorf("disk: %s has no attachments or is not attached to compute: %s", volume.Name, instanceID)
	} else {
		err = volumeattach.Delete(os.compute, instanceID, volume.ID).ExtractErr()
		if err != nil {
			return fmt.Errorf("failed to delete volume %s from compute %s attached %v", volume.ID, instanceID, err)
		}
		glog.V(2).Infof("Successfully detached volume: %s from compute: %s", volume.ID, instanceID)
	}

	return nil
}

// WaitDiskDetached waits for detached
func (os *OpenStack) WaitDiskDetached(instanceID string, volumeID string) error {
	backoff := wait.Backoff{
		Duration: diskDetachInitDelay,
		Factor:   diskDetachFactor,
		Steps:    diskDetachSteps,
	}

	err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		attached, err := os.diskIsAttached(instanceID, volumeID)
		if err != nil {
			return false, err
		}
		return !attached, nil
	})

	if err == wait.ErrWaitTimeout {
		err = fmt.Errorf("Volume %q failed to detach within the alloted time", volumeID)
	}

	return err
}

// GetAttachmentDiskPath gets device path of attached volume to the compute
func (os *OpenStack) GetAttachmentDiskPath(instanceID, volumeID string) (string, error) {
	volume, err := os.GetVolume(volumeID)
	if err != nil {
		return "", err
	}
	if volume.Status != VolumeInUseStatus {
		return "", fmt.Errorf("can not get device path of volume %s, its status is %s ", volume.Name, volume.Status)
	}
	if !isAttachedToServerID(volume.Attachments, instanceID) {

	if volume.AttachedServerId != "" {
		if instanceID == volume.AttachedServerId {
			return volume.AttachedDevice, nil
		} else {
			return "", fmt.Errorf("disk %q is attached to a different compute: %q, should be detached before proceeding", volumeID, volume.AttachedServerId)
		}
	}
	return "", fmt.Errorf("volume %s has no ServerId", volumeID)
}

// diskIsAttached queries if a volume is attached to a compute instance
// Deprecated: Inspect volume.Attachments instead.
func (os *OpenStack) diskIsAttached(instanceID, volumeID string) (bool, error) {
	volume, err := os.GetVolume(volumeID)
	if err != nil {
		return false, err
	}
	if len(volume.Attachments) > 0 {
		return true, nil
	}
	return false, nil
}

// diskIsUsed returns true a disk is attached to any node.
func (os *OpenStack) diskIsUsed(volumeID string) (bool, error) {
	volume, err := os.GetVolume(volumeID)
	if err != nil {
		return false, err
	}
	return volume.AttachedServerId != "", nil
}

func isAttachedToServerID(attachments volumes.Attachments, serverID string) bool {
	for _, a := range attachments {
		if a.ServerID == serverID {
			return true
		}
	}
	return false
}

// getFilteredAttachmentIndexes provides a function to search a list of attachments for a
// filter and returns an list of indexes for cooresponding entries that match the filter.
// for example to find all attachments attached to ServerID:
//     `getFilteredAttachmentIndexes(attachments, "ServerID"="myserverID")`
func getFilteredAttachmentIndexes(attachments volumes.Attachments, filter map[string]string) ([]int, error) {
}
