/*
Copyright © 2022 - 2024 SUSE LLC

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

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/rancher/elemental-toolkit/v2/pkg/constants"
)

const (
	GPT   = "gpt"
	BIOS  = "bios"
	MSDOS = "msdos"
	EFI   = "efi"
	ESP   = "esp"
	bios  = "bios_grub"
	boot  = "boot"
)

// Config is the struct that includes basic and generic configuration of elemental binary runtime.
// It mostly includes the interfaces used around many methods in elemental code
type Config struct {
	Logger                    Logger
	Fs                        FS
	Mounter                   Mounter
	Runner                    Runner
	Syscall                   SyscallInterface
	CloudInitRunner           CloudInitRunner
	ImageExtractor            ImageExtractor
	Client                    HTTPClient
	Platform                  *Platform `yaml:"platform,omitempty" mapstructure:"platform"`
	Cosign                    bool      `yaml:"cosign,omitempty" mapstructure:"cosign"`
	Verify                    bool      `yaml:"verify,omitempty" mapstructure:"verify"`
	TLSVerify                 bool      `yaml:"tls-verify,omitempty" mapstructure:"tls-verify"`
	CosignPubKey              string    `yaml:"cosign-key,omitempty" mapstructure:"cosign-key"`
	LocalImage                bool      `yaml:"local,omitempty" mapstructure:"local"`
	Arch                      string    `yaml:"arch,omitempty" mapstructure:"arch"`
	SquashFsCompressionConfig []string  `yaml:"squash-compression,omitempty" mapstructure:"squash-compression"`
	SquashFsNoCompression     bool      `yaml:"squash-no-compression,omitempty" mapstructure:"squash-no-compression"`
	CloudInitPaths            []string  `yaml:"cloud-init-paths,omitempty" mapstructure:"cloud-init-paths"`
	Strict                    bool      `yaml:"strict,omitempty" mapstructure:"strict"`
}

// WriteInstallState writes the state.yaml file to the given state and recovery paths
func (c Config) WriteInstallState(i *InstallState, statePath, recoveryPath string) error {
	data, err := yaml.Marshal(i)
	if err != nil {
		c.Logger.Errorf("failed marshalling state file: %v", err)
		return err
	}

	data = append([]byte("# Autogenerated file by elemental client, do not edit\n\n"), data...)

	if statePath != "" {
		err = c.Fs.WriteFile(statePath, data, constants.FilePerm)
		if err != nil {
			c.Logger.Errorf("failed state file in state partition: %v", err)
			return err
		}
	}

	if recoveryPath != "" {
		err = c.Fs.WriteFile(recoveryPath, data, constants.FilePerm)
		if err != nil {
			c.Logger.Errorf("failed state file in recovery partition: %v", err)
			return err
		}
	}

	return nil
}

// LoadInstallState loads the state.yaml file and unmarshals it to an InstallState object
func (c Config) LoadInstallState() (*InstallState, error) {
	installState := &InstallState{
		Snapshotter: NewLoopDevice(),
	}
	stateFile := filepath.Join(constants.RunningStateDir, constants.InstallStateFile)
	data, err := c.Fs.ReadFile(stateFile)
	if err != nil {
		c.Logger.Warnf("Could not read state file %s", stateFile)
		stateFile = filepath.Join(constants.LegacyStateDir, constants.InstallStateFile)
		c.Logger.Debugf("Attempting to read state file %s", stateFile)
		data, err = c.Fs.ReadFile(stateFile)
		if err != nil {
			return nil, err
		}
	}
	err = yaml.Unmarshal(data, installState)
	if err != nil {
		return nil, err
	}

	// Set default filesystem labels if missing, see rancher/elemental-toolkit#1827
	if installState.Partitions[constants.BootPartName] != nil && installState.Partitions[constants.BootPartName].FSLabel == "" {
		installState.Partitions[constants.BootPartName].FSLabel = constants.BootLabel
	}
	if installState.Partitions[constants.OEMPartName] != nil && installState.Partitions[constants.OEMPartName].FSLabel == "" {
		installState.Partitions[constants.OEMPartName].FSLabel = constants.OEMLabel
	}
	if installState.Partitions[constants.RecoveryPartName] != nil && installState.Partitions[constants.RecoveryPartName].FSLabel == "" {
		installState.Partitions[constants.RecoveryPartName].FSLabel = constants.RecoveryLabel
		recovery := installState.Partitions[constants.RecoveryPartName]
		if recovery.RecoveryImage.FS == "" {
			recovery.RecoveryImage.FS = constants.SquashFs
		}
		if recovery.RecoveryImage.Label == "" && recovery.RecoveryImage.FS != constants.SquashFs {
			recovery.RecoveryImage.Label = constants.SystemLabel
		}
	}
	if installState.Partitions[constants.StatePartName] != nil && installState.Partitions[constants.StatePartName].FSLabel == "" {
		installState.Partitions[constants.StatePartName].FSLabel = constants.StateLabel
	}
	if installState.Partitions[constants.PersistentPartName] != nil && installState.Partitions[constants.PersistentPartName].FSLabel == "" {
		installState.Partitions[constants.PersistentPartName].FSLabel = constants.PersistentLabel
	}

	return installState, nil
}

// Sanitize checks the consistency of the struct, returns error
// if unsolvable inconsistencies are found
func (c *Config) Sanitize() error {
	// If no squashcompression is set, zero the compression parameters
	// By default on NewConfig the SquashFsCompressionConfig is set to the default values, and then override
	// on config unmarshall.
	if c.SquashFsNoCompression {
		c.SquashFsCompressionConfig = constants.GetSquashfsNoCompressionOptions()
	}

	if c.Arch != "" {
		p, err := NewPlatformFromArch(c.Arch)
		if err != nil {
			return err
		}
		c.Platform = p
	}

	if c.Platform == nil {
		p, err := NewPlatformFromArch(runtime.GOARCH)
		if err != nil {
			return err
		}
		c.Platform = p
	}

	return nil
}

type RunConfig struct {
	Reboot      bool              `yaml:"reboot,omitempty" mapstructure:"reboot"`
	PowerOff    bool              `yaml:"poweroff,omitempty" mapstructure:"poweroff"`
	EjectCD     bool              `yaml:"eject-cd,omitempty" mapstructure:"eject-cd"`
	Snapshotter SnapshotterConfig `yaml:"snapshotter,omitempty" mapstructure:"snapshotter"`

	// 'inline' and 'squash' labels ensure config fields
	// are embedded from a yaml and map PoV
	Config `yaml:",inline" mapstructure:",squash"`
}

// Sanitize checks the consistency of the struct, returns error
// if unsolvable inconsistencies are found
func (r *RunConfig) Sanitize() error {
	// Always include default cloud-init paths
	r.CloudInitPaths = append(constants.GetCloudInitPaths(), r.CloudInitPaths...)
	return r.Config.Sanitize()
}

// InstallSpec struct represents all the installation action details
type InstallSpec struct {
	Target           string `yaml:"target,omitempty" mapstructure:"target"`
	Firmware         string
	PartTable        string
	Partitions       ElementalPartitions `yaml:"partitions,omitempty" mapstructure:"partitions"`
	ExtraPartitions  PartitionList       `yaml:"extra-partitions,omitempty" mapstructure:"extra-partitions"`
	NoFormat         bool                `yaml:"no-format,omitempty" mapstructure:"no-format"`
	Force            bool                `yaml:"force,omitempty" mapstructure:"force"`
	CloudInit        []string            `yaml:"cloud-init,omitempty" mapstructure:"cloud-init"`
	Iso              string              `yaml:"iso,omitempty" mapstructure:"iso"`
	GrubDefEntry     string              `yaml:"grub-entry-name,omitempty" mapstructure:"grub-entry-name"`
	System           *ImageSource        `yaml:"system,omitempty" mapstructure:"system"`
	RecoverySystem   Image               `yaml:"recovery-system,omitempty" mapstructure:"recovery-system"`
	DisableBootEntry bool                `yaml:"disable-boot-entry,omitempty" mapstructure:"disable-boot-entry"`
	SnapshotLabels   map[string]string   `yaml:"snapshot-labels,omitempty" mapstructure:"snapshot-labels"`
}

// Sanitize checks the consistency of the struct, returns error
// if unsolvable inconsistencies are found
func (i *InstallSpec) Sanitize() error {
	if i.System.IsEmpty() && i.Iso == "" {
		return fmt.Errorf("undefined system source to install")
	}
	if i.Partitions.State == nil || i.Partitions.State.MountPoint == "" {
		return fmt.Errorf("undefined state partition")
	}

	// If not special recovery is defined use main system source
	if i.RecoverySystem.Source.IsEmpty() {
		i.RecoverySystem.Source = i.System
	}

	// Set default label for non squashfs images
	if i.RecoverySystem.FS != constants.SquashFs && i.RecoverySystem.Label == "" {
		i.RecoverySystem.Label = constants.SystemLabel
	} else if i.RecoverySystem.FS == constants.SquashFs {
		i.RecoverySystem.Label = ""
	}

	// Check for extra partitions having set its size to 0
	extraPartsSizeCheck := 0
	for _, p := range i.ExtraPartitions {
		if p.Size == 0 {
			extraPartsSizeCheck++
		}
	}

	if extraPartsSizeCheck > 1 {
		return fmt.Errorf("more than one extra partition has its size set to 0. Only one partition can have its size set to 0 which means that it will take all the available disk space in the device")
	}
	// Check for both an extra partition and the persistent partition having size set to 0
	if extraPartsSizeCheck == 1 && i.Partitions.Persistent.Size == 0 {
		return fmt.Errorf("both persistent partition and extra partitions have size set to 0. Only one partition can have its size set to 0 which means that it will take all the available disk space in the device")
	}
	return i.Partitions.SetFirmwarePartitions(i.Firmware, i.PartTable)
}

// InitSpec struct represents all the init action details
type InitSpec struct {
	Mkinitrd bool `yaml:"mkinitrd,omitempty" mapstructure:"mkinitrd"`
	Force    bool `yaml:"force,omitempty" mapstructure:"force"`

	Features []string `yaml:"features,omitempty" mapstructure:"features"`
}

// MountSpec struct represents all the mount action details
type MountSpec struct {
	WriteFstab     bool             `yaml:"write-fstab,omitempty" mapstructure:"write-fstab"`
	Disable        bool             `yaml:"disable,omitempty" mapstructure:"disable"`
	Sysroot        string           `yaml:"sysroot,omitempty" mapstructure:"sysroot"`
	Mode           string           `yaml:"mode,omitempty" mapstructure:"mode"`
	SelinuxRelabel bool             `yaml:"selinux-relabel,omitempty" mapstructure:"selinux-relabel"`
	Volumes        []*VolumeMount   `yaml:"extra-volumes,omitempty" mapstructure:"extra-volumes"`
	Ephemeral      EphemeralMounts  `yaml:"ephemeral,omitempty" mapstructure:"ephemeral"`
	Persistent     PersistentMounts `yaml:"persistent,omitempty" mapstructure:"persistent"`
}

type VolumeMount struct {
	Mountpoint string   `yaml:"mountpoint,omitempty" mapstructure:"mountpoint"`
	Device     string   `yaml:"device,omitempty" mapstructure:"device"`
	Options    []string `yaml:"options,omitempty" mapstructure:"options"`
	FSType     string   `yaml:"fs,omitempty" mapstructure:"fs"`
}

// PersistentMounts struct contains settings for which paths to mount as
// persistent
type PersistentMounts struct {
	Mode   string      `yaml:"mode,omitempty" mapstructure:"mode"`
	Paths  []string    `yaml:"paths,omitempty" mapstructure:"paths"`
	Volume VolumeMount `yaml:"volume,omitempty" mapstructure:"volume"`
}

// EphemeralMounts contains information about the RW overlay mounted over the
// immutable system.
type EphemeralMounts struct {
	Type   string   `yaml:"type,omitempty" mapstructure:"type"`
	Device string   `yaml:"device,omitempty" mapstructure:"device"`
	Size   string   `yaml:"size,omitempty" mapstructure:"size"`
	Paths  []string `yaml:"paths,omitempty" mapstructure:"paths"`
}

// Sanitize checks the consistency of the struct, returns error
// if unsolvable inconsistencies are found
func (spec *MountSpec) Sanitize() error {
	switch spec.Persistent.Mode {
	case constants.BindMode, constants.OverlayMode:
		break
	default:
		return fmt.Errorf("unknown persistent mode: '%s'", spec.Persistent.Mode)
	}

	const separator = string(os.PathSeparator)

	if spec.Persistent.Paths != nil {
		// Remove empty paths
		spec.Persistent.Paths = slices.DeleteFunc(spec.Persistent.Paths, func(s string) bool {
			return s == ""
		})

		sort.Slice(spec.Persistent.Paths, func(i, j int) bool {
			return strings.Count(spec.Persistent.Paths[i], separator) < strings.Count(spec.Persistent.Paths[j], separator)
		})
	}

	switch spec.Ephemeral.Type {
	case constants.Tmpfs, constants.Block:
		break
	default:
		return fmt.Errorf("unknown overlay type: '%s'", spec.Ephemeral.Type)
	}

	if spec.Ephemeral.Paths != nil {
		// Remove empty paths
		spec.Ephemeral.Paths = slices.DeleteFunc(spec.Ephemeral.Paths, func(s string) bool {
			return s == ""
		})

		sort.Slice(spec.Ephemeral.Paths, func(i, j int) bool {
			return strings.Count(spec.Ephemeral.Paths[i], separator) < strings.Count(spec.Ephemeral.Paths[j], separator)
		})
	}

	return nil
}

func (spec *MountSpec) HasPersistent() bool {
	return spec.Mode != constants.RecoveryImgName &&
		spec.Persistent.Volume.Device != "" && spec.Persistent.Volume.Mountpoint != ""
}

// ResetSpec struct represents all the reset action details
type ResetSpec struct {
	FormatPersistent bool `yaml:"reset-persistent,omitempty" mapstructure:"reset-persistent"`
	FormatOEM        bool `yaml:"reset-oem,omitempty" mapstructure:"reset-oem"`

	CloudInit        []string     `yaml:"cloud-init,omitempty" mapstructure:"cloud-init"`
	GrubDefEntry     string       `yaml:"grub-entry-name,omitempty" mapstructure:"grub-entry-name"`
	System           *ImageSource `yaml:"system,omitempty" mapstructure:"system"`
	Partitions       ElementalPartitions
	Target           string
	Efi              bool
	State            *InstallState
	DisableBootEntry bool              `yaml:"disable-boot-entry,omitempty" mapstructure:"disable-boot-entry"`
	SnapshotLabels   map[string]string `yaml:"snapshot-labels,omitempty" mapstructure:"snapshot-labels"`
}

// Sanitize checks the consistency of the struct, returns error
// if unsolvable inconsistencies are found
func (r *ResetSpec) Sanitize() error {
	if r.System.IsEmpty() {
		return fmt.Errorf("undefined system source to reset to")
	}
	if r.Partitions.State == nil || r.Partitions.State.MountPoint == "" {
		return fmt.Errorf("undefined state partition")
	}

	return nil
}

type UpgradeSpec struct {
	RecoveryUpgrade   bool              `yaml:"recovery,omitempty" mapstructure:"recovery"`
	System            *ImageSource      `yaml:"system,omitempty" mapstructure:"system"`
	RecoverySystem    Image             `yaml:"recovery-system,omitempty" mapstructure:"recovery-system"`
	GrubDefEntry      string            `yaml:"grub-entry-name,omitempty" mapstructure:"grub-entry-name"`
	BootloaderUpgrade bool              `yaml:"bootloader,omitempty" mapstructure:"bootloader"`
	SnapshotLabels    map[string]string `yaml:"snapshot-labels,omitempty" mapstructure:"snapshot-labels"`
	Partitions        ElementalPartitions
	State             *InstallState
}

// Sanitize checks the consistency of the struct, returns error
// if unsolvable inconsistencies are found
func (u *UpgradeSpec) Sanitize() error {
	if u.Partitions.State == nil || u.Partitions.State.MountPoint == "" {
		return fmt.Errorf("undefined state partition")
	}
	if u.System.IsEmpty() {
		return fmt.Errorf("undefined upgrade source")
	}

	if u.RecoveryUpgrade {
		if u.Partitions.Recovery == nil || u.Partitions.Recovery.MountPoint == "" {
			return fmt.Errorf("undefined recovery partition")
		}
		if u.RecoverySystem.Source.IsEmpty() {
			u.RecoverySystem.Source = u.System
		}
	}

	if u.BootloaderUpgrade {
		if u.Partitions.Boot == nil || u.Partitions.Boot.MountPoint == "" {
			return fmt.Errorf("undefined Bootloader partition")
		}
	}

	// Set default label for non squashfs images
	if u.RecoverySystem.FS != constants.SquashFs && u.RecoverySystem.Label == "" {
		u.RecoverySystem.Label = constants.SystemLabel
	} else if u.RecoverySystem.FS == constants.SquashFs {
		u.RecoverySystem.Label = ""
	}

	return nil
}

// SanitizeForRecoveryOnly sanitizes UpgradeSpec when upgrading recovery only.
func (u *UpgradeSpec) SanitizeForRecoveryOnly() error {
	if u.Partitions.State == nil || u.Partitions.State.MountPoint == "" {
		return fmt.Errorf("undefined state partition")
	}

	if u.Partitions.Recovery == nil || u.Partitions.Recovery.MountPoint == "" {
		return fmt.Errorf("undefined recovery partition")
	}
	if u.RecoverySystem.Source.IsEmpty() {
		return fmt.Errorf("undefined upgrade-recovery source")
	}

	// Set default label for non squashfs images
	if u.RecoverySystem.FS != constants.SquashFs && u.RecoverySystem.Label == "" {
		u.RecoverySystem.Label = constants.SystemLabel
	} else if u.RecoverySystem.FS == constants.SquashFs {
		u.RecoverySystem.Label = ""
	}

	return nil
}

// Partition struct represents a partition with its commonly configurable values, size in MiB
type Partition struct {
	Name            string
	FilesystemLabel string   `yaml:"label,omitempty" mapstructure:"label"`
	Size            uint     `yaml:"size,omitempty" mapstructure:"size"`
	FS              string   `yaml:"fs,omitempty" mapstructure:"fs"`
	Flags           []string `yaml:"flags,omitempty" mapstructure:"flags"`
	MountPoint      string
	Path            string
	Disk            string
}

type PartitionList []*Partition

// ToImage returns an image object that matches the partition. This is helpful if the partition
// is managed as an image.
func (p Partition) ToImage() *Image {
	return &Image{
		File:       p.Path,
		Label:      p.FilesystemLabel,
		Size:       p.Size,
		FS:         p.FS,
		Source:     NewEmptySrc(),
		MountPoint: p.MountPoint,
	}
}

// GetByName gets a partitions by its name from the PartitionList
func (pl PartitionList) GetByName(name string) *Partition {
	var part *Partition

	for _, p := range pl {
		if p.Name == name {
			part = p
			if part.MountPoint != "" {
				return part
			}
		}
	}
	return part
}

// GetByLabel gets a partition by its label from the PartitionList
func (pl PartitionList) GetByLabel(label string) *Partition {
	var part *Partition

	for _, p := range pl {
		if p.FilesystemLabel == label {
			part = p
			if part.MountPoint != "" {
				return part
			}
		}
	}
	return part
}

// GetByNameOrLabel gets a partition by its name or label. It tries by name first
func (pl PartitionList) GetByNameOrLabel(name, label string) *Partition {
	part := pl.GetByName(name)
	if part == nil {
		part = pl.GetByLabel(label)
	}
	return part
}

type ElementalPartitions struct {
	BIOS       *Partition
	Boot       *Partition `yaml:"bootloader,omitempty" mapstructure:"bootloader"`
	OEM        *Partition `yaml:"oem,omitempty" mapstructure:"oem"`
	Recovery   *Partition `yaml:"recovery,omitempty" mapstructure:"recovery"`
	State      *Partition `yaml:"state,omitempty" mapstructure:"state"`
	Persistent *Partition `yaml:"persistent,omitempty" mapstructure:"persistent"`
}

// GetConfigStorage returns the path, usually a mountpoint, of the configuration partition
func (ep ElementalPartitions) GetConfigStorage() string {
	if ep.OEM != nil {
		return ep.OEM.MountPoint
	}
	return ""
}

// SetFirmwarePartitions sets firmware partitions for a given firmware and partition table type
func (ep *ElementalPartitions) SetFirmwarePartitions(firmware string, partTable string) error {
	if firmware == EFI && partTable == GPT {
		if ep.Boot == nil {
			return fmt.Errorf("nil efi partition")
		}
		ep.BIOS = nil
	} else if firmware == BIOS && partTable == GPT {
		ep.BIOS = &Partition{
			FilesystemLabel: "",
			Size:            constants.BiosSize,
			Name:            constants.BiosPartName,
			FS:              "",
			MountPoint:      "",
			Flags:           []string{bios},
		}
		ep.Boot = nil
	} else {
		if ep.State == nil {
			return fmt.Errorf("nil state partition")
		}
		ep.State.Flags = []string{boot}
		ep.Boot = nil
		ep.BIOS = nil
	}
	return nil
}

// NewElementalPartitionsFromList fills an ElementalPartitions instance from given
// partitions list. First tries to match partitions by partition label, if not,
// it tries to match partitions by filesystem label
func NewElementalPartitionsFromList(pl PartitionList, state *InstallState) ElementalPartitions {
	ep := ElementalPartitions{}

	lm := map[string]string{
		constants.BootPartName:       constants.BootLabel,
		constants.OEMPartName:        constants.OEMLabel,
		constants.RecoveryPartName:   constants.RecoveryLabel,
		constants.StatePartName:      constants.StateLabel,
		constants.PersistentPartName: constants.PersistentLabel,
	}
	if state != nil {
		for k := range lm {
			if state.Partitions[k] != nil {
				lm[k] = state.Partitions[k].FSLabel
			}
		}
	}

	ep.BIOS = pl.GetByName(constants.BiosPartName)
	ep.Boot = pl.GetByNameOrLabel(constants.BootPartName, lm[constants.BootPartName])
	ep.OEM = pl.GetByNameOrLabel(constants.OEMPartName, lm[constants.OEMPartName])
	ep.Recovery = pl.GetByNameOrLabel(constants.RecoveryPartName, lm[constants.RecoveryPartName])
	ep.State = pl.GetByNameOrLabel(constants.StatePartName, lm[constants.StatePartName])
	ep.Persistent = pl.GetByNameOrLabel(constants.PersistentPartName, lm[constants.PersistentPartName])

	return ep
}

// PartitionsByInstallOrder sorts partitions according to the default layout
// nil partitons are ignored
// partition with 0 size is set last
func (ep ElementalPartitions) PartitionsByInstallOrder(extraPartitions PartitionList, excludes ...*Partition) PartitionList {
	partitions := PartitionList{}
	var lastPartition *Partition

	inExcludes := func(part *Partition, list ...*Partition) bool {
		for _, p := range list {
			if part == p {
				return true
			}
		}
		return false
	}

	if ep.BIOS != nil && !inExcludes(ep.BIOS, excludes...) {
		partitions = append(partitions, ep.BIOS)
	}
	if ep.Boot != nil && !inExcludes(ep.Boot, excludes...) {
		partitions = append(partitions, ep.Boot)
	}
	if ep.OEM != nil && !inExcludes(ep.OEM, excludes...) {
		partitions = append(partitions, ep.OEM)
	}
	if ep.Recovery != nil && !inExcludes(ep.Recovery, excludes...) {
		partitions = append(partitions, ep.Recovery)
	}
	if ep.State != nil && !inExcludes(ep.State, excludes...) {
		partitions = append(partitions, ep.State)
	}
	if ep.Persistent != nil && !inExcludes(ep.Persistent, excludes...) {
		// Check if we have to set this partition the latest due size == 0
		if ep.Persistent.Size == 0 {
			lastPartition = ep.Persistent
		} else {
			partitions = append(partitions, ep.Persistent)
		}
	}
	for _, p := range extraPartitions {
		// Check if we have to set this partition the latest due size == 0
		// Also check that we didn't set already the persistent to last in which case ignore this
		// InstallConfig.Sanitize should have already taken care of failing if this is the case, so this is extra protection
		if p.Size == 0 {
			if lastPartition != nil {
				// Ignore this part, we are not setting 2 parts to have 0 size!
				continue
			}
			lastPartition = p
		} else {
			partitions = append(partitions, p)
		}
	}

	// Set the last partition in the list the partition which has 0 size, so it grows to use the rest of free space
	if lastPartition != nil {
		partitions = append(partitions, lastPartition)
	}

	return partitions
}

// PartitionsByMountPoint sorts partitions according to its mountpoint, ignores nil
// partitions or partitions with an empty mountpoint
func (ep ElementalPartitions) PartitionsByMountPoint(descending bool, excludes ...*Partition) PartitionList {
	mountPointKeys := map[string]*Partition{}
	mountPoints := []string{}
	partitions := PartitionList{}

	for _, p := range ep.PartitionsByInstallOrder([]*Partition{}, excludes...) {
		if p.MountPoint != "" {
			mountPointKeys[p.MountPoint] = p
			mountPoints = append(mountPoints, p.MountPoint)
		}
	}

	if descending {
		sort.Sort(sort.Reverse(sort.StringSlice(mountPoints)))
	} else {
		sort.Strings(mountPoints)
	}

	for _, mnt := range mountPoints {
		partitions = append(partitions, mountPointKeys[mnt])
	}
	return partitions
}

// Image struct represents a file system image with its commonly configurable values, size in MiB
type Image struct {
	File       string
	Label      string       `yaml:"label,omitempty" mapstructure:"label"`
	Size       uint         `yaml:"size,omitempty" mapstructure:"size"`
	FS         string       `yaml:"fs,omitempty" mapstructure:"fs"`
	Source     *ImageSource `yaml:"uri,omitempty" mapstructure:"uri"`
	MountPoint string
	LoopDevice string
}

// LiveISO represents the configurations needed for a live ISO image
type LiveISO struct {
	RootFS             []*ImageSource `yaml:"rootfs,omitempty" mapstructure:"rootfs"`
	UEFI               []*ImageSource `yaml:"uefi,omitempty" mapstructure:"uefi"`
	Image              []*ImageSource `yaml:"image,omitempty" mapstructure:"image"`
	Label              string         `yaml:"label,omitempty" mapstructure:"label"`
	GrubEntry          string         `yaml:"grub-entry-name,omitempty" mapstructure:"grub-entry-name"`
	BootloaderInRootFs bool           `yaml:"bootloader-in-rootfs" mapstructure:"bootloader-in-rootfs"`
	Firmware           string         `yaml:"firmware,omitempty" mapstructure:"firmware"`
	ExtraCmdline       string         `yaml:"extra-cmdline,omitempty" mapstructure:"extra-cmdline"`
}

// Sanitize checks the consistency of the struct, returns error
// if unsolvable inconsistencies are found
func (i *LiveISO) Sanitize() error {
	for _, src := range i.RootFS {
		if src == nil {
			return fmt.Errorf("wrong name of source package for rootfs")
		}
	}
	for _, src := range i.UEFI {
		if src == nil {
			return fmt.Errorf("wrong name of source package for uefi")
		}
	}
	for _, src := range i.Image {
		if src == nil {
			return fmt.Errorf("wrong name of source package for image")
		}
	}

	return nil
}

// Repository represents the basic configuration for a package repository
type Repository struct {
	Name        string `yaml:"name,omitempty" mapstructure:"name"`
	Priority    int    `yaml:"priority,omitempty" mapstructure:"priority"`
	URI         string `yaml:"uri,omitempty" mapstructure:"uri"`
	Type        string `yaml:"type,omitempty" mapstructure:"type"`
	Arch        string `yaml:"arch,omitempty" mapstructure:"arch"`
	ReferenceID string `yaml:"reference,omitempty" mapstructure:"reference"`
}

// BuildConfig represents the config we need for building isos, raw images, artifacts
type BuildConfig struct {
	Date        bool              `yaml:"date,omitempty" mapstructure:"date"`
	Name        string            `yaml:"name,omitempty" mapstructure:"name"`
	OutDir      string            `yaml:"output,omitempty" mapstructure:"output"`
	Snapshotter SnapshotterConfig `yaml:"snapshotter,omitempty" mapstructure:"snapshotter"`

	// 'inline' and 'squash' labels ensure config fields
	// are embedded from a yaml and map PoV
	Config `yaml:",inline" mapstructure:",squash"`
}

// Sanitize checks the consistency of the struct, returns error
// if unsolvable inconsistencies are found
func (b *BuildConfig) Sanitize() error {
	// Always include default cloud-init paths
	b.CloudInitPaths = append(constants.GetCloudInitPaths(), b.CloudInitPaths...)
	return b.Config.Sanitize()
}

type DiskSpec struct {
	Size           uint                `yaml:"size,omitempty" mapstructure:"size"`
	Partitions     ElementalPartitions `yaml:"partitions,omitempty" mapstructure:"partitions"`
	Expandable     bool                `yaml:"expandable,omitempty" mapstructure:"expandable"`
	System         *ImageSource        `yaml:"system,omitempty" mapstructure:"system"`
	RecoverySystem Image               `yaml:"recovery-system,omitempty" mapstructure:"recovery-system"`
	GrubConf       string
	CloudInit      []string `yaml:"cloud-init,omitempty" mapstructure:"cloud-init"`
	GrubDefEntry   string   `yaml:"grub-entry-name,omitempty" mapstructure:"grub-entry-name"`
	Type           string   `yaml:"type,omitempty" mapstructure:"type"`
	DeployCmd      []string `yaml:"deploy-command,omitempty" mapstructure:"deploy-command"`
}

// Sanitize checks the consistency of the struct, returns error
// if unsolvable inconsistencies are found
func (d *DiskSpec) Sanitize() error {
	// If not special recovery is defined use main system source
	if d.System.IsEmpty() {
		return fmt.Errorf("undefined image source")
	}

	if d.RecoverySystem.Source.IsEmpty() {
		d.RecoverySystem.Source = d.System
	}

	if d.RecoverySystem.FS == constants.SquashFs {
		d.RecoverySystem.Label = ""
	} else if d.RecoverySystem.Label == "" {
		d.RecoverySystem.Label = constants.SystemLabel
	}

	// The disk size is enough for all partitions
	minSize := d.MinDiskSize()
	if d.Size != 0 && !d.Expandable && d.Size <= minSize {
		return fmt.Errorf("Requested disk size (%dMB) is not enough, it should be, at least, of %d", d.Size, minSize)
	}

	return nil
}

// minDiskSize counts the minimum size (MB) required for the disk given the partitions setup
func (d *DiskSpec) MinDiskSize() uint {
	var minDiskSize uint

	// First partition is aligned at the first 1MB and the last one ends at -1MB
	minDiskSize = 2
	for _, part := range d.Partitions.PartitionsByInstallOrder(PartitionList{}) {
		if part.Size == 0 {
			minDiskSize += constants.MinPartSize
		} else {
			minDiskSize += part.Size
		}
	}

	return minDiskSize
}

// InstallState tracks the installation data of the whole system
type InstallState struct {
	Date        string                     `yaml:"date,omitempty"`
	Partitions  map[string]*PartitionState `yaml:",omitempty,inline"`
	Snapshotter SnapshotterConfig          `yaml:"snapshotter,omitempty"`
}

// PartState tracks installation data of a partition
type PartitionState struct {
	FSLabel       string               `yaml:"label,omitempty"`
	RecoveryImage *SystemState         `yaml:"recovery,omitempty"`
	Snapshots     map[int]*SystemState `yaml:"snapshots,omitempty"`
}

// SystemState represents data of a deployed OS image
type SystemState struct {
	Source     *ImageSource      `yaml:"source,omitempty"`
	Digest     string            `yaml:"digest,omitempty"`
	Active     bool              `yaml:"active,omitempty"`
	Label      string            `yaml:"label,omitempty"` // Only meaningful for the recovery image
	FS         string            `yaml:"fs,omitempty"`    // Only meaningful for the recovery image
	Labels     map[string]string `yaml:"labels,omitempty"`
	Date       string            `yaml:"date,omitempty"`
	FromAction string            `yaml:"fromAction,omitempty"`
}
