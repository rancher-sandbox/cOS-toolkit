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

package snapshotter_test

import (
	"bytes"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	conf "github.com/rancher/elemental-toolkit/v2/pkg/config"
	"github.com/rancher/elemental-toolkit/v2/pkg/constants"
	"github.com/rancher/elemental-toolkit/v2/pkg/mocks"
	"github.com/rancher/elemental-toolkit/v2/pkg/snapshotter"
	"github.com/rancher/elemental-toolkit/v2/pkg/types"
	"github.com/rancher/elemental-toolkit/v2/pkg/utils"
	"github.com/twpayne/go-vfs/v4"
	"github.com/twpayne/go-vfs/v4/vfst"
)

var _ = Describe("btrfsBackend", Label("snapshotter", " btrfs"), func() {
	var cfg *types.Config
	var runner *mocks.FakeRunner
	var fs vfs.FS
	var logger types.Logger
	var mounter *mocks.FakeMounter
	var cleanup func()

	var memLog *bytes.Buffer
	var btrfsCfg types.BtrfsConfig
	var rootDir string
	var statePart *types.Partition
	var syscall *mocks.FakeSyscall

	type sideEffect struct {
		cmd      string
		cmdOut   string
		errorMsg string
	}
	var sEffects []*sideEffect

	BeforeEach(func() {
		sEffects = []*sideEffect{}
		rootDir = "/some/root"
		statePart = &types.Partition{
			Name:       constants.StatePartName,
			Path:       "/dev/state-device",
			MountPoint: rootDir,
		}
		runner = mocks.NewFakeRunner()
		mounter = mocks.NewFakeMounter()
		syscall = &mocks.FakeSyscall{}
		memLog = bytes.NewBuffer(nil)
		logger = types.NewBufferLogger(memLog)
		logger.SetLevel(types.DebugLevel())

		var err error
		fs, cleanup, err = vfst.NewTestFS(map[string]interface{}{})
		Expect(err).Should(BeNil())

		cfg = conf.NewConfig(
			conf.WithFs(fs),
			conf.WithRunner(runner),
			conf.WithLogger(logger),
			conf.WithMounter(mounter),
			conf.WithSyscall(syscall),
			conf.WithPlatform("linux/amd64"),
		)
		btrfsCfg = types.BtrfsConfig{Snapper: false}
		Expect(utils.MkdirAll(fs, rootDir, constants.DirPerm)).To(Succeed())

		runner.SideEffect = func(cmd string, args ...string) ([]byte, error) {
			fullCmd := strings.Join(append([]string{cmd}, args...), " ")
			for _, effect := range sEffects {
				if strings.HasPrefix(fullCmd, effect.cmd) {
					if effect.errorMsg != "" {
						return []byte(effect.cmdOut), fmt.Errorf(effect.errorMsg)
					}
					return []byte(effect.cmdOut), nil
				}
			}
			return []byte{}, nil
		}
	})

	AfterEach(func() {
		cleanup()
	})

	Describe("in a not initiated environment", func() {
		It("probes a non initiated environment, missing subvolumes", func() {
			backend := snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)
			stat, err := backend.Probe(statePart.Path, statePart.MountPoint)
			Expect(err).NotTo(HaveOccurred())
			Expect(stat.ActiveID).To(Equal(0))
			Expect(runner.MatchMilestones([][]string{{"btrfs", "subvolume", "list"}})).To(Succeed())
			runner.ClearCmds()
		})

		It("fails to probe partition, can't read btrfs subvolumes", func() {
			errMsg := "btrfs failed"
			sEffects = append(sEffects, &sideEffect{cmd: "btrfs subvolume list", errorMsg: errMsg})
			backend := snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)
			_, err := backend.Probe(statePart.Path, statePart.MountPoint)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(errMsg))
		})

		It("initalizes the btrfs partition", func() {
			backend := snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)
			Expect(backend.InitBrfsPartition(rootDir)).To(Succeed())
			Expect(runner.MatchMilestones([][]string{
				{"btrfs", "quota", "enable"},
				{"btrfs", "subvolume", "create"},
				{"btrfs", "qgroup", "create"},
				{"btrfs", "subvolume", "create"},
			})).To(Succeed())
		})

		It("partition initialization fails enabling quota", func() {
			errMsg := "btrfs quota failed"
			sEffects = append(sEffects, &sideEffect{cmd: "btrfs quota enable", errorMsg: errMsg})
			backend := snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)
			Expect(backend.InitBrfsPartition(rootDir)).NotTo(Succeed())
			Expect(runner.MatchMilestones([][]string{
				{"btrfs", "quota", "enable"},
			})).To(Succeed())
		})

		It("partition initialization fails creating subvolume", func() {
			errMsg := "subvolume create failed"
			sEffects = append(sEffects, &sideEffect{cmd: "btrfs subvolume create", errorMsg: errMsg})
			backend := snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)
			Expect(backend.InitBrfsPartition(rootDir)).NotTo(Succeed())
			Expect(runner.MatchMilestones([][]string{
				{"btrfs", "quota", "enable"},
				{"btrfs", "subvolume", "create"},
			})).To(Succeed())
		})

		It("partition initialization fails setting quota group", func() {
			errMsg := "qgroup create failed"
			sEffects = append(sEffects, &sideEffect{cmd: "btrfs qgroup create", errorMsg: errMsg})
			backend := snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)
			Expect(backend.InitBrfsPartition(rootDir)).NotTo(Succeed())
			Expect(runner.MatchMilestones([][]string{
				{"btrfs", "quota", "enable"},
				{"btrfs", "subvolume", "create"},
				{"btrfs", "qgroup", "create"},
			})).To(Succeed())
		})

		It("creates the very first snapshot", func() {
			backend := snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)
			snap, err := backend.CreateNewSnapshot(rootDir, 0)
			Expect(err).ToNot(HaveOccurred())
			Expect(snap.ID).To(Equal(1))
			Expect(runner.MatchMilestones([][]string{
				{"btrfs", "subvolume", "create"},
			})).To(Succeed())
		})

		It("fails to create the first snapshot folder", func() {
			cfg.Fs = vfs.NewReadOnlyFS(fs)
			backend := snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)
			_, err := backend.CreateNewSnapshot(rootDir, 0)
			Expect(err).To(HaveOccurred())
			Expect(runner.MatchMilestones([][]string{
				{"btrfs", "subvolume", "create"},
			})).NotTo(Succeed())
		})

		It("fails to create the first snapshot", func() {
			errMsg := "subvolume create failed"
			sEffects = append(sEffects, &sideEffect{cmd: "btrfs subvolume create", errorMsg: errMsg})
			backend := snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)
			_, err := backend.CreateNewSnapshot(rootDir, 0)
			Expect(err).To(HaveOccurred())
			Expect(runner.MatchMilestones([][]string{
				{"btrfs", "subvolume", "create"},
			})).To(Succeed())
		})

		It("lists no snapshots", func() {
			listCmd := "btrfs subvolume list"
			defVolCmd := "btrfs subvolume get-default"

			sEffects = append(sEffects, &sideEffect{cmd: listCmd, cmdOut: "there are no subvolumes"})
			sEffects = append(sEffects, &sideEffect{cmd: defVolCmd, cmdOut: "there is no default subvolume"})

			backend := snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)
			lst, err := backend.ListSnapshots(rootDir)
			Expect(err).NotTo(HaveOccurred())
			Expect(lst.ActiveID).To(Equal(0))
			Expect(len(lst.IDs)).To(Equal(0))
			Expect(runner.MatchMilestones([][]string{
				strings.Fields(listCmd),
				strings.Fields(defVolCmd),
			})).To(Succeed())
		})

		It("fails to list snapshots, can't read subvolumes", func() {
			listCmd := "btrfs subvolume list"
			defVolCmd := "btrfs subvolume get-default"

			sEffects = append(sEffects, &sideEffect{cmd: listCmd, errorMsg: "can't read subvolumes"})

			backend := snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)
			_, err := backend.ListSnapshots(rootDir)
			Expect(err).To(HaveOccurred())
			Expect(runner.MatchMilestones([][]string{
				strings.Fields(listCmd),
			})).To(Succeed())
			Expect(runner.MatchMilestones([][]string{
				strings.Fields(defVolCmd),
			})).NotTo(Succeed())
		})

		It("fails to list snapshots, can't find the default subvolume", func() {
			listCmd := "btrfs subvolume list"
			defVolCmd := "btrfs subvolume get-default"

			sEffects = append(sEffects, &sideEffect{cmd: listCmd, cmdOut: "there are no subvolumes"})
			sEffects = append(sEffects, &sideEffect{cmd: defVolCmd, errorMsg: "can't find the default subvolume"})

			backend := snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)
			_, err := backend.ListSnapshots(rootDir)
			Expect(err).To(HaveOccurred())
			Expect(runner.MatchMilestones([][]string{
				strings.Fields(listCmd),
				strings.Fields(defVolCmd),
			})).To(Succeed())
		})
	})

	Describe("initiated environment while not being in active nor passive", func() {
		var defaultVol, volumesList, listCmd, getDefCmd string
		var volSideEffect *sideEffect
		BeforeEach(func() {
			defaultVol = "ID 259 gen 13453 top level 258 path @/.snapshots/1/snapshot\n"
			volumesList = "ID 257 gen 13451 top level 3 path @\n"
			volumesList += "ID 258 gen 13452 top level 257 path @/.snapshots\n"
			volumesList += defaultVol

			listCmd = "btrfs subvolume list"
			getDefCmd = "btrfs subvolume get-default"

			volSideEffect = &sideEffect{cmd: listCmd, cmdOut: volumesList}
			sEffects = append(sEffects, volSideEffect)
		})

		Describe("initated backend", func() {
			backend := snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)
			var defVolSideEffect *sideEffect
			BeforeEach(func() {
				defVolSideEffect = &sideEffect{cmd: getDefCmd, cmdOut: defaultVol}
				sEffects = append(sEffects, defVolSideEffect)
				By("probes an initiatied environment", func() {
					backend = snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)
					stat, err := backend.Probe(statePart.Path, statePart.MountPoint)
					Expect(err).NotTo(HaveOccurred())
					Expect(stat.ActiveID).To(Equal(1))
					Expect(stat.CurrentID).To(Equal(0))
					Expect(stat.RootDir).To(Equal(statePart.MountPoint))
					Expect(stat.StateMount).To(Equal(statePart.MountPoint))
					Expect(runner.MatchMilestones([][]string{
						strings.Fields(listCmd),
						strings.Fields(getDefCmd),
					})).To(Succeed())
				})
				// Clear commands history
				runner.ClearCmds()
			})

			Describe("snapshot created", func() {
				var snap *types.Snapshot
				var err error
				BeforeEach(func() {
					By("creates a new snapshot", func() {
						snap, err = backend.CreateNewSnapshot(rootDir, 1)
						Expect(err).ToNot(HaveOccurred())
						Expect(snap.ID).To(Equal(2))
						Expect(runner.MatchMilestones([][]string{
							{"btrfs", "subvolume", "list"},
							{"btrfs", "subvolume", "get-default"},
							{"btrfs", "subvolume", "snapshot"},
						})).To(Succeed())
					})
					runner.ClearCmds()
				})

				It("commits a snapshot", func() {
					// Include the new snapshot int he subvolumes list
					volSideEffect.cmdOut += "ID 260 gen 13454 top level 259 path @/.snapshots/2/snapshot\n"

					err = backend.CommitSnapshot(rootDir, snap)
					Expect(err).To(Succeed())
					Expect(runner.MatchMilestones([][]string{
						{"btrfs", "property", "set"},
						{"btrfs", "subvolume", "list"},
						{"btrfs", "subvolume", "set-default", "260"},
					})).To(Succeed())
				})

				It("fails to set the snapshot as read-only", func() {
					propCmd := "btrfs property set"
					errMsg := "failed setting read only property"
					sEffects = append(sEffects, &sideEffect{cmd: propCmd, errorMsg: errMsg})

					err = backend.CommitSnapshot(rootDir, snap)
					Expect(err).NotTo(Succeed())
					Expect(runner.MatchMilestones([][]string{
						{"btrfs", "property", "set"},
					})).To(Succeed())
					Expect(runner.MatchMilestones([][]string{
						{"btrfs", "subvolume", "list"},
					})).NotTo(Succeed())
				})

				It("fails to fine the volume ID of the snapshot", func() {
					propCmd := "btrfs property set"
					errMsg := "failed setting read only property"
					sEffects = append(sEffects, &sideEffect{cmd: propCmd, errorMsg: errMsg})

					err = backend.CommitSnapshot(rootDir, snap)
					Expect(err).NotTo(Succeed())
					Expect(runner.MatchMilestones([][]string{
						{"btrfs", "property", "set"},
					})).To(Succeed())
					Expect(runner.MatchMilestones([][]string{
						{"btrfs", "subvolume", "list"},
					})).NotTo(Succeed())
				})

				It("fails to set new snapshot as default", func() {
					// Include the new snapshot int he subvolumes list
					volSideEffect.cmdOut += "ID 260 gen 13454 top level 259 path @/.snapshots/2/snapshot\n"

					setDefCmd := "btrfs subvolume set-default 260"
					errMsg := "subvolume set-default failed"
					sEffects = append(sEffects, &sideEffect{cmd: setDefCmd, errorMsg: errMsg})

					err = backend.CommitSnapshot(rootDir, snap)
					Expect(err).NotTo(Succeed())
					Expect(runner.MatchMilestones([][]string{
						{"btrfs", "property", "set"},
						{"btrfs", "subvolume", "list"},
						{"btrfs", "subvolume", "set-default", "260"},
					})).To(Succeed())
				})

				It("lists expected snapshots", func() {
					lst, err := backend.ListSnapshots(rootDir)
					Expect(err).NotTo(HaveOccurred())
					Expect(lst.ActiveID).To(Equal(1))
					Expect(len(lst.IDs)).To(Equal(1))
					Expect(lst.IDs[0]).To(Equal(1))
					Expect(runner.MatchMilestones([][]string{
						strings.Fields(listCmd),
						strings.Fields(getDefCmd),
					})).To(Succeed())
				})
			})

			It("fails to determine a new ID while creating a new snapshot", func() {
				errMsg := "failed listing subvolumes"
				listCmd := "btrfs subvolume list"

				// It does not detect any subvolume
				sEffects = []*sideEffect{}
				_, err := backend.CreateNewSnapshot(rootDir, 1)
				Expect(err).To(HaveOccurred())
				Expect(runner.MatchMilestones([][]string{
					strings.Fields(listCmd),
				})).To(Succeed())

				// Fails to list subvolumes
				runner.ClearCmds()
				sEffects = []*sideEffect{{cmd: listCmd, errorMsg: errMsg}}
				_, err = backend.CreateNewSnapshot(rootDir, 1)
				Expect(err).To(HaveOccurred())
				Expect(runner.MatchMilestones([][]string{
					strings.Fields(listCmd),
				})).To(Succeed())
				Expect(runner.MatchMilestones([][]string{
					{"btrfs", "subvolume", "get-default"},
				})).NotTo(Succeed())
			})

			It("fails to create a new snapshot", func() {
				errMsg := "failed create snapshot"
				cSnapCmd := "btrfs subvolume snapshot"
				sEffects = append(sEffects, &sideEffect{cmd: cSnapCmd, errorMsg: errMsg})
				_, err := backend.CreateNewSnapshot(rootDir, 1)
				Expect(err).To(HaveOccurred())
				Expect(runner.MatchMilestones([][]string{
					{"btrfs", "subvolume", "list"},
					{"btrfs", "subvolume", "get-default"},
					strings.Fields(cSnapCmd),
				})).To(Succeed())
			})
		})

		It("fails to detect active snapshot", func() {
			errMsg := "failed get-default"
			sEffects = append(sEffects, &sideEffect{cmd: getDefCmd, errorMsg: errMsg})

			backend := snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)
			_, err := backend.Probe(statePart.Path, statePart.MountPoint)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(errMsg))
		})
	})

	Describe("initiated environment while being in active or passive", func() {
		var defaultVol, volumesList, listCmd, getDefCmd, fMntCmd string
		var listSideEffect *sideEffect
		BeforeEach(func() {
			defaultVol = "ID 261 gen 13455 top level 260 path @/.snapshots/3/snapshot\n"
			volumesList = "ID 257 gen 13451 top level 3 path @\n"
			volumesList += "ID 258 gen 13452 top level 257 path @/.snapshots\n"
			volumesList += "ID 259 gen 13453 top level 258 path @/.snapshots/1/snapshot\n"
			volumesList += "ID 260 gen 13454 top level 259 path @/.snapshots/2/snapshot\n"
			volumesList += defaultVol

			listCmd = "btrfs subvolume list"
			getDefCmd = "btrfs subvolume get-default"
			fMntCmd = "findmnt"

			listSideEffect = &sideEffect{cmd: listCmd, cmdOut: volumesList}

			sEffects = append(sEffects, listSideEffect)
			sEffects = append(sEffects, &sideEffect{cmd: getDefCmd, cmdOut: defaultVol})

			// Set active mode
			Expect(utils.MkdirAll(fs, constants.RunElementalDir, constants.DirPerm)).To(Succeed())
			Expect(fs.WriteFile(constants.ActiveMode, []byte("1"), constants.FilePerm)).To(Succeed())
		})

		Describe("initated backend", func() {
			backend := snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)
			BeforeEach(func() {
				mntLines := "/dev/sda[/@/.snapshots/2/snapshot] /some/root\n"
				mntLines += "/dev/sda[/@] /some/root/run/initramfs/elemental-state\n"

				sEffects = append(sEffects, &sideEffect{cmd: fMntCmd, cmdOut: mntLines})
				By("probes an initiatied environment, in active mode", func() {
					backend = snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 2)
					stat, err := backend.Probe(statePart.Path, statePart.MountPoint)
					Expect(err).NotTo(HaveOccurred())
					Expect(stat.ActiveID).To(Equal(3))
					Expect(stat.CurrentID).To(Equal(2))
					Expect(stat.RootDir).To(Equal("/some/root"))
					Expect(stat.StateMount).To(Equal("/some/root/run/initramfs/elemental-state"))
					Expect(runner.MatchMilestones([][]string{
						strings.Fields(listCmd),
						strings.Fields(getDefCmd),
						strings.Fields(fMntCmd),
					})).To(Succeed())
					runner.ClearCmds()
				})
			})

			It("deletes the given snapshot", func() {
				Expect(backend.DeleteSnapshot(rootDir, 1)).To(Succeed())
				Expect(runner.MatchMilestones([][]string{
					{"btrfs", "subvolume", "delete"},
				})).To(Succeed())
			})

			It("fails to delete the current snapshot", func() {
				Expect(backend.DeleteSnapshot(rootDir, 2)).NotTo(Succeed())
			})

			It("fails to delete the given snapshot", func() {
				deleteCmd := "btrfs subvolume delete"
				sEffects = append(sEffects, &sideEffect{cmd: deleteCmd, errorMsg: "failed deleting snapshot"})
				Expect(backend.DeleteSnapshot(rootDir, 1)).NotTo(Succeed())
				Expect(runner.MatchMilestones([][]string{
					strings.Fields(deleteCmd),
				})).To(Succeed())
			})

			It("fails to delete snapshot 0", func() {
				Expect(backend.DeleteSnapshot(rootDir, 0)).NotTo(Succeed())
				Expect(runner.MatchMilestones([][]string{
					{"btrfs", "subvolume", "delete"},
				})).NotTo(Succeed())
			})

			It("fails to delete snapshot folder", func() {
				cfg.Fs = vfs.NewReadOnlyFS(fs)
				Expect(backend.DeleteSnapshot(rootDir, 1)).NotTo(Succeed())
				Expect(runner.MatchMilestones([][]string{
					{"btrfs", "subvolume", "delete"},
				})).To(Succeed())
			})

			It("cleans up the expected snapshot", func() {
				Expect(backend.SnapshotsCleanup(rootDir)).To(Succeed())
				Expect(runner.MatchMilestones([][]string{
					{"btrfs", "subvolume", "delete", "/some/root/.snapshots/1/snapshot"},
				})).To(Succeed())
				Expect(runner.MatchMilestones([][]string{
					{"btrfs", "subvolume", "delete", "/some/root/.snapshots/2/snapshot"},
				})).NotTo(Succeed())
				Expect(memLog.Bytes()).NotTo(ContainSubstring("current snapshot '2' can't be cleaned up"))
			})

			It("cleans up the expected snapshot, stops on current snapshot", func() {
				listSideEffect.cmdOut += "ID 262 gen 13456 top level 261 path @/.snapshots/4/snapshot\n"
				Expect(backend.SnapshotsCleanup(rootDir)).To(Succeed())
				Expect(runner.MatchMilestones([][]string{
					{"btrfs", "subvolume", "delete", "/some/root/.snapshots/1/snapshot"},
				})).To(Succeed())
				Expect(runner.MatchMilestones([][]string{
					{"btrfs", "subvolume", "delete", "/some/root/.snapshots/2/snapshot"},
				})).NotTo(Succeed())
				Expect(memLog.String()).To(ContainSubstring("current snapshot '2' can't be cleaned up"))
			})

			//TODO missing a test to check it stops on current

			It("fails to clean up the expected snapshot, can´t delete the snapshot", func() {
				deleteCmd := "btrfs subvolume delete"
				sEffects = append(sEffects, &sideEffect{cmd: deleteCmd, errorMsg: "failed deleting snapshot"})
				Expect(backend.SnapshotsCleanup(rootDir)).NotTo(Succeed())
				Expect(runner.MatchMilestones([][]string{
					{"btrfs", "subvolume", "delete", "/some/root/.snapshots/1/snapshot"},
				})).To(Succeed())
			})

			It("fails to clean up the expected snapshot, can't list subvolumes", func() {
				listCmd := "btrfs subvolume list"
				sEffects = []*sideEffect{{cmd: listCmd, errorMsg: "failed deleting snapshot"}}
				Expect(backend.SnapshotsCleanup(rootDir)).NotTo(Succeed())
				Expect(runner.MatchMilestones([][]string{
					strings.Fields(listCmd),
				})).To(Succeed())
			})
		})

		It("fails to find active or passive mounts", func() {
			errMsg := "findmnt failed"
			sEffects = append(sEffects, &sideEffect{cmd: fMntCmd, errorMsg: errMsg})

			// Set active mode
			Expect(utils.MkdirAll(fs, constants.RunElementalDir, constants.DirPerm)).To(Succeed())
			Expect(fs.WriteFile(constants.ActiveMode, []byte("1"), constants.FilePerm)).To(Succeed())

			backend := snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)
			_, err := backend.Probe(statePart.Path, statePart.MountPoint)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(errMsg))
		})
	})
})
