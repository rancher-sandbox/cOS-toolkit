package cos_test

import (
	"fmt"
	"time"

	"github.com/rancher-sandbox/cOS/tests/sut"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("cOS booting fallback tests", func() {
	var s *sut.SUT

	BeforeEach(func() {
		s = sut.NewSUT()
		s.EventuallyConnects()
	})
	AfterEach(func() {
		if CurrentGinkgoTestDescription().Failed {
			s.GatherAllLogs()
		}
		if CurrentGinkgoTestDescription().Failed == false {
			s.Reset()
		}
	})

	bootAssessmentInstalled := func() {
		// Auto assessment was installed
		out, _ := s.Command("sudo cat /run/initramfs/cos-state/grubcustom")
		Expect(out).To(ContainSubstring("bootfile_loc"))

		out, _ = s.Command("sudo cat /run/initramfs/cos-state/grub_boot_assessment")
		Expect(out).To(ContainSubstring("boot_assessment_blk"))

		cmdline, _ := s.Command("sudo cat /proc/cmdline")
		Expect(cmdline).To(ContainSubstring("rd.emergency=reboot rd.shell=0 panic=5"))
	}

	Context("image is corrupted", func() {
		breakPaths := []string{"usr/lib/systemd", "bin/sh", "bin/bash", "usr/bin/bash", "usr/bin/sh"}
		It("boots in fallback when rootfs is damaged, triggered by missing files", func() {
			currentVersion := s.GetOSRelease("VERSION")

			// Auto assessment was installed
			bootAssessmentInstalled()

			out, err := s.Command(fmt.Sprintf("elemental --debug upgrade --no-verify --docker-image %s:cos-system-%s", s.GreenRepo, s.TestVersion))
			Expect(err).ToNot(HaveOccurred(), out)
			Expect(out).Should(ContainSubstring("Upgrade completed"))
			Expect(out).Should(ContainSubstring("Upgrading active partition"))

			out, _ = s.Command("sudo cat /run/initramfs/cos-state/boot_assessment")
			Expect(out).To(ContainSubstring("enable_boot_assessment=yes"))

			// Break the upgrade
			out, _ = s.Command("sudo mount -o rw,remount /run/initramfs/cos-state")
			fmt.Println(out)

			out, _ = s.Command("sudo mkdir -p /tmp/mnt/STATE")
			fmt.Println(out)

			s.Command("sudo mount /run/initramfs/cos-state/cOS/active.img /tmp/mnt/STATE")

			for _, d := range breakPaths {
				out, _ = s.Command("sudo rm -rfv /tmp/mnt/STATE/" + d)
			}

			out, _ = s.Command("sudo ls -liah /tmp/mnt/STATE/")
			fmt.Println(out)

			out, _ = s.Command("sudo umount /tmp/mnt/STATE")
			s.Command("sudo sync")

			s.Reboot(700)

			v := s.GetOSRelease("VERSION")
			Expect(v).To(Equal(currentVersion))

			cmdline, _ := s.Command("sudo cat /proc/cmdline")
			Expect(cmdline).To(And(ContainSubstring("passive.img"), ContainSubstring("upgrade_failure")), cmdline)

			Eventually(func() string {
				out, _ := s.Command("sudo ls -liah /run/cos")
				return out
			}, 5*time.Minute, 10*time.Second).Should(ContainSubstring("upgrade_failure"))
		})

		It("without upgrades boots in fallback when rootfs is damaged, triggered by missing files", func() {
			// Note, this double checks also that when we do a reset the boot assessment is re-installed
			//  elemental reset wipes disks, so the boot-assessment code is re-installed via cloud-init, so we check
			// also that
			Expect(s.BootFrom()).To(Equal(sut.Active))

			currentVersion := s.GetOSRelease("VERSION")

			// Auto assessment was installed
			bootAssessmentInstalled()

			// No manual sentinel enabled
			out, _ := s.Command("sudo cat /run/initramfs/cos-state/boot_assessment")
			Expect(out).ToNot(ContainSubstring("enable_boot_assessment=yes"))

			By("Reboot to recovery before reset")
			err := s.ChangeBootOnce(sut.Passive)
			Expect(err).ToNot(HaveOccurred())
			s.Reboot()
			Expect(s.BootFrom()).To(Equal(sut.Passive))

			// Enable permanent boot assessment, break active.img
			out, _ = s.Command("sudo mount -o rw,remount /run/initramfs/cos-state")
			fmt.Println(out)

			s.Command("sudo grub2-editenv /run/initramfs/cos-state/boot_assessment set enable_boot_assessment_always=yes")

			// Break the upgrade
			out, _ = s.Command("sudo mkdir -p /tmp/mnt/STATE")
			fmt.Println(out)

			s.Command("sudo mount /run/initramfs/cos-state/cOS/active.img /tmp/mnt/STATE")

			for _, d := range breakPaths {
				out, _ = s.Command("sudo rm -rfv /tmp/mnt/STATE/" + d)
				fmt.Println(out)
			}

			out, _ = s.Command("sudo ls -liah /tmp/mnt/STATE/")
			fmt.Println(out)

			out, _ = s.Command("sudo umount /tmp/mnt/STATE")
			s.Command("sudo sync")

			s.Reboot(700)

			v := s.GetOSRelease("VERSION")
			Expect(v).To(Equal(currentVersion))

			cmdline, _ := s.Command("sudo cat /proc/cmdline")
			Expect(cmdline).To(And(ContainSubstring("passive.img"), ContainSubstring("upgrade_failure")), cmdline)

			Eventually(func() string {
				out, _ := s.Command("sudo ls -liah /run/cos")
				return out
			}, 5*time.Minute, 10*time.Second).Should(ContainSubstring("upgrade_failure"))

			// Disable boot assessment
			out, _ = s.Command("sudo mount -o rw,remount /run/initramfs/cos-state")
			fmt.Println(out)

			s.Command("sudo grub2-editenv /run/initramfs/cos-state/boot_assessment set enable_boot_assessment_always=")
		})
	})

	Context("GRUB cannot mount image", func() {
		When("COS_ACTIVE image was corrupted", func() {
			It("fallbacks by booting into passive", func() {
				Expect(s.BootFrom()).To(Equal(sut.Active))

				_, err := s.Command("mount -o rw,remount /run/initramfs/cos-state")
				Expect(err).ToNot(HaveOccurred())
				_, err = s.Command("rm -rf /run/initramfs/cos-state/cOS/active.img")
				Expect(err).ToNot(HaveOccurred())

				s.Reboot()

				Expect(s.BootFrom()).To(Equal(sut.Passive))

				// Here we did fallback from grub. boot assessment didn't kicked in here
				cmdline, _ := s.Command("sudo cat /proc/cmdline")
				Expect(cmdline).ToNot(And(ContainSubstring("upgrade_failure")), cmdline)
			})
		})
		When("COS_ACTIVE and COS_PASSIVE images are corrupted", func() {
			It("fallbacks by booting into recovery", func() {
				Expect(s.BootFrom()).To(Equal(sut.Active))

				_, err := s.Command("mount -o rw,remount /run/initramfs/cos-state")
				Expect(err).ToNot(HaveOccurred())
				_, err = s.Command("rm -rf /run/initramfs/cos-state/cOS/active.img")
				Expect(err).ToNot(HaveOccurred())
				_, err = s.Command("rm -rf /run/initramfs/cos-state/cOS/passive.img")
				Expect(err).ToNot(HaveOccurred())
				s.Reboot()

				Expect(s.BootFrom()).To(Equal(sut.Recovery))
			})
		})
	})
})
