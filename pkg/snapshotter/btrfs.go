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

package snapshotter

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rancher/elemental-toolkit/v2/pkg/constants"
	"github.com/rancher/elemental-toolkit/v2/pkg/elemental"
	"github.com/rancher/elemental-toolkit/v2/pkg/types"
	"github.com/rancher/elemental-toolkit/v2/pkg/utils"
)

const (
	rootSubvol        = "@"
	rootSubvolID      = 257
	snapshotsSubvolID = 258
	snapshotsPath     = ".snapshots"
	snapshotPathTmpl  = ".snapshots/%d/snapshot"
	snapshotPathRegex = `.snapshots/(\d+)/snapshot`
	snapshotInfoPath  = ".snapshots/%d/info.xml"
	snapshotWorkDir   = "snapshot.workDir"
	installProgress   = "install-in-progress"
	updateProgress    = "update-in-progress"
)

func configTemplatesPaths() []string {
	return []string{
		"/etc/snapper/config-templates/default",
		"/usr/share/snapper/config-templates/default",
	}
}

var _ types.Snapshotter = (*Btrfs)(nil)

type subvolumeBackend interface {
	Probe(device string, mountpoint string) (stat backendStat, err error)
	InitBrfsPartition(rootDir string) error
	CreateNewSnapshot(rootDir string, baseID int) (*types.Snapshot, error) // baseID = 0 means first snapshot
	CommitSnapshot(rootDir string, snapshot *types.Snapshot) error         // snapshot.ID = 1 means first snapshot
	ListSnapshots(rootDir string) (snapshotsList, error)
	DeleteSnapshot(rootDir string, id int) error
	SnapshotsCleanup(rootDir string) error
}

type snapshotsList struct {
	IDs      []int
	activeID int
}

type backendStat struct {
	activeID   int
	currentID  int
	rootDir    string
	stateMount string
}

type Btrfs struct {
	cfg              types.Config
	snapshotterCfg   types.SnapshotterConfig
	btrfsCfg         types.BtrfsConfig
	rootDir          string
	efiDir           string
	activeSnapshotID int
	bootloader       types.Bootloader
	backend          subvolumeBackend
	snapshotsUmount  func() error
	snapshotsMount   func() error
}

type Date time.Time

// NewLoopDeviceSnapshotter creates a new loop device snapshotter vased on the given configuration and the given bootloader
func newBtrfsSnapshotter(cfg types.Config, snapCfg types.SnapshotterConfig, bootloader types.Bootloader) (types.Snapshotter, error) {
	if snapCfg.Type != constants.BtrfsSnapshotterType {
		msg := "invalid snapshotter type ('%s'), must be of '%s' type"
		cfg.Logger.Errorf(msg, snapCfg.Type, constants.BtrfsSnapshotterType)
		return nil, fmt.Errorf(msg, snapCfg.Type, constants.BtrfsSnapshotterType)
	}
	var btrfsCfg *types.BtrfsConfig
	var ok bool
	if snapCfg.Config == nil {
		btrfsCfg = types.NewBtrfsConfig()
	} else {
		btrfsCfg, ok = snapCfg.Config.(*types.BtrfsConfig)
		if !ok {
			msg := "failed casting BtrfsConfig type"
			cfg.Logger.Errorf(msg)
			return nil, fmt.Errorf("%s", msg)
		}
	}
	var backend subvolumeBackend
	if btrfsCfg.Snapper {
		backend = newSnapperBackend(&cfg, snapCfg.MaxSnaps)
	} else {
		backend = newBtrfsBackend(&cfg, snapCfg.MaxSnaps)
	}
	return &Btrfs{
		cfg: cfg, snapshotterCfg: snapCfg,
		btrfsCfg: *btrfsCfg, bootloader: bootloader,
		snapshotsUmount: func() error { return nil },
		snapshotsMount:  func() error { return nil },
		backend:         backend,
	}, nil
}

func (b *Btrfs) InitSnapshotter(state *types.Partition, efiDir string) error {
	var err error
	var ok bool

	b.cfg.Logger.Infof("Initiate btrfs snapshotter at %s", state.MountPoint)
	b.efiDir = efiDir

	b.cfg.Logger.Debug("Checking if essential subvolumes are already created")
	if ok, err = b.isInitiated(state); ok {
		if elemental.IsActiveMode(b.cfg) || elemental.IsPassiveMode(b.cfg) {
			return nil
		}
		b.cfg.Logger.Debug("Remount state partition at root subvolume")
	} else if err != nil {
		b.cfg.Logger.Errorf("failed loading initial snapshotter state: %s", err.Error())
		return err
	} else {
		b.cfg.Logger.Debug("Running initial btrfs configuration")
		err = b.backend.InitBrfsPartition(state.MountPoint)
		if err != nil {
			b.cfg.Logger.Errorf("failed setting the btrfs partition for snapshots: %s", err.Error())
			return err
		}
	}

	return b.remountStatePartition(state)
}

func (b *Btrfs) StartTransaction() (*types.Snapshot, error) {
	var newID int
	var err error
	var snapshot *types.Snapshot

	b.cfg.Logger.Info("Starting a btrfs snapshotter transaction")

	if b.rootDir == "" {
		b.cfg.Logger.Errorf("snapshotter should have been initalized before starting a transaction")
		return nil, fmt.Errorf("uninitialized snapshotter")
	}

	snapshot, err = b.backend.CreateNewSnapshot(b.rootDir, b.activeSnapshotID)
	if err != nil {
		b.cfg.Logger.Errorf("failed creating new snapshot: %v", err)
		return nil, err
	}

	err = utils.MkdirAll(b.cfg.Fs, constants.WorkingImgDir, constants.DirPerm)
	if err != nil {
		b.cfg.Logger.Errorf("failed creating working tree directory: %s", constants.WorkingImgDir)
		return nil, err
	}

	err = b.cfg.Mounter.Mount(snapshot.WorkDir, constants.WorkingImgDir, "bind", []string{"bind"})
	if err != nil {
		_ = b.DeleteSnapshot(newID)
		return nil, err
	}
	snapshot.MountPoint = constants.WorkingImgDir
	snapshot.InProgress = true

	return snapshot, err
}

func (b *Btrfs) CloseTransactionOnError(snapshot *types.Snapshot) (err error) {
	if snapshot.InProgress {
		err = b.cfg.Mounter.Unmount(snapshot.MountPoint)
	}
	defer func() {
		newErr := b.snapshotsUmount()
		if err == nil {
			err = newErr
		}
	}()
	err = b.DeleteSnapshot(snapshot.ID)
	return err
}

func (b *Btrfs) CloseTransaction(snapshot *types.Snapshot) (err error) {
	if !snapshot.InProgress {
		b.cfg.Logger.Debugf("No transaction to close for snapshot %d workdir", snapshot.ID)
		return fmt.Errorf("given snapshot is not in progress")
	}
	defer func() {
		if err != nil {
			_ = b.DeleteSnapshot(snapshot.ID)
		}
		newErr := b.snapshotsUmount()
		if err == nil {
			err = newErr
		}
	}()
	b.cfg.Logger.Infof("Closing transaction for snapshot %d workdir", snapshot.ID)

	// Make sure snapshots mountpoint folder is part of the resulting snapshot image
	err = utils.MkdirAll(b.cfg.Fs, filepath.Join(snapshot.WorkDir, snapshotsPath), constants.DirPerm)
	if err != nil {
		b.cfg.Logger.Errorf("failed creating snapshots folder: %v", err)
		return err
	}

	b.cfg.Logger.Debugf("Unmount %s", snapshot.MountPoint)
	err = b.cfg.Mounter.Unmount(snapshot.MountPoint)
	if err != nil {
		b.cfg.Logger.Errorf("failed umounting snapshot %d workdir bind mount", snapshot.ID)
		return err
	}

	if snapshot.ID > 1 {
		// These steps are not required for the first snapshot (snapshot.ID = 1)
		err = utils.MirrorData(b.cfg.Logger, b.cfg.Runner, b.cfg.Fs, snapshot.WorkDir, snapshot.Path)
		if err != nil {
			b.cfg.Logger.Errorf("failed syncing working directory with snapshot directory")
			return err
		}

		err = b.cfg.Fs.RemoveAll(snapshot.WorkDir)
		if err != nil {
			b.cfg.Logger.Errorf("failed deleting snapshot's workdir '%s': %s", snapshot.WorkDir, err)
			return err
		}
	}

	extraBind := map[string]string{filepath.Join(b.rootDir, snapshotsPath): filepath.Join("/", snapshotsPath)}
	err = elemental.ApplySELinuxLabels(b.cfg, snapshot.Path, extraBind)
	if err != nil {
		b.cfg.Logger.Errorf("failed relabelling snapshot path: %s", snapshot.Path)
		return err
	}

	err = b.backend.CommitSnapshot(b.rootDir, snapshot)
	if err != nil {
		b.cfg.Logger.Errorf("failed relabelling snapshot path: %s", snapshot.Path)
		return err
	}

	_ = b.setBootloader()
	_ = b.backend.SnapshotsCleanup(b.rootDir)
	return nil
}

func (b *Btrfs) DeleteSnapshot(id int) error {
	b.cfg.Logger.Infof("Deleting snapshot %d", id)

	snapshots, err := b.GetSnapshots()
	if err != nil {
		b.cfg.Logger.Errorf("failed listing available snapshots: %v", err)
		return err
	}
	if !slices.Contains(snapshots, id) {
		b.cfg.Logger.Debugf("snapshot %d not found, nothing has been deleted", id)
		return nil
	}

	return b.backend.DeleteSnapshot(b.rootDir, id)
}

func (b *Btrfs) GetSnapshots() (snapshots []int, err error) {
	var snapList snapshotsList

	if b.rootDir == "" {
		return nil, fmt.Errorf("snapshotter not initiated yet, run 'InitSnapshotter' before calling this method")
	}

	if b.activeSnapshotID > 0 {
		// Check if snapshots subvolume is mounted
		snapshotsSubolume := filepath.Join(b.rootDir, fmt.Sprintf(snapshotPathTmpl, b.activeSnapshotID), snapshotsPath)
		if notMnt, _ := b.cfg.Mounter.IsLikelyNotMountPoint(snapshotsSubolume); notMnt {
			err = b.snapshotsMount()
			if err != nil {
				return nil, err
			}
			defer func() {
				nErr := b.snapshotsUmount()
				if err == nil && nErr != nil {
					err = nErr
					snapshots = nil
				}
			}()
		}
		snapList, err = b.backend.ListSnapshots(b.rootDir)
		if err != nil {
			return nil, err
		}
		b.activeSnapshotID = snapList.activeID
		return snapList.IDs, err
	}

	return []int{}, err
}

// SnapshotImageToSource converts the given snapshot into an ImageSource. This is useful to deploy a system
// from a given snapshot, for instance setting the recovery image from a snapshot.
func (b *Btrfs) SnapshotToImageSource(snap *types.Snapshot) (*types.ImageSource, error) {
	ok, err := utils.Exists(b.cfg.Fs, snap.Path)
	if err != nil || !ok {
		msg := fmt.Sprintf("snapshot path does not exist: %s.", snap.Path)
		b.cfg.Logger.Errorf(msg)
		if err == nil {
			err = fmt.Errorf("%s", msg)
		}
		return nil, err
	}
	return types.NewDirSrc(snap.Path), nil
}

func (b *Btrfs) isInitiated(state *types.Partition) (bool, error) {
	if b.activeSnapshotID > 0 {
		return true, nil
	}

	if b.rootDir != "" {
		return false, nil
	}

	bStat, err := b.backend.Probe(state.Path, state.MountPoint)
	if err != nil {
		return false, err
	}
	b.activeSnapshotID = bStat.activeID
	b.rootDir = bStat.rootDir
	state.MountPoint = bStat.stateMount
	return bStat.activeID > 0, nil
}

func (b *Btrfs) getPassiveSnapshots() ([]int, error) {
	passives := []int{}

	snapshots, err := b.GetSnapshots()
	if err != nil {
		return nil, err
	}
	for _, snapshot := range snapshots {
		if snapshot != b.activeSnapshotID {
			passives = append(passives, snapshot)
		}
	}

	return passives, nil
}

// setBootloader sets the bootloader variables to update new passives
func (b *Btrfs) setBootloader() error {
	var passives, fallbacks []string

	b.cfg.Logger.Infof("Setting bootloader with current passive snapshots")
	ids, err := b.getPassiveSnapshots()
	if err != nil {
		b.cfg.Logger.Warnf("failed getting current passive snapshots: %v", err)
		return err
	}

	for i := len(ids) - 1; i >= 0; i-- {
		passives = append(passives, strconv.Itoa(ids[i]))
	}

	// We count first is active, then all passives and finally the recovery
	for i := 0; i <= len(ids)+1; i++ {
		fallbacks = append(fallbacks, strconv.Itoa(i))
	}
	snapsList := strings.Join(passives, " ")
	fallbackList := strings.Join(fallbacks, " ")
	envFile := filepath.Join(b.efiDir, constants.GrubOEMEnv)

	envs := map[string]string{
		constants.GrubFallback:         fallbackList,
		constants.GrubPassiveSnapshots: snapsList,
		"snapshotter":                  constants.BtrfsSnapshotterType,
	}

	err = b.bootloader.SetPersistentVariables(envFile, envs)
	if err != nil {
		b.cfg.Logger.Warnf("failed setting bootloader environment file %s: %v", envFile, err)
		return err
	}

	return err
}

func (b *Btrfs) remountStatePartition(state *types.Partition) error {
	b.cfg.Logger.Debugf("Umount %s", state.MountPoint)
	err := b.cfg.Mounter.Unmount(state.MountPoint)
	if err != nil {
		b.cfg.Logger.Errorf("failed unmounting %s: %v", state.MountPoint, err)
		return err
	}

	b.cfg.Logger.Debugf("Remount root '%s' on top level subvolume '%s'", state.MountPoint, rootSubvol)
	err = b.cfg.Mounter.Mount(state.Path, state.MountPoint, "btrfs", []string{"rw", fmt.Sprintf("subvol=%s", rootSubvol)})
	if err != nil {
		b.cfg.Logger.Errorf("failed mounting subvolume %s at %s", rootSubvol, state.MountPoint)
		return err
	}

	if b.activeSnapshotID > 0 {
		err = b.mountSnapshotsSubvolumeInSnapshot(state.MountPoint, state.Path, b.activeSnapshotID)
	}
	return err
}

func (b *Btrfs) mountSnapshotsSubvolumeInSnapshot(root, device string, snapshotID int) error {
	var mountpoint, subvol string

	b.snapshotsMount = func() error {
		b.cfg.Logger.Debugf("Mount snapshots subvolume in active snapshot %d", snapshotID)
		mountpoint = filepath.Join(filepath.Join(root, fmt.Sprintf(snapshotPathTmpl, snapshotID)), snapshotsPath)
		subvol = fmt.Sprintf("subvol=%s", filepath.Join(rootSubvol, snapshotsPath))
		return b.cfg.Mounter.Mount(device, mountpoint, "btrfs", []string{"rw", subvol})
	}
	err := b.snapshotsMount()
	if err != nil {
		b.cfg.Logger.Errorf("failed mounting subvolume %s at %s", subvol, mountpoint)
		return err
	}
	b.snapshotsUmount = func() error { return b.cfg.Mounter.Unmount(mountpoint) }
	return nil
}
