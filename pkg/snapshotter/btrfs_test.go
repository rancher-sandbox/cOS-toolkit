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

var _ = Describe("Btrfs", Label("snapshotter", " btrfs"), func() {
	var cfg types.Config
	var runner *mocks.FakeRunner
	var fs vfs.FS
	var logger types.Logger
	var mounter *mocks.FakeMounter
	var cleanup func()
	var bootloader *mocks.FakeBootloader
	var memLog *bytes.Buffer
	var snapCfg types.SnapshotterConfig
	var rootDir, efiDir string
	var statePart *types.Partition
	var syscall *mocks.FakeSyscall

	BeforeEach(func() {
		rootDir = "/some/root"
		statePart = &types.Partition{
			Name:       constants.StatePartName,
			Path:       "/dev/state-device",
			MountPoint: rootDir,
		}
		efiDir = constants.BootDir
		runner = mocks.NewFakeRunner()
		mounter = mocks.NewFakeMounter()
		syscall = &mocks.FakeSyscall{}
		bootloader = &mocks.FakeBootloader{}
		memLog = bytes.NewBuffer(nil)
		logger = types.NewBufferLogger(memLog)
		logger.SetLevel(types.DebugLevel())

		var err error
		fs, cleanup, err = vfst.NewTestFS(map[string]interface{}{})
		Expect(err).Should(BeNil())

		cfg = *conf.NewConfig(
			conf.WithFs(fs),
			conf.WithRunner(runner),
			conf.WithLogger(logger),
			conf.WithMounter(mounter),
			conf.WithSyscall(syscall),
			conf.WithPlatform("linux/amd64"),
		)
		snapCfg = types.SnapshotterConfig{
			Type:     constants.BtrfsSnapshotterType,
			MaxSnaps: 4,
		}

		Expect(utils.MkdirAll(fs, rootDir, constants.DirPerm)).To(Succeed())
	})

	AfterEach(func() {
		cleanup()
	})

	It("creates a new Btrfs snapshotter instance", func() {
		Expect(snapshotter.NewSnapshotter(cfg, snapCfg, bootloader)).Error().NotTo(HaveOccurred())

		snapCfg.Type = "invalid"
		Expect(snapshotter.NewSnapshotter(cfg, snapCfg, bootloader)).Error().To(HaveOccurred())

		snapCfg.Type = constants.BtrfsSnapshotterType
		snapCfg.Config = map[string]string{"nonsense": "setup"}
		Expect(snapshotter.NewSnapshotter(cfg, snapCfg, bootloader)).Error().To(HaveOccurred())
	})

	Describe("Running transaction", func() {
		var b types.Snapshotter
		var err error

		BeforeEach(func() {
			b, err = snapshotter.NewSnapshotter(cfg, snapCfg, bootloader)
			Expect(err).NotTo(HaveOccurred())
		})

		Describe("Running transaction on a clean install", func() {
			BeforeEach(func() {
				runner.SideEffect = func(cmd string, args ...string) ([]byte, error) {
					switch cmd {
					case "findmnt":
						return []byte("/dev/sda"), nil
					default:
						return []byte{}, nil
					}
				}

				Expect(b.InitSnapshotter(statePart, efiDir)).To(Succeed())
				Expect(runner.MatchMilestones([][]string{
					{"btrfs", "quota", "enable"},
					{"btrfs", "subvolume", "create"},
					{"btrfs", "qgroup", "create"},
					{"/usr/lib/snapper/installation-helper", "--root-prefix"},
				})).To(Succeed())
			})
			Describe("Closing a transaction on a clean install", func() {
				var snap *types.Snapshot
				BeforeEach(func() {
					snap, err = b.StartTransaction()
					Expect(err).NotTo(HaveOccurred())
					Expect(snap.InProgress).To(BeTrue())
					Expect(runner.MatchMilestones([][]string{
						{"/usr/lib/snapper/installation-helper", "--root-prefix", rootDir, "--step", "config"},
					})).To(Succeed())

					defaultTmpl := filepath.Join(snap.Path, "/etc/snapper/config-templates/default")
					Expect(utils.MkdirAll(fs, filepath.Dir(defaultTmpl), constants.DirPerm)).To(Succeed())
					Expect(fs.WriteFile(defaultTmpl, []byte{}, constants.FilePerm)).To(Succeed())

					snapperSysconfig := filepath.Join(snap.Path, "/etc/sysconfig/snapper")
					Expect(utils.MkdirAll(fs, filepath.Dir(snapperSysconfig), constants.DirPerm)).To(Succeed())
					Expect(fs.WriteFile(snapperSysconfig, []byte{}, constants.FilePerm)).To(Succeed())

					snapperCfg := filepath.Join(snap.Path, "/etc/snapper/configs")
					Expect(utils.MkdirAll(fs, snapperCfg, constants.DirPerm)).To(Succeed())
				})

				It("successfully closes a transaction on a clean install", func() {
					err = b.CloseTransaction(snap)
					Expect(err).NotTo(HaveOccurred())
					Expect(runner.MatchMilestones([][]string{
						{"snapper", "--no-dbus", "--root", "/some/root", "modify", "--read-only", "--default"},
						{"snapper", "--no-dbus", "--root", "/some/root", "cleanup"},
					})).To(Succeed())
				})

				Describe("failures closing a transaction on a clean install", func() {
					var failCmd string
					BeforeEach(func() {
						runner.SideEffect = func(cmd string, args ...string) ([]byte, error) {
							fullCmd := strings.Join(append([]string{cmd}, args...), " ")
							if strings.HasPrefix(fullCmd, failCmd) {
								return []byte{}, fmt.Errorf("command '%s' failed", failCmd)
							} else if strings.HasPrefix(fullCmd, "btrfs subvolume list") {
								return []byte("ID 259 gen 13453 top level 259 path @/.snapshots/1/snapshot\n"), nil
							}
							return []byte{}, nil
						}
					})

					It("fails on missing snapper config", func() {
						failCmd = "nofailure"
						Expect(fs.Remove(filepath.Join(snap.Path, "/etc/snapper/config-templates/default"))).To(Succeed())
						err = b.CloseTransaction(snap)
						Expect(err.Error()).To(ContainSubstring("failed to find"))
					})

					It("fails setting a ro and default subvolume", func() {
						failCmd = fmt.Sprintf("snapper --no-dbus --root %s modify", rootDir)
						err = b.CloseTransaction(snap)

						Expect(err.Error()).To(ContainSubstring(failCmd))
					})

					It("does not fail if snapshots cleanup returns error", func() {
						failCmd = fmt.Sprintf("snapper --no-dbus --root %s cleanup", rootDir)
						Expect(b.CloseTransaction(snap)).To(Succeed())
					})
				})
			})

			It("fails to start a transaction on a fresh install", func() {
				runner.SideEffect = func(cmd string, args ...string) ([]byte, error) {
					fullCmd := strings.Join(append([]string{cmd}, args...), " ")
					if strings.HasPrefix(fullCmd, "/usr/lib/snapper/installation-helper --root-prefix") {
						return []byte{}, fmt.Errorf("failed installation-helper")
					}
					return []byte{}, nil
				}
				_, err = b.StartTransaction()

				Expect(err.Error()).To(ContainSubstring("failed installation-helper"))
			})
		})

		Describe("failures to initate a snapshotter on a clean install", func() {
			var failCmd string
			BeforeEach(func() {
				runner.SideEffect = func(cmd string, args ...string) ([]byte, error) {
					fullCmd := strings.Join(append([]string{cmd}, args...), " ")
					switch {
					case strings.HasPrefix(fullCmd, failCmd):
						return []byte{}, fmt.Errorf("command '%s' failed", failCmd)
					case cmd == "findmnt":
						return []byte("/dev/sda"), nil
					default:
						return []byte{}, nil
					}
				}
			})

			It("fails to enable btrfs quota", func() {
				failCmd = "btrfs quota enable"
				err = b.InitSnapshotter(statePart, efiDir)
				Expect(err.Error()).To(ContainSubstring(failCmd))
			})

			It("fails to create subvolume", func() {
				failCmd = "btrfs subvolume create"
				err = b.InitSnapshotter(statePart, efiDir)
				Expect(err.Error()).To(ContainSubstring(failCmd))
			})

			It("fails to create quota group", func() {
				failCmd = "btrfs qgroup create"
				err = b.InitSnapshotter(statePart, efiDir)
				Expect(err.Error()).To(ContainSubstring(failCmd))
			})
		})

		Describe("Running transaction on a recovery system", func() {
			BeforeEach(func() {
				runner.SideEffect = func(cmd string, args ...string) ([]byte, error) {
					fullCmd := strings.Join(append([]string{cmd}, args...), " ")
					switch {
					case strings.HasPrefix(fullCmd, fmt.Sprintf("snapper --no-dbus --root %s --csvout list", rootDir)):
						return []byte("1,yes,no\n"), nil
					case cmd == "findmnt":
						return []byte("/dev/sda[/@/.snapshots/1/snapshot]"), nil
					default:
						return []byte{}, nil
					}
				}

				Expect(utils.MkdirAll(fs, filepath.Join(rootDir, ".snapshots"), constants.DirPerm)).To(Succeed())
				Expect(b.InitSnapshotter(statePart, efiDir)).To(Succeed())
				Expect(runner.MatchMilestones([][]string{
					{"snapper", "--no-dbus", "--root", rootDir, "--csvout", "list"},
				})).To(Succeed())

			})

			Describe("Closing a transaction on a recovery system", func() {
				var snap *types.Snapshot
				BeforeEach(func() {
					runner.SideEffect = func(cmd string, args ...string) ([]byte, error) {
						fullCmd := strings.Join(append([]string{cmd}, args...), " ")
						if strings.HasPrefix(fullCmd, "snapper --no-dbus --root /some/root/.snapshots/1/snapshot create") {
							return []byte("2\n"), nil
						}
						return []byte{}, nil
					}

					snap, err = b.StartTransaction()
					Expect(err).NotTo(HaveOccurred())
					Expect(snap.InProgress).To(BeTrue())
					Expect(runner.MatchMilestones([][]string{
						{"snapper", "--no-dbus", "--root", "/some/root/.snapshots/1/snapshot", "create"},
					})).To(Succeed())

					defaultTmpl := filepath.Join(snap.Path, "/etc/snapper/config-templates/default")
					Expect(utils.MkdirAll(fs, filepath.Dir(defaultTmpl), constants.DirPerm)).To(Succeed())
					Expect(fs.WriteFile(defaultTmpl, []byte{}, constants.FilePerm)).To(Succeed())

					snapperSysconfig := filepath.Join(snap.Path, "/etc/sysconfig/snapper")
					Expect(utils.MkdirAll(fs, filepath.Dir(snapperSysconfig), constants.DirPerm)).To(Succeed())
					Expect(fs.WriteFile(snapperSysconfig, []byte{}, constants.FilePerm)).To(Succeed())

					snapperCfg := filepath.Join(snap.Path, "/etc/snapper/configs")
					Expect(utils.MkdirAll(fs, snapperCfg, constants.DirPerm)).To(Succeed())
				})

				It("successfully closes a transaction on a recovery system", func() {
					runner.SideEffect = func(cmd string, args ...string) ([]byte, error) {
						fullCmd := strings.Join(append([]string{cmd}, args...), " ")
						if strings.HasPrefix(fullCmd, "snapper --no-dbus --root /some/root/.snapshots/1/snapshot --csvout list") {
							return []byte("1,no,no\n2,yes,no\n"), nil
						}
						return []byte{}, nil
					}

					Expect(b.CloseTransaction(snap)).NotTo(HaveOccurred())
					Expect(runner.MatchMilestones([][]string{
						{"snapper", "--no-dbus", "--root", "/some/root/.snapshots/1/snapshot", "cleanup"},
					})).To(Succeed())
				})

				Describe("close transaction failures on a recovery system", func() {
					var failCmd string
					BeforeEach(func() {
						runner.SideEffect = func(cmd string, args ...string) ([]byte, error) {
							fullCmd := strings.Join(append([]string{cmd}, args...), " ")
							if strings.HasPrefix(fullCmd, failCmd) {
								return []byte{}, fmt.Errorf("command '%s' failed", failCmd)
							} else if strings.HasPrefix(fullCmd, "snapper --no-dbus --root /some/root/.snapshots/1/snapshot --csvout list") {
								return []byte("1,yes,no\n2,no,no\n"), nil
							}
							return []byte{}, nil
						}
					})

					It("fails syncing", func() {
						failCmd = "rsync"
						err = b.CloseTransaction(snap)
						Expect(err.Error()).To(ContainSubstring(failCmd))
						Expect(runner.MatchMilestones([][]string{
							{"snapper", "--no-dbus", "--root", "/some/root/.snapshots/1/snapshot", "delete"},
						})).To(Succeed())
					})

					It("fails on snapper modify", func() {
						failCmd = "snapper --no-dbus --root /some/root/.snapshots/1/snapshot modify"
						err = b.CloseTransaction(snap)
						Expect(err.Error()).To(ContainSubstring(failCmd))
						Expect(runner.MatchMilestones([][]string{
							{"snapper", "--no-dbus", "--root", "/some/root/.snapshots/1/snapshot", "delete"},
						})).To(Succeed())
					})

					It("fails setting snapshot read only and default", func() {
						failCmd = "snapper --no-dbus --root /some/root/.snapshots/1/snapshot modify --read-only --default"
						err = b.CloseTransaction(snap)
						Expect(err.Error()).To(ContainSubstring(failCmd))
						Expect(runner.MatchMilestones([][]string{
							{"snapper", "--no-dbus", "--root", "/some/root/.snapshots/1/snapshot", "delete"},
						})).To(Succeed())
					})
				})
			})

			It("fails to start a transaction on a recovery system", func() {
				runner.SideEffect = func(cmd string, args ...string) ([]byte, error) {
					fullCmd := strings.Join(append([]string{cmd}, args...), " ")
					if strings.HasPrefix(fullCmd, "snapper --no-dbus --root /some/root/.snapshots/1/snapshot create") {
						return []byte{}, fmt.Errorf("failed creating snapshot")
					}
					return []byte{}, nil
				}
				_, err = b.StartTransaction()
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed creating snapshot"))
			})

		})

		Describe("failures to init an snapshotter on a recovery system", func() {
			var failCmd string
			BeforeEach(func() {
				runner.SideEffect = func(cmd string, args ...string) ([]byte, error) {
					fullCmd := strings.Join(append([]string{cmd}, args...), " ")
					switch {
					case strings.HasPrefix(fullCmd, failCmd):
						return []byte{}, fmt.Errorf("command '%s' failed", failCmd)
					case strings.HasPrefix(fullCmd, "snapper --no-dbus --root /some/root --csvout list"):
						return []byte("1,yes,no\n"), nil
					case cmd == "findmnt":
						return []byte("/dev/sda[/@/.snapshots/1/snapshot]"), nil
					default:
						return []byte{}, nil
					}
				}
				Expect(utils.MkdirAll(fs, filepath.Join(rootDir, ".snapshots"), constants.DirPerm)).To(Succeed())
			})

			It("fails to list snapshots", func() {
				failCmd = "snapper --no-dbus --root /some/root --csvout list"
				err = b.InitSnapshotter(statePart, efiDir)
				Expect(err.Error()).To(ContainSubstring(failCmd))
			})

			It("fails to mount .snapshots subvolume", func() {
				failCmd = "nofail"
				mounter.ErrorOnMount = true
				err = b.InitSnapshotter(statePart, efiDir)
				Expect(err.Error()).To(ContainSubstring("mount"))
			})

			It("fails to umount .snapshots subvolume", func() {
				failCmd = "nofail"
				mounter.ErrorOnUnmount = true
				err = b.InitSnapshotter(statePart, rootDir)
				Expect(err.Error()).To(ContainSubstring("unmount"))
			})
		})

		Describe("Running transaction on an active system", func() {
			BeforeEach(func() {
				Expect(utils.MkdirAll(fs, constants.RunElementalDir, constants.DirPerm)).To(Succeed())
				Expect(fs.WriteFile(constants.ActiveMode, []byte("1"), constants.FilePerm)).To(Succeed())

				runner.SideEffect = func(cmd string, args ...string) ([]byte, error) {
					fullCmd := strings.Join(append([]string{cmd}, args...), " ")
					switch {
					case strings.HasPrefix(fullCmd, "snapper --csvout list"):
						return []byte("1,yes,no\n"), nil
					case cmd == "findmnt":
						mntLines := "/dev/sda[/@/.snapshots/1/snapshot] /\n"
						mntLines += "/dev/sda[/@] /run/initramfs/elemental-state\n"
						return []byte(mntLines), nil
					default:
						return []byte{}, nil
					}
				}

				Expect(b.InitSnapshotter(statePart, efiDir)).To(Succeed())
				Expect(runner.MatchMilestones([][]string{
					{"findmnt", "-lno", "SOURCE,TARGET"},
					{"snapper", "--csvout", "list"},
				})).To(Succeed())
			})

			Describe("Closing a transaction on an active system", func() {
				var snap *types.Snapshot
				BeforeEach(func() {
					runner.SideEffect = func(cmd string, args ...string) ([]byte, error) {
						fullCmd := strings.Join(append([]string{cmd}, args...), " ")
						if strings.HasPrefix(fullCmd, "snapper create --from") {
							return []byte("2\n"), nil
						}
						return []byte{}, nil
					}

					snap, err = b.StartTransaction()
					Expect(err).NotTo(HaveOccurred())
					Expect(snap.InProgress).To(BeTrue())
					Expect(runner.MatchMilestones([][]string{
						{"snapper", "create", "--from"},
					})).To(Succeed())

					defaultTmpl := filepath.Join(snap.Path, "/etc/snapper/config-templates/default")
					Expect(utils.MkdirAll(fs, filepath.Dir(defaultTmpl), constants.DirPerm)).To(Succeed())
					Expect(fs.WriteFile(defaultTmpl, []byte{}, constants.FilePerm)).To(Succeed())

					snapperSysconfig := filepath.Join(snap.Path, "/etc/sysconfig/snapper")
					Expect(utils.MkdirAll(fs, filepath.Dir(snapperSysconfig), constants.DirPerm)).To(Succeed())
					Expect(fs.WriteFile(snapperSysconfig, []byte{}, constants.FilePerm)).To(Succeed())

					snapperCfg := filepath.Join(snap.Path, "/etc/snapper/configs")
					Expect(utils.MkdirAll(fs, snapperCfg, constants.DirPerm)).To(Succeed())
				})

				It("successfully closes a transaction on an active system", func() {
					Expect(b.CloseTransaction(snap)).NotTo(HaveOccurred())
					Expect(runner.MatchMilestones([][]string{
						{"snapper", "modify"},
						{"snapper", "cleanup"},
					})).To(Succeed())
				})

				Describe("close transaction failures on an active system", func() {
					var failCmd string
					BeforeEach(func() {
						runner.SideEffect = func(cmd string, args ...string) ([]byte, error) {
							fullCmd := strings.Join(append([]string{cmd}, args...), " ")
							if strings.HasPrefix(fullCmd, failCmd) {
								return []byte{}, fmt.Errorf("command '%s' failed", failCmd)
							} else if strings.HasPrefix(fullCmd, "snapper --csvout list") {
								return []byte("1,no,yes\n2,yes,no\n"), nil
							}
							return []byte{}, nil
						}
					})

					It("fails syncing", func() {
						failCmd = "rsync"
						err = b.CloseTransaction(snap)
						Expect(err.Error()).To(ContainSubstring(failCmd))
						Expect(runner.MatchMilestones([][]string{{"snapper", "delete"}})).To(Succeed())
					})

					It("fails on snapper modify", func() {
						failCmd = "snapper modify"
						err = b.CloseTransaction(snap)
						Expect(err.Error()).To(ContainSubstring(failCmd))
						Expect(runner.MatchMilestones([][]string{{"snapper", "delete"}})).To(Succeed())
					})

					It("fails setting snapshot read only and default", func() {
						failCmd = "snapper modify --read-only --default"
						err = b.CloseTransaction(snap)
						Expect(err.Error()).To(ContainSubstring(failCmd))
						Expect(runner.MatchMilestones([][]string{{"snapper", "delete"}})).To(Succeed())
					})
				})
			})

			It("fails to start a transaction on an active system", func() {
				runner.SideEffect = func(cmd string, args ...string) ([]byte, error) {
					fullCmd := strings.Join(append([]string{cmd}, args...), " ")
					if strings.HasPrefix(fullCmd, "snapper create --from") {
						return []byte{}, fmt.Errorf("failed creating snapshot")
					}
					return []byte{}, nil
				}
				_, err = b.StartTransaction()
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed creating snapshot"))
			})
		})
	})
})
