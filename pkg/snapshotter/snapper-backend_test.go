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
	"path/filepath"
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

var _ = Describe("snapperBackend", Label("snapshotter", " btrfs"), func() {
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
		btrfsCfg = types.BtrfsConfig{Snapper: true}
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
			Expect(len(runner.GetCmds())).To(Equal(0))
			runner.ClearCmds()
		})

		It("initalizes the btrfs partition", func() {
			backend := snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)
			Expect(backend.InitBrfsPartition(rootDir)).To(Succeed())
			Expect(runner.MatchMilestones([][]string{
				{"btrfs", "quota", "enable"},
				{"btrfs", "subvolume", "create"},
				{"btrfs", "qgroup", "create"},
				{"/usr/lib/snapper/installation-helper", "--root-prefix"},
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

		It("partition initialization fails running snapper's installation helper", func() {
			errMsg := "/usr/lib/snapper/installation-helper failed"
			sEffects = append(sEffects, &sideEffect{cmd: "/usr/lib/snapper/installation-helper --root-prefix", errorMsg: errMsg})
			backend := snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)
			Expect(backend.InitBrfsPartition(rootDir)).NotTo(Succeed())
			Expect(runner.MatchMilestones([][]string{
				{"btrfs", "quota", "enable"},
				{"btrfs", "subvolume", "create"},
				{"btrfs", "qgroup", "create"},
				{"/usr/lib/snapper/installation-helper", "--root-prefix"},
			})).To(Succeed())
		})

		Describe("snapshot created", func() {
			var err error
			var snap *types.Snapshot
			backend := snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)

			BeforeEach(func() {
				By("creates the very first snapshot", func() {
					backend = snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)
					snap, err = backend.CreateNewSnapshot(rootDir, 0)
					Expect(err).ToNot(HaveOccurred())
					Expect(snap.ID).To(Equal(1))

					Expect(runner.MatchMilestones([][]string{
						{"/usr/lib/snapper/installation-helper"},
					})).To(Succeed())
				})
			})
			It("commits the first snapshot", func() {
				defaultTmpl := filepath.Join(snap.Path, "/etc/snapper/config-templates/default")
				Expect(utils.MkdirAll(fs, filepath.Dir(defaultTmpl), constants.DirPerm)).To(Succeed())
				Expect(fs.WriteFile(defaultTmpl, []byte{}, constants.FilePerm)).To(Succeed())

				Expect(utils.MkdirAll(fs, filepath.Join(snap.Path, "/etc/sysconfig"), constants.DirPerm)).To(Succeed())

				snapperCfg := filepath.Join(snap.Path, "/etc/snapper/configs")
				Expect(utils.MkdirAll(fs, snapperCfg, constants.DirPerm)).To(Succeed())

				err = backend.CommitSnapshot(rootDir, snap)
				Expect(err).To(Succeed())
				Expect(runner.MatchMilestones([][]string{
					{"snapper", "--no-dbus", "--root", rootDir, "modify", "--read-only", "--default"},
				})).To(Succeed())
			})
		})

		It("lists no snapshots", func() {
			backend := snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)
			lst, err := backend.ListSnapshots(rootDir)
			Expect(err).NotTo(HaveOccurred())
			Expect(lst.ActiveID).To(Equal(0))
			Expect(len(lst.IDs)).To(Equal(0))
			Expect(runner.MatchMilestones([][]string{
				{"snapper", "--no-dbus", "--root", "/some/root", "--csvout", "list"},
			})).To(Succeed())
		})

		It("fails to list snapshots, snapper errors out", func() {
			listCmd := "snapper --no-dbus --root /some/root --csvout list"
			sEffects = append(sEffects, &sideEffect{cmd: listCmd, errorMsg: "can't read subvolumes"})

			backend := snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)
			_, err := backend.ListSnapshots(rootDir)
			Expect(err).To(HaveOccurred())
			Expect(runner.MatchMilestones([][]string{
				strings.Fields(listCmd),
			})).To(Succeed())
		})
	})

	Describe("initiated environment while not being in active nor passive", func() {
		var listCmd, snapsList string
		var snapsSideEffect *sideEffect
		BeforeEach(func() {
			listCmd = "snapper --no-dbus --root /some/root --csvout list"
			snapsList = "1,yes,no\n"
			snapsSideEffect = &sideEffect{cmd: listCmd, cmdOut: snapsList}
			sEffects = append(sEffects, snapsSideEffect)

			Expect(utils.MkdirAll(fs, filepath.Join(statePart.MountPoint, ".snapshots"), constants.DirPerm)).To(Succeed())
		})

		Describe("initated backend", func() {
			backend := snapshotter.NewSubvolumeBackend(cfg, btrfsCfg, 4)
			BeforeEach(func() {
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
					})).To(Succeed())
				})
				// Clear commands history
				runner.ClearCmds()
			})

			Describe("snapshot created", func() {
				var snap *types.Snapshot
				var err error
				BeforeEach(func() {
					createCmd := "snapper --no-dbus --root /some/root/.snapshots/1/snapshot create"
					sEffects = append(sEffects, &sideEffect{cmd: createCmd, cmdOut: "2\n"})
					By("creates a new snapshot", func() {
						snap, err = backend.CreateNewSnapshot(rootDir, 1)
						Expect(err).ToNot(HaveOccurred())
						Expect(snap.ID).To(Equal(2))
						Expect(runner.MatchMilestones([][]string{
							strings.Fields(createCmd),
						})).To(Succeed())
					})
					runner.ClearCmds()
					defaultTmpl := filepath.Join(snap.Path, "/etc/snapper/config-templates/default")
					Expect(utils.MkdirAll(fs, filepath.Dir(defaultTmpl), constants.DirPerm)).To(Succeed())
					Expect(fs.WriteFile(defaultTmpl, []byte{}, constants.FilePerm)).To(Succeed())

					snapperSysconfig := filepath.Join(snap.Path, "/etc/sysconfig/snapper")
					Expect(utils.MkdirAll(fs, filepath.Dir(snapperSysconfig), constants.DirPerm)).To(Succeed())
					Expect(fs.WriteFile(snapperSysconfig, []byte{}, constants.FilePerm)).To(Succeed())

					snapperCfg := filepath.Join(snap.Path, "/etc/snapper/configs")
					Expect(utils.MkdirAll(fs, snapperCfg, constants.DirPerm)).To(Succeed())
				})

				It("commits a snapshot", func() {
					modifyCmd := "snapper --no-dbus --root /some/root/.snapshots/1/snapshot modify"

					err = backend.CommitSnapshot(rootDir, snap)
					Expect(err).To(Succeed())
					Expect(runner.MatchMilestones([][]string{
						strings.Fields(modifyCmd),
					})).To(Succeed())
				})

				It("fails to find snapper configuration", func() {
					Expect(utils.RemoveAll(cfg.Fs, filepath.Join(snap.Path, "/etc/snapper/config-templates/default"))).To(Succeed())
					err = backend.CommitSnapshot(rootDir, snap)
					Expect(err).NotTo(Succeed())
					Expect(len(runner.GetCmds())).To(Equal(0))
				})

				It("fails to write snapper configuration", func() {
					cfg.Fs = vfs.NewReadOnlyFS(fs)
					err = backend.CommitSnapshot(rootDir, snap)
					Expect(err).NotTo(Succeed())
					Expect(len(runner.GetCmds())).To(Equal(0))
				})

				It("fails to set the snapshot as read-only", func() {
					modifyCmd := "snapper --no-dbus --root /some/root/.snapshots/1/snapshot modify"
					errMsg := "failed setting read only property"
					sEffects = append(sEffects, &sideEffect{cmd: modifyCmd, errorMsg: errMsg})

					err = backend.CommitSnapshot(rootDir, snap)
					Expect(err).NotTo(Succeed())
					Expect(runner.MatchMilestones([][]string{
						strings.Fields(modifyCmd),
					})).To(Succeed())
				})

				It("lists expected snapshots", func() {
					listCmd := "snapper --no-dbus --root /some/root/.snapshots/1/snapshot --csvout list"
					cmdOut := "0,no,no\n1,yes,yes\n"
					sEffects = append(sEffects, &sideEffect{cmd: listCmd, cmdOut: cmdOut})

					lst, err := backend.ListSnapshots(rootDir)
					Expect(err).NotTo(HaveOccurred())
					Expect(lst.ActiveID).To(Equal(1))
					Expect(len(lst.IDs)).To(Equal(1))
					Expect(lst.IDs[0]).To(Equal(1))
					Expect(runner.MatchMilestones([][]string{
						strings.Fields(listCmd),
					})).To(Succeed())
				})
			})

			It("fails to determine a new ID while creating a new snapshot", func() {
				createCmd := "snapper --no-dbus --root /some/root/.snapshots/1/snapshot create"
				sEffects = append(sEffects, &sideEffect{cmd: createCmd, cmdOut: "wrong ID\n"})

				_, err := backend.CreateNewSnapshot(rootDir, 1)
				Expect(err).To(HaveOccurred())
				Expect(runner.MatchMilestones([][]string{
					strings.Fields(createCmd),
				})).To(Succeed())
			})

			It("fails to create a new snapshot", func() {
				createCmd := "snapper --no-dbus --root /some/root/.snapshots/1/snapshot create"
				sEffects = append(sEffects, &sideEffect{cmd: createCmd, errorMsg: "some thing failed"})

				_, err := backend.CreateNewSnapshot(rootDir, 1)
				Expect(err).To(HaveOccurred())
				Expect(runner.MatchMilestones([][]string{
					strings.Fields(createCmd),
				})).To(Succeed())
			})

			It("fails to create the working area folder", func() {
				cfg.Fs = vfs.NewReadOnlyFS(fs)
				createCmd := "snapper --no-dbus --root /some/root/.snapshots/1/snapshot create"
				sEffects = append(sEffects, &sideEffect{cmd: createCmd, cmdOut: "2\n"})

				// Snapshot was already created when the error raises, hence it attempts to delete it
				_, err := backend.CreateNewSnapshot(rootDir, 1)
				Expect(err).To(HaveOccurred())
				Expect(runner.MatchMilestones([][]string{
					strings.Fields(createCmd),
					{"snapper", "--no-dbus", "--root", "/some/root/.snapshots/1/snapshot", "delete", "--sync", "2"},
				})).To(Succeed())
			})
		})
	})

	Describe("initiated environment while being in active or passive", func() {
		var listCmd, snapsList, fMntCmd string
		var snapsSideEffect *sideEffect
		BeforeEach(func() {
			listCmd = "snapper --no-dbus --root /some/root --csvout list"
			snapsList = "1,no,no\n2,no,yes\n3,yes,no\n"
			snapsSideEffect = &sideEffect{cmd: listCmd, cmdOut: snapsList}
			sEffects = append(sEffects, snapsSideEffect)

			fMntCmd = "findmnt"

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
						strings.Fields(fMntCmd),
						strings.Fields(listCmd),
					})).To(Succeed())
					runner.ClearCmds()
				})
			})

			It("deletes the given snapshot", func() {
				Expect(backend.DeleteSnapshot(rootDir, 1)).To(Succeed())
				Expect(runner.MatchMilestones([][]string{
					{"snapper", "--no-dbus", "--root", "/some/root delete"},
				})).To(Succeed())
			})

			It("fails to delete the current snapshot", func() {
				deleteCmd := "snapper --no-dbus --root /some/root delete"
				sEffects = append(sEffects, &sideEffect{cmd: deleteCmd, errorMsg: "delete failed"})
				Expect(backend.DeleteSnapshot(rootDir, 1)).NotTo(Succeed())
				Expect(runner.MatchMilestones([][]string{
					strings.Fields(deleteCmd),
				})).To(Succeed())
			})

			It("cleans up snapshots", func() {
				cleanupCmd := "snapper --no-dbus --root /some/root cleanup --path /some/root/.snapshots number"
				Expect(backend.SnapshotsCleanup(rootDir)).To(Succeed())
				Expect(runner.MatchMilestones([][]string{
					strings.Fields(cleanupCmd),
				})).To(Succeed())
			})

			It("fails to clean up snapshots", func() {
				cleanupCmd := "snapper --no-dbus --root /some/root cleanup --path /some/root/.snapshots number"

				sEffects = append(sEffects, &sideEffect{cmd: cleanupCmd, errorMsg: "failed cleaning up"})

				Expect(backend.SnapshotsCleanup(rootDir)).NotTo(Succeed())
				Expect(runner.MatchMilestones([][]string{
					strings.Fields(cleanupCmd),
				})).To(Succeed())
			})
		})
	})
})
