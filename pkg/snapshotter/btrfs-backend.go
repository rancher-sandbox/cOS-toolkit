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
	"encoding/xml"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rancher/elemental-toolkit/v2/pkg/constants"
	"github.com/rancher/elemental-toolkit/v2/pkg/elemental"
	"github.com/rancher/elemental-toolkit/v2/pkg/types"
	"github.com/rancher/elemental-toolkit/v2/pkg/utils"
)

const dateFormat = "2006-01-02 15:04:05"

var _ subvolumeBackend = (*btrfsBackend)(nil)

type SnapperSnapshotXML struct {
	XMLName     xml.Name   `xml:"snapshot"`
	Type        string     `xml:"type"`
	Num         int        `xml:"num"`
	Date        Date       `xml:"date"`
	Cleanup     string     `xml:"cleanup"`
	Description string     `xml:"description"`
	UserData    []UserData `xml:"userdata"`
}

type UserData struct {
	XMLName xml.Name `xml:"userdata"`
	Key     string   `xml:"key"`
	Value   string   `xml:"value"`
}

type Date time.Time

type btrfsSubvol struct {
	path string
	id   int
}

type btrfsSubvolList []btrfsSubvol

// newSnapperSnapshotXML returns a new instance of the struct to marshal and unmarshal
// snapper's info XML files.
func newSnapperSnapshotXML(id int, desc string) SnapperSnapshotXML {
	var usrData UserData
	if id == 1 {
		usrData = UserData{Key: installProgress, Value: "yes"}
	} else {
		usrData = UserData{Key: updateProgress, Value: "yes"}
	}
	return SnapperSnapshotXML{
		Type:        "single",
		Num:         id,
		Date:        Date(time.Now()),
		Description: desc,
		Cleanup:     "number",
		UserData:    []UserData{usrData},
	}
}

// MarshalXML is the encoder handler for time.Time types according to the
// snapper format.
func (d Date) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	t := time.Time(d)
	v := t.Format(dateFormat)
	return e.EncodeElement(v, start)
}

// UnmarshalXML is the decoder handler for time.Time types according to the
// snapper format.
func (d *Date) UnmarshalXML(dec *xml.Decoder, start xml.StartElement) error {
	var s string
	err := dec.DecodeElement(&s, &start)
	if err != nil {
		return err
	}
	t, err := time.Parse(dateFormat, s)
	if err != nil {
		return err
	}
	*d = Date(t)
	return nil
}

type btrfsBackend struct {
	cfg          *types.Config
	currentID    int
	activeID     int
	maxSnapshots int
}

// newBtrfsBackend returns a new instance of the btrfs backend
func newBtrfsBackend(cfg *types.Config, maxSnapshots int) *btrfsBackend {
	return &btrfsBackend{cfg: cfg, maxSnapshots: maxSnapshots}
}

// Probe tests the given device and returns the found state as a backendStat struct
func (b *btrfsBackend) Probe(device string, mountpoint string) (backendStat, error) {
	var stat backendStat

	rootVolume, snapshotsVolume, err := b.getStateSubvolumes(mountpoint)
	if err != nil {
		return stat, err
	}

	if (rootVolume != nil) && (snapshotsVolume != nil) {
		id, err := b.getActiveSnapshot(mountpoint)
		if err != nil {
			return stat, err
		}
		if id > 0 {
			b.activeID = id
		}
	}

	// On active or passive we must ensure the actual mountpoint reported by the state
	// partition is the actual root, ghw only reports a single mountpoint per device...
	if elemental.IsPassiveMode(*b.cfg) || elemental.IsActiveMode(*b.cfg) {
		rootDir, stateMount, currentID, err := findStateMount(b.cfg.Runner, device)
		if err != nil {
			return stat, err
		}
		stat.RootDir = rootDir
		stat.StateMount = stateMount
		stat.CurrentID, b.currentID = currentID, currentID
		stat.ActiveID = b.activeID
		return stat, nil
	}

	stat.RootDir = mountpoint
	stat.StateMount = mountpoint
	stat.ActiveID = b.activeID
	return stat, nil
}

// InitBrfsPartition is the method required to create snapshots structure on just formated partition
func (b *btrfsBackend) InitBrfsPartition(rootDir string) error {
	err := initBtrfsQuotaAndRootSubvolume(b.cfg.Runner, b.cfg.Logger, rootDir)
	if err != nil {
		b.cfg.Logger.Errorf("failed setting quota and root subvolume")
		return err
	}

	subvolume := filepath.Join(rootDir, rootSubvol, snapshotsPath)
	b.cfg.Logger.Debugf("Creating subvolume: %s", subvolume)
	cmdOut, err := b.cfg.Runner.Run("btrfs", "subvolume", "create", subvolume)
	if err != nil {
		b.cfg.Logger.Errorf("failed creating subvolume %s: %s", subvolume, string(cmdOut))
		return err
	}

	return nil
}

// CreateNewSnapshot creates a new snapshot based on the given baseID. In case basedID == 0, this method
// assumes it will be creating the first snapshot.
func (b btrfsBackend) CreateNewSnapshot(rootDir string, baseID int) (*types.Snapshot, error) {
	var workingDir string
	var desc string

	newID, err := b.computeNewID(rootDir)
	if err != nil {
		b.cfg.Logger.Errorf("failed computing a new snapshotID: %v", err)
		return nil, err
	}

	err = utils.MkdirAll(b.cfg.Fs, filepath.Join(rootDir, snapshotsPath, strconv.Itoa(newID)), constants.DirPerm)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(rootDir, fmt.Sprintf(snapshotPathTmpl, newID))

	if baseID == 0 {
		b.cfg.Logger.Debug("Creating first root filesystem as a snapshot")
		cmdOut, err := b.cfg.Runner.Run(
			"btrfs", "subvolume", "create",
			filepath.Join(rootDir, fmt.Sprintf(snapshotPathTmpl, newID)),
		)
		if err != nil {
			b.cfg.Logger.Errorf("failed creating first snapshot volume: %s", string(cmdOut))
			return nil, err
		}
		workingDir = path
		desc = fmt.Sprintf("first root filesystem, snapshot %d", newID)
	} else {
		b.cfg.Logger.Debugf("Creating snapshot %d", newID)
		cmdOut, err := b.cfg.Runner.Run(
			"btrfs", "subvolume", "snapshot",
			filepath.Join(rootDir, fmt.Sprintf(snapshotPathTmpl, baseID)),
			filepath.Join(rootDir, fmt.Sprintf(snapshotPathTmpl, newID)),
		)
		if err != nil {
			b.cfg.Logger.Errorf("failed creating first snapshot volume: %s", string(cmdOut))
			return nil, err
		}
		workingDir = filepath.Join(rootDir, snapshotsPath, strconv.Itoa(newID), snapshotWorkDir)
		err = utils.MkdirAll(b.cfg.Fs, workingDir, constants.DirPerm)
		if err != nil {
			b.cfg.Logger.Errorf("failed creating the snapshot working directory: %v", err)
			_ = b.DeleteSnapshot(rootDir, newID)
			return nil, err
		}
		desc = fmt.Sprintf("Update based on snapshot %d", baseID)
	}
	snapperXML := filepath.Join(rootDir, fmt.Sprintf(snapshotInfoPath, newID))
	err = b.writeSnapperSnapshotXML(snapperXML, newSnapperSnapshotXML(newID, desc))
	if err != nil {
		b.cfg.Logger.Errorf("failed creating snapper info XML")
		return nil, err
	}

	return &types.Snapshot{
		ID:      newID,
		WorkDir: workingDir,
		Path:    path,
	}, nil
}

// CommitSnapshot set the given snapshot as default and readonly
func (b btrfsBackend) CommitSnapshot(rootDir string, snapshot *types.Snapshot) error {
	err := b.clearInProgressMetadata(rootDir, snapshot.ID)
	if err != nil {
		b.cfg.Logger.Errorf("failed updating snapshot %d metadata: %v", snapshot.ID, err)
		return err
	}

	cmdOut, err := b.cfg.Runner.Run("btrfs", "property", "set", snapshot.Path, "ro", "true")
	if err != nil {
		b.cfg.Logger.Errorf("failed setting read only property to snapshot %d: %s", snapshot.ID, string(cmdOut))
		return err
	}

	subvolID, err := b.findSubvolumeByPath(rootDir, fmt.Sprintf(snapshotPathTmpl, snapshot.ID))
	if err != nil {
		b.cfg.Logger.Error("failed finding subvolume")
		return err
	}

	cmdOut, err = b.cfg.Runner.Run("btrfs", "subvolume", "set-default", strconv.Itoa(subvolID), snapshot.Path)
	if err != nil {
		b.cfg.Logger.Errorf("failed setting read only property to snapshot %d: %s", snapshot.ID, string(cmdOut))
		return err
	}
	return nil
}

// ListSnapshots list the available snapshots in the state filesystem.
func (b btrfsBackend) ListSnapshots(rootDir string) (snapshotsList, error) {
	var snaps snapshotsList

	list, err := b.getSubvolumes(rootDir)
	if err != nil {
		b.cfg.Logger.Errorf("failed listing subvolumes: %v", err)
		return snaps, err
	}

	activeID, err := b.getActiveSnapshot(rootDir)
	if err != nil {
		b.cfg.Logger.Errorf("failed looking for active snapshot: %v", err)
		return snaps, err
	}

	snaps.IDs = subvolumesListToSnapshotsIDs(list)
	snaps.ActiveID = activeID
	b.activeID = activeID
	return snaps, nil
}

// DeleteSnapshot deletes the given snapshot
func (b btrfsBackend) DeleteSnapshot(rootDir string, id int) error {
	if id <= 0 {
		return fmt.Errorf("invalid id, should be higher than zero")
	}
	if id == b.currentID {
		return fmt.Errorf("invalid id, cannot delete current snapshot")
	}
	cmdOut, err := b.cfg.Runner.Run("btrfs", "subvolume", "delete", filepath.Join(rootDir, fmt.Sprintf(snapshotPathTmpl, id)))
	if err != nil {
		b.cfg.Logger.Errorf("failed deleting snapshot %d: %s", id, string(cmdOut))
		return err
	}
	err = utils.RemoveAll(b.cfg.Fs, filepath.Join(rootDir, snapshotsPath, strconv.Itoa(id)))
	if err != nil {
		b.cfg.Logger.Errorf("failed deleting snapshot %d folder: %v", id, err)
		return err
	}
	return nil
}

// SnapshotsCleanup removes old snapshost to match the maximum criteria. Starts deleting the oldest and
// continues deleting the next one until it matches the maximum number. It cannot delete the current
// snapshot, as soon as the snapshot to be deleted matches the current one it returns without error
func (b btrfsBackend) SnapshotsCleanup(rootDir string) error {
	list, err := b.ListSnapshots(rootDir)
	if err != nil {
		b.cfg.Logger.Errorf("failed cleaning up up snaphots, could not list them: %v", err)
		return err
	}
	snapsToDelete := len(list.IDs) - b.maxSnapshots
	if snapsToDelete > 0 {
		slices.Sort(list.IDs)
		for i := range snapsToDelete {
			if list.IDs[i] == b.currentID {
				b.cfg.Logger.Warnf("current snapshot '%d' can't be cleaned up, stopping", list.IDs[i])
				break
			}
			err = b.DeleteSnapshot(rootDir, list.IDs[i])
			if err != nil {
				b.cfg.Logger.Errorf("failed cleaning up up snaphots, could delete snapshot '%d': %v", list.IDs[i], err)
				return err
			}
		}
	}
	return nil
}

// writeSnapperSnapshotXML writes the info.xml file used by snapper to hold some snapshot metadata
func (b btrfsBackend) writeSnapperSnapshotXML(filepath string, snapshot SnapperSnapshotXML) error {
	data, err := xml.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		b.cfg.Logger.Errorf("failed marhsalling snapper's snapshot XML: %v", err)
		return err
	}
	err = b.cfg.Fs.WriteFile(filepath, data, constants.FilePerm)
	if err != nil {
		b.cfg.Logger.Errorf("failed writing snapper's snapshot XML: %v", err)
		return err
	}
	return nil
}

// loadSnapperSnapshotXML unmarshals the info.xml file used by snapper to hold some snapshot metadata
func (b btrfsBackend) loadSnapperSnapshotXML(filepath string) (SnapperSnapshotXML, error) {
	var data SnapperSnapshotXML

	bData, err := b.cfg.Fs.ReadFile(filepath)
	if err != nil {
		b.cfg.Logger.Errorf("failed reading '%s' file: %v", filepath, err)
		return data, err
	}

	err = xml.Unmarshal(bData, &data)
	if err != nil {
		b.cfg.Logger.Errorf("failed decoding '%s' file contents: %v", filepath, err)
		return data, err
	}

	return data, nil
}

// findSubvolumeByPath returns the subvolume ID from a given subvolume path
func (b btrfsBackend) findSubvolumeByPath(rootDir, path string) (int, error) {
	subvolumes, err := b.getSubvolumes(rootDir)
	if err != nil {
		b.cfg.Logger.Errorf("failed loading subvolumes: %v", err)
		return 0, err
	}

	for _, subvol := range subvolumes {
		if strings.Contains(subvol.path, path) {
			return subvol.id, nil
		}
	}

	b.cfg.Logger.Errorf("could not find subvolume with path '%s' in subvolumes list '%v'", path, subvolumes)
	return 0, fmt.Errorf("can't find subvolume '%s'", path)
}

// getSubvolumes lists all btrfs subvolumes for the given root
func (b btrfsBackend) getSubvolumes(rootDir string) (btrfsSubvolList, error) {
	out, err := b.cfg.Runner.Run("btrfs", "subvolume", "list", "-a", "--sort=path", rootDir)
	if err != nil {
		b.cfg.Logger.Errorf("failed listing btrfs subvolumes: %s", err.Error())
		return nil, err
	}
	return parseVolumes(strings.TrimSpace(string(out))), nil
}

func (b btrfsBackend) getStateSubvolumes(rootDir string) (rootVolume *btrfsSubvol, snapshotsVolume *btrfsSubvol, err error) {
	volumes, err := b.getSubvolumes(rootDir)
	if err != nil {
		return nil, nil, err
	}

	snapshots := filepath.Join(rootSubvol, snapshotsPath)
	b.cfg.Logger.Debugf(
		"Looking for subvolumes %s and %s in subvolume list: %v",
		rootSubvol, snapshots, volumes,
	)
	for _, vol := range volumes {
		if vol.path == rootSubvol {
			rootVolume = &vol
		} else if vol.path == snapshots {
			snapshotsVolume = &vol
		}
	}

	return rootVolume, snapshotsVolume, err
}

// getActiveSnapshot returns the active snapshot. Zero value means there is no active or default snapshot
func (b btrfsBackend) getActiveSnapshot(rootDir string) (int, error) {
	out, err := b.cfg.Runner.Run("btrfs", "subvolume", "get-default", rootDir)
	if err != nil {
		b.cfg.Logger.Errorf("failed listing btrfs subvolumes: %s", err.Error())
		return 0, err
	}
	list := parseVolumes(strings.TrimSpace(string(out)))
	ids := subvolumesListToSnapshotsIDs(list)
	if len(ids) == 1 {
		return ids[0], nil
	}
	if len(ids) == 0 {
		return 0, nil
	}
	return 0, fmt.Errorf("detected multiple active snapshots")
}

// parseVolumes parses the output 'btrfs subvolume list' and similar commands
// in order to extract subvolume ID and path
func parseVolumes(rawBtrfsList string) btrfsSubvolList {
	re := regexp.MustCompile(`^ID (\d+) gen \d+ top level \d+ path (.*)$`)
	list := btrfsSubvolList{}

	scanner := bufio.NewScanner(strings.NewReader(rawBtrfsList))
	for scanner.Scan() {
		match := re.FindStringSubmatch(strings.TrimSpace(scanner.Text()))
		if match != nil {
			id, _ := strconv.Atoi(match[1])
			path := strings.TrimPrefix(match[2], "<FS_TREE>/")
			list = append(list, btrfsSubvol{id: id, path: path})
		}
	}
	return list
}

// subvolumesListToSnapshotsIDs turns a list of btrfs subvolumes into a list
// of snapshots IDs
func subvolumesListToSnapshotsIDs(list btrfsSubvolList) []int {
	ids := []int{}
	re := regexp.MustCompile(snapshotPathRegex)
	for _, v := range list {
		match := re.FindStringSubmatch(v.path)
		if match != nil {
			id, _ := strconv.Atoi(match[1])
			ids = append(ids, id)
		}
	}
	return ids
}

// clearInProgressMetadata removes backend custom user data from the info.xml
func (b btrfsBackend) clearInProgressMetadata(rootDir string, id int) error {
	snapperXML := filepath.Join(rootDir, fmt.Sprintf(snapshotInfoPath, id))
	snapshotData, err := b.loadSnapperSnapshotXML(snapperXML)
	if err != nil {
		b.cfg.Logger.Errorf("failed reading snapshot %d metadata: %v", id, err)
		return err
	}

	var usrData []UserData
	for _, ud := range snapshotData.UserData {
		if ud.Key == updateProgress || ud.Key == installProgress {
			continue
		}
		usrData = append(usrData, ud)
	}
	snapshotData.UserData = usrData

	err = b.writeSnapperSnapshotXML(snapperXML, snapshotData)
	if err != nil {
		b.cfg.Logger.Errorf("failed writing snapshot %d metadata: %v", id, err)
		return err
	}
	return nil
}

// computeNewID defines the next available snapshot ID
func (b btrfsBackend) computeNewID(rootDir string) (int, error) {
	if b.activeID == 0 {
		// If there is no active snapshot we assume this will be the first one
		return 1, nil
	}
	list, err := b.ListSnapshots(rootDir)
	if err != nil {
		b.cfg.Logger.Errorf("failed computing new ID, could not list snapshots")
		return 0, err
	}
	if len(list.IDs) == 0 {
		return 0, fmt.Errorf("no snapshots found, inconsistent state")
	}
	return slices.Max(list.IDs) + 1, nil
}
