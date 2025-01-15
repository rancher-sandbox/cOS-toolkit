/*
Copyright © 2022 - 2025 SUSE LLC

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
	"bufio"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/rancher/elemental-toolkit/v2/pkg/constants"
	"github.com/rancher/elemental-toolkit/v2/pkg/elemental"
	"github.com/rancher/elemental-toolkit/v2/pkg/types"
	"github.com/rancher/elemental-toolkit/v2/pkg/utils"
)

const (
	snapperRootConfig    = "/etc/snapper/configs/root"
	snapperSysconfig     = "/etc/sysconfig/snapper"
	snapperDefaultconfig = "/etc/default/snapper"
	snapperInstaller     = "/usr/lib/snapper/installation-helper"
)

var _ subvolumeBackend = (*snapperBackend)(nil)

type snapperBackend struct {
	cfg          *types.Config
	currentID    int
	activeID     int
	btrfs        *btrfsBackend
	maxSnapshots int
}

// newSnapperBackend creates a new instance for of the snapper backend
func newSnapperBackend(cfg *types.Config, maxSnapshots int) *snapperBackend {
	// snapper backend embeds an instance of a btrfs backend to fill the gap for the
	// operatons that snapper can't entirely handle.

	// TODO detect wheather the current snapper supports the required installation helper
	return &snapperBackend{cfg: cfg, maxSnapshots: maxSnapshots, btrfs: nil}
}

// Probe tests the given device and returns the found state as a backendStat struct
func (s *snapperBackend) Probe(device, mountpoint string) (stat backendStat, retErr error) {
	snapshots := filepath.Join(mountpoint, snapshotsPath)
	// On active or passive we must ensure the actual mountpoint reported by the state
	// partition is the actual root, ghw only reports a single mountpoint per device...
	if elemental.IsPassiveMode(*s.cfg) || elemental.IsActiveMode(*s.cfg) {
		rootDir, stateMount, currentID, err := findStateMount(s.cfg.Runner, device)
		if err != nil {
			return stat, err
		}

		sl, err := s.ListSnapshots(rootDir)
		if err != nil {
			return stat, err
		}

		stat.RootDir = rootDir
		stat.StateMount = stateMount
		stat.CurrentID = currentID
		stat.ActiveID = sl.ActiveID
		s.activeID, s.currentID = stat.ActiveID, stat.CurrentID
		return stat, nil
	} else if ok, _ := utils.Exists(s.cfg.Fs, snapshots); ok {
		// We must mount .snapshots to ensure snapper is capable to list snapshots
		if ok, _ := s.cfg.Mounter.IsLikelyNotMountPoint(snapshots); ok {
			err := s.cfg.Mounter.Mount(device, snapshots, "btrfs", []string{"ro", fmt.Sprintf("subvol=%s", filepath.Join(rootSubvol, snapshotsPath))})
			if err != nil {
				return stat, err
			}
			defer func() {
				err = s.cfg.Mounter.Unmount(snapshots)
				if err != nil && retErr == nil {
					retErr = err
				}
			}()
		}
		sl, err := s.ListSnapshots(mountpoint)
		if err != nil {
			return stat, err
		}
		stat.ActiveID = sl.ActiveID
	}

	stat.RootDir = mountpoint
	stat.StateMount = mountpoint
	s.activeID, s.currentID = stat.ActiveID, stat.CurrentID
	return stat, nil
}

// InitBrfsPartition is the method required to create snapshots structure on just formated partition
func (s *snapperBackend) InitBrfsPartition(rootDir string) error {
	if s.btrfs != nil {
		return s.btrfs.InitBrfsPartition(rootDir)
	}

	// create root subvolume
	err := initBtrfsQuotaAndRootSubvolume(s.cfg.Runner, s.cfg.Logger, rootDir)
	if err != nil {
		s.cfg.Logger.Errorf("failed setting quota and root subvolume")
		return err
	}

	// create required snapshots subvolumes
	out, err := s.cfg.Runner.Run(snapperInstaller, "--root-prefix", filepath.Join(rootDir, rootSubvol), "--step", "filesystem")
	if err != nil {
		s.cfg.Logger.Errorf("failed initiating btrfs subvolumes to work with snapper: %s", strings.TrimSpace(string(out)))
	}
	return err
}

// CreateNewSnapshot creates a new snapshot based on the given baseID. In case basedID == 0, this method
// assumes it will be creating the first snapshot.
func (s snapperBackend) CreateNewSnapshot(rootDir string, baseID int) (*types.Snapshot, error) {
	var newID int
	var err error
	var cmdOut []byte
	var workingDir, path string

	if baseID == 0 {
		if s.btrfs != nil {
			// Snapper does not support creating the very first empty snapshot yet
			return s.btrfs.CreateNewSnapshot(rootDir, baseID)
		}
		newID = 1
		cmdOut, err = s.cfg.Runner.Run(
			snapperInstaller, "--root-prefix", rootDir, "--step",
			"config", "--description", fmt.Sprintf("first root filesystem, snapshot %d", newID),
			"--userdata", fmt.Sprintf("%s=yes", updateProgress),
		)
		if err != nil {
			s.cfg.Logger.Errorf("failed creating initial snapshot: %s", strings.TrimSpace(string(cmdOut)))
			return nil, err
		}
		path = filepath.Join(rootDir, fmt.Sprintf(snapshotPathTmpl, newID))
		workingDir = path
	} else {
		s.cfg.Logger.Infof("Creating a new snapshot from %d", baseID)
		args := []string{
			"create", "--from", strconv.Itoa(baseID),
			"--read-write", "--print-number", "--description",
			fmt.Sprintf("Update based on snapshot %d", baseID),
			"-c", "number", "--userdata", fmt.Sprintf("%s=yes", updateProgress),
		}
		args = append(s.rootArgs(rootDir), args...)
		cmdOut, err = s.cfg.Runner.Run("snapper", args...)
		if err != nil {
			s.cfg.Logger.Errorf("snapper failed to create a new snapshot: %v", err)
			return nil, err
		}
		newID, err = strconv.Atoi(strings.TrimSpace(string(cmdOut)))
		if err != nil {
			s.cfg.Logger.Errorf("failed parsing new snapshot ID")
			return nil, err
		}

		path = filepath.Join(rootDir, fmt.Sprintf(snapshotPathTmpl, newID))
		workingDir = filepath.Join(rootDir, snapshotsPath, strconv.Itoa(newID), snapshotWorkDir)
		err = utils.MkdirAll(s.cfg.Fs, workingDir, constants.DirPerm)
		if err != nil {
			s.cfg.Logger.Errorf("failed creating the snapshot working directory: %v", err)
			_ = s.DeleteSnapshot(rootDir, newID)
			return nil, err
		}
	}

	return &types.Snapshot{
		ID:      newID,
		WorkDir: workingDir,
		Path:    path,
	}, nil
}

// CommitSnapshot set the given snapshot as default and readonly
func (s snapperBackend) CommitSnapshot(rootDir string, snapshot *types.Snapshot) error {
	err := s.configureSnapper(snapshot.Path)
	if err != nil {
		s.cfg.Logger.Errorf("failed setting snapper configuration for snapshot %d: %v", snapshot.ID, err)
		return err
	}

	args := []string{
		"modify", "--read-only", "--default", "--userdata",
		fmt.Sprintf("%s=,%s=", installProgress, updateProgress), strconv.Itoa(snapshot.ID),
	}
	args = append(s.rootArgs(rootDir), args...)
	cmdOut, err := s.cfg.Runner.Run("snapper", args...)
	if err != nil {
		s.cfg.Logger.Errorf("failed clearing userdata for snapshot %d: %s", snapshot.ID, string(cmdOut))
		return err
	}
	return nil
}

// ListSnapshots list the available snapshots in the state filesystem
func (s *snapperBackend) ListSnapshots(rootDir string) (snapshotsList, error) {
	var sl snapshotsList
	ids := []int{}
	re := regexp.MustCompile(`^(\d+),(yes|no),(yes|no)$`)

	args := []string{"--csvout", "list", "--columns", "number,default,active"}
	args = append(s.rootArgs(rootDir), args...)
	cmdOut, err := s.cfg.Runner.Run("snapper", args...)
	if err != nil {
		// snapper tries to relabel even when listing subvolumes, skip this error.
		if !strings.HasPrefix(string(cmdOut), "fsetfilecon on") {
			s.cfg.Logger.Errorf("failed collecting current snapshots: %s", string(cmdOut))
			return sl, err
		}
	}

	scanner := bufio.NewScanner(strings.NewReader(strings.TrimSpace(string(cmdOut))))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		match := re.FindStringSubmatch(line)
		if match != nil {
			id, _ := strconv.Atoi(match[1])
			if id == 0 {
				continue
			}
			ids = append(ids, id)
			if match[2] == "yes" {
				sl.ActiveID = id
			}
		}
	}
	sl.IDs = ids
	s.activeID = sl.ActiveID
	return sl, nil
}

// DeleteSnapshot deletes the given snapshot
func (s snapperBackend) DeleteSnapshot(rootDir string, id int) error {
	if s.activeID == 0 && s.currentID == 0 {
		s.cfg.Logger.Warnf("cannot delete snapshot %d without a current and active snapshot defined, nothing to do", id)
		return nil
	}
	args := []string{"delete", "--sync", strconv.Itoa(id)}
	args = append(s.rootArgs(rootDir), args...)
	cmdOut, err := s.cfg.Runner.Run("snapper", args...)
	if err != nil {
		s.cfg.Logger.Errorf("snapper failed deleting snapshot %d: %s", id, string(cmdOut))
		return err
	}
	return nil
}

// SnapshotsCleanup removes old snapshost to match the maximum criteria
func (s snapperBackend) SnapshotsCleanup(rootDir string) error {
	args := []string{"cleanup", "--path", filepath.Join(rootDir, snapshotsPath), "number"}
	args = append(s.rootArgs(rootDir), args...)
	cmdOut, err := s.cfg.Runner.Run("snapper", args...)
	if err != nil {
		s.cfg.Logger.Warnf("failed snapshots cleanup request: %s", string(cmdOut))
	}
	return err
}

// rootArgs returns the addition extra arguments to include in snapper when it is no operating
// over the actual "/" root
func (s snapperBackend) rootArgs(rootDir string) []string {
	args := []string{}
	if rootDir != "/" && s.currentID == 0 && s.activeID > 0 {
		args = []string{"--no-dbus", "--root", filepath.Join(rootDir, fmt.Sprintf(snapshotPathTmpl, s.activeID))}
	} else if rootDir != "/" {
		args = []string{"--no-dbus", "--root", rootDir}
	}
	return args
}

// configureSnapper sets the 'root' configuration for snapper
func (s snapperBackend) configureSnapper(snapshotPath string) error {
	defaultTmpl, err := utils.FindFile(s.cfg.Fs, snapshotPath, configTemplatesPaths()...)
	if err != nil {
		s.cfg.Logger.Errorf("failed to find default snapper configuration template")
		return err
	}

	sysconfigData := map[string]string{}
	sysconfig := filepath.Join(snapshotPath, snapperDefaultconfig)
	if ok, _ := utils.Exists(s.cfg.Fs, sysconfig); !ok {
		sysconfig = filepath.Join(snapshotPath, snapperSysconfig)
	}

	if ok, _ := utils.Exists(s.cfg.Fs, sysconfig); ok {
		sysconfigData, err = utils.LoadEnvFile(s.cfg.Fs, sysconfig)
		if err != nil {
			s.cfg.Logger.Errorf("failed to load global snapper sysconfig")
			return err
		}
	}
	sysconfigData["SNAPPER_CONFIGS"] = "root"

	s.cfg.Logger.Debugf("Creating sysconfig snapper configuration at '%s'", sysconfig)
	err = utils.WriteEnvFile(s.cfg.Fs, sysconfigData, sysconfig)
	if err != nil {
		s.cfg.Logger.Errorf("failed writing snapper global configuration file: %v", err)
		return err
	}

	snapCfg, err := utils.LoadEnvFile(s.cfg.Fs, defaultTmpl)
	if err != nil {
		s.cfg.Logger.Errorf("failed to load default snapper templage configuration")
		return err
	}

	snapCfg["TIMELINE_CREATE"] = "no"
	snapCfg["QGROUP"] = "1/0"
	snapCfg["NUMBER_LIMIT"] = strconv.Itoa(s.maxSnapshots)
	snapCfg["NUMBER_LIMIT_IMPORTANT"] = strconv.Itoa(s.maxSnapshots)

	rootCfg := filepath.Join(snapshotPath, snapperRootConfig)
	s.cfg.Logger.Debugf("Creating 'root' snapper configuration at '%s'", rootCfg)
	err = utils.WriteEnvFile(s.cfg.Fs, snapCfg, rootCfg)
	if err != nil {
		s.cfg.Logger.Errorf("failed writing snapper root configuration file: %v", err)
		return err
	}
	return nil
}
