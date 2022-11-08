/*
Copyright © 2022 SUSE LLC

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

package constants

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	GrubConf               = "/etc/cos/grub.cfg"
	GrubOEMEnv             = "grub_oem_env"
	GrubDefEntry           = "cOs"
	BiosPartName           = "p.bios"
	EfiLabel               = "COS_GRUB"
	EfiPartName            = "p.grub"
	ActiveLabel            = "COS_ACTIVE"
	PassiveLabel           = "COS_PASSIVE"
	SystemLabel            = "COS_SYSTEM"
	RecoveryLabel          = "COS_RECOVERY"
	RecoveryPartName       = "p.recovery"
	StateLabel             = "COS_STATE"
	StatePartName          = "p.state"
	PersistentLabel        = "COS_PERSISTENT"
	PersistentPartName     = "p.persistent"
	OEMLabel               = "COS_OEM"
	OEMPartName            = "p.oem"
	MountBinary            = "/usr/bin/mount"
	EfiDevice              = "/sys/firmware/efi"
	LinuxFs                = "ext4"
	LinuxImgFs             = "ext2"
	SquashFs               = "squashfs"
	EfiFs                  = "vfat"
	BiosFs                 = ""
	EfiSize                = uint(64)
	OEMSize                = uint(64)
	StateSize              = uint(15360)
	RecoverySize           = uint(8192)
	PersistentSize         = uint(0)
	BiosSize               = uint(1)
	ImgSize                = uint(3072)
	HTTPTimeout            = 60
	PartStage              = "partitioning"
	IsoMnt                 = "/run/initramfs/live"
	RecoveryDir            = "/run/cos/recovery"
	StateDir               = "/run/cos/state"
	OEMDir                 = "/run/cos/oem"
	PersistentDir          = "/run/cos/persistent"
	ActiveDir              = "/run/cos/active"
	EfiDir                 = "/run/cos/efi"
	RecoverySquashFile     = "recovery.squashfs"
	IsoRootFile            = "rootfs.squashfs"
	IsoEFIPath             = "/boot/uefi.img"
	ActiveImgFile          = "active.img"
	PassiveImgFile         = "passive.img"
	RecoveryImgFile        = "recovery.img"
	IsoBaseTree            = "/run/rootfsbase"
	CosSetup               = "/usr/bin/cos-setup"
	AfterInstallChrootHook = "after-install-chroot"
	AfterInstallHook       = "after-install"
	BeforeInstallHook      = "before-install"
	AfterResetChrootHook   = "after-reset-chroot"
	AfterResetHook         = "after-reset"
	BeforeResetHook        = "before-reset"
	LuetCosignPlugin       = "luet-cosign"
	LuetMtreePlugin        = "luet-mtree"
	UpgradeActive          = "active"
	UpgradeRecovery        = "recovery"
	ChannelSource          = "system/cos"
	UpgradeRecoveryDir     = "/run/initramfs/live"
	TransitionImgFile      = "transition.img"
	TransitionSquashFile   = "transition.squashfs"
	RunningStateDir        = "/run/initramfs/cos-state" // TODO: converge this constant with StateDir/RecoveryDir in dracut module from cos-toolkit
	ActiveImgName          = "active"
	PassiveImgName         = "passive"
	RecoveryImgName        = "recovery"
	GPT                    = "gpt"
	BuildImgName           = "elemental"

	//TODO these paths are abitrary, coupled to package live/grub2 and assuming xz
	// I'd suggest using `/boot/kernel` and `/boot/initrd`
	IsoKernelPath = "/boot/kernel.xz"
	IsoInitrdPath = "/boot/rootfs.xz"

	// TODO would be nice to discover these ISO loader values instead of hardcoding them
	// These values are coupled with package live/grub2
	IsoHybridMBR   = "/boot/x86_64/loader/boot_hybrid.img"
	IsoBootCatalog = "/boot/x86_64/boot.catalog"
	IsoBootFile    = "/boot/x86_64/loader/eltorito.img"

	// Default directory and file fileModes
	DirPerm  = os.ModeDir | os.ModePerm
	FilePerm = 0666

	// Eject script
	EjectScript = "#!/bin/sh\n/usr/bin/eject -rmF"
)

func GetCloudInitPaths() []string {
	return []string{"/system/oem", "/oem/", "/usr/local/cloud-config/"}
}

// GetDefaultSquashfsOptions returns the default options to use when creating a squashfs
func GetDefaultSquashfsOptions() []string {
	options := []string{"-b", "1024k", "-comp", "xz", "-Xbcj"}
	// Set the filter based on arch for best compression results
	if runtime.GOARCH == "arm64" {
		options = append(options, "arm")
	} else {
		options = append(options, "x86")
	}
	return options
}

func GetDefaultXorrisoBooloaderArgs(root, bootFile, bootCatalog, hybridMBR string) []string {
	args := []string{}
	// TODO: make this detection more robust or explicit
	// Assume ISOLINUX bootloader is used if boot file is includes 'isolinux'
	// in its name, otherwise assume an eltorito based grub2 setup
	if strings.Contains(bootFile, "isolinux") {
		args = append(args, []string{
			"-boot_image", "isolinux", fmt.Sprintf("bin_path=%s", bootFile),
			"-boot_image", "isolinux", fmt.Sprintf("system_area=%s/%s", root, hybridMBR),
			"-boot_image", "isolinux", "partition_table=on",
		}...)
	} else {
		args = append(args, []string{
			"-boot_image", "grub", fmt.Sprintf("bin_path=%s", bootFile),
			"-boot_image", "grub", fmt.Sprintf("grub2_mbr=%s/%s", root, hybridMBR),
			"-boot_image", "grub", "grub2_boot_info=on",
		}...)
	}

	args = append(args, []string{
		"-boot_image", "any", "partition_offset=16",
		"-boot_image", "any", fmt.Sprintf("cat_path=%s", bootCatalog),
		"-boot_image", "any", "cat_hidden=on",
		"-boot_image", "any", "boot_info_table=on",
		"-boot_image", "any", "platform_id=0x00",
		"-boot_image", "any", "emul_type=no_emulation",
		"-boot_image", "any", "load_size=2048",
		"-append_partition", "2", "0xef", filepath.Join(root, IsoEFIPath),
		"-boot_image", "any", "next",
		"-boot_image", "any", "efi_path=--interval:appended_partition_2:all::",
		"-boot_image", "any", "platform_id=0xef",
		"-boot_image", "any", "emul_type=no_emulation",
	}...)
	return args
}