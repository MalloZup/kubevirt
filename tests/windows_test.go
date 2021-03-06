/*
 * This file is part of the KubeVirt project
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * Copyright 2018 Red Hat, Inc.
 *
 */

package tests_test

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	k8sv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"

	"kubevirt.io/kubevirt/pkg/api/v1"
	"kubevirt.io/kubevirt/pkg/kubecli"
	"kubevirt.io/kubevirt/pkg/testutils"
	"kubevirt.io/kubevirt/tests"
)

const (
	windowsDisk        = "windows-disk"
	windowsFirmware    = "5d307ca9-b3ef-428c-8861-06e72d69f223"
	windowsVMIUser     = "Administrator"
	windowsVMIPassword = "Heslo123"
)

const (
	winrmCli    = "winrmcli"
	winrmCliCmd = "winrm-cli"
)

var _ = Describe("Windows VirtualMachineInstance", func() {
	flag.Parse()

	virtClient, err := kubecli.GetKubevirtClient()
	tests.PanicOnError(err)

	var windowsVMI *v1.VirtualMachineInstance

	gracePeriod := int64(0)
	spinlocks := uint32(8191)
	firmware := types.UID(windowsFirmware)
	_false := false
	windowsVMISpec := v1.VirtualMachineInstanceSpec{
		TerminationGracePeriodSeconds: &gracePeriod,
		Domain: v1.DomainSpec{
			CPU: &v1.CPU{Cores: 2},
			Features: &v1.Features{
				ACPI: v1.FeatureState{},
				APIC: &v1.FeatureAPIC{},
				Hyperv: &v1.FeatureHyperv{
					Relaxed:   &v1.FeatureState{},
					VAPIC:     &v1.FeatureState{},
					Spinlocks: &v1.FeatureSpinlocks{Retries: &spinlocks},
				},
			},
			Clock: &v1.Clock{
				ClockOffset: v1.ClockOffset{UTC: &v1.ClockOffsetUTC{}},
				Timer: &v1.Timer{
					HPET:   &v1.HPETTimer{Enabled: &_false},
					PIT:    &v1.PITTimer{TickPolicy: v1.PITTickPolicyDelay},
					RTC:    &v1.RTCTimer{TickPolicy: v1.RTCTickPolicyCatchup},
					Hyperv: &v1.HypervTimer{},
				},
			},
			Firmware: &v1.Firmware{UUID: firmware},
			Resources: v1.ResourceRequirements{
				Requests: k8sv1.ResourceList{
					k8sv1.ResourceMemory: resource.MustParse("2048Mi"),
				},
			},
			Devices: v1.Devices{
				Disks: []v1.Disk{
					{
						Name:       windowsDisk,
						VolumeName: windowsDisk,
						DiskDevice: v1.DiskDevice{Disk: &v1.DiskTarget{Bus: "sata"}},
					},
				},
			},
		},
		Volumes: []v1.Volume{
			{
				Name: windowsDisk,
				VolumeSource: v1.VolumeSource{
					Ephemeral: &v1.EphemeralVolumeSource{
						PersistentVolumeClaim: &k8sv1.PersistentVolumeClaimVolumeSource{
							ClaimName: tests.DiskWindows,
						},
					},
				},
			},
		},
	}

	tests.BeforeAll(func() {
		tests.SkipIfNoWindowsImage(virtClient)
	})

	BeforeEach(func() {
		tests.BeforeTestCleanup()
		windowsVMI = tests.NewRandomVMI()
		windowsVMI.Spec = windowsVMISpec
		tests.AddExplicitPodNetworkInterface(windowsVMI)
		windowsVMI.Spec.Domain.Devices.Interfaces[0].Model = "e1000"
	})

	It("should succeed to start a vmi", func() {
		vmi, err := virtClient.VirtualMachineInstance(tests.NamespaceTestDefault).Create(windowsVMI)
		Expect(err).To(BeNil())
		tests.WaitForSuccessfulVMIStartWithTimeout(vmi, 180)
	}, 300)

	It("should succeed to stop a running vmi", func() {
		By("Starting the vmi")
		vmi, err := virtClient.VirtualMachineInstance(tests.NamespaceTestDefault).Create(windowsVMI)
		Expect(err).To(BeNil())
		tests.WaitForSuccessfulVMIStartWithTimeout(vmi, 180)

		By("Stopping the vmi")
		err = virtClient.VirtualMachineInstance(tests.NamespaceTestDefault).Delete(vmi.Name, &metav1.DeleteOptions{})
		Expect(err).To(BeNil())
	}, 300)

	Context("with winrm connection", func() {
		var winrmcliPod *k8sv1.Pod
		var cli []string
		var output string
		var vmiIp string

		BeforeEach(func() {
			By("Creating winrm-cli pod for the future use")
			winrmcliPod = &k8sv1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: winrmCli + rand.String(5)},
				Spec: k8sv1.PodSpec{
					Containers: []k8sv1.Container{
						{
							Name:    winrmCli,
							Image:   fmt.Sprintf("%s/%s:%s", tests.KubeVirtRepoPrefix, winrmCli, tests.KubeVirtVersionTag),
							Command: []string{"sleep"},
							Args:    []string{"3600"},
						},
					},
				},
			}
			winrmcliPod, err = virtClient.CoreV1().Pods(tests.NamespaceTestDefault).Create(winrmcliPod)
			Expect(err).ToNot(HaveOccurred())

			By("Starting the windows VirtualMachineInstance")
			vmi, err := virtClient.VirtualMachineInstance(tests.NamespaceTestDefault).Create(windowsVMI)
			Expect(err).To(BeNil())
			tests.WaitForSuccessfulVMIStartWithTimeout(vmi, 180)

			vmi, err = virtClient.VirtualMachineInstance(tests.NamespaceTestDefault).Get(vmi.Name, &metav1.GetOptions{})
			vmiIp = vmi.Status.Interfaces[0].IP
			cli = []string{
				winrmCliCmd,
				"-hostname",
				vmiIp,
				"-username",
				windowsVMIUser,
				"-password",
				windowsVMIPassword,
			}
		})

		It("should have correct UUID", func() {
			command := append(cli, "wmic csproduct get \"UUID\"")
			By(fmt.Sprintf("Running \"%s\" command via winrm-cli", command))
			Eventually(func() error {
				output, err = tests.ExecuteCommandOnPod(
					virtClient,
					winrmcliPod,
					winrmcliPod.Spec.Containers[0].Name,
					command,
				)
				return err
			}, time.Minute*5, time.Second*15).ShouldNot(HaveOccurred())
			By("Checking that the Windows VirtualMachineInstance has expected UUID")
			Expect(output).Should(ContainSubstring(strings.ToUpper(windowsFirmware)))
		}, 360)

		It("should have pod IP", func() {
			command := append(cli, "ipconfig /all")
			By(fmt.Sprintf("Running \"%s\" command via winrm-cli", command))
			Eventually(func() error {
				output, err = tests.ExecuteCommandOnPod(
					virtClient,
					winrmcliPod,
					winrmcliPod.Spec.Containers[0].Name,
					command,
				)
				return err
			}, time.Minute*5, time.Second*15).ShouldNot(HaveOccurred())

			By("Checking that the Windows VirtualMachineInstance has expected IP address")
			Expect(output).Should(ContainSubstring(vmiIp))
		}, 360)
	})

	Context("with kubectl command", func() {
		var yamlFile string
		BeforeEach(func() {
			tests.SkipIfNoKubectl()
			yamlFile, err = tests.GenerateVMIJson(windowsVMI)
			Expect(err).ToNot(HaveOccurred())
		})

		AfterEach(func() {
			if yamlFile != "" {
				err = os.Remove(yamlFile)
				Expect(err).ToNot(HaveOccurred())
				yamlFile = ""
			}
		})

		It("should succeed to start a vmi", func() {
			By("Starting the vmi via kubectl command")
			_, err = tests.RunKubectlCommand("create", "-f", yamlFile)
			Expect(err).ToNot(HaveOccurred())

			tests.WaitForSuccessfulVMIStartWithTimeout(windowsVMI, 120)
		})

		It("should succeed to stop a vmi", func() {
			By("Starting the vmi via kubectl command")
			_, err = tests.RunKubectlCommand("create", "-f", yamlFile)
			Expect(err).ToNot(HaveOccurred())

			tests.WaitForSuccessfulVMIStartWithTimeout(windowsVMI, 120)

			By("Deleting the vmi via kubectl command")
			_, err = tests.RunKubectlCommand("delete", "-f", yamlFile)
			Expect(err).ToNot(HaveOccurred())

			By("Checking that the vmi does not exist anymore")
			result := virtClient.RestClient().Get().Resource(tests.VMIResource).Namespace(k8sv1.NamespaceDefault).Name(windowsVMI.Name).Do()
			Expect(result).To(testutils.HaveStatusCode(http.StatusNotFound))

			By("Checking that the vmi pod terminated")
			Eventually(func() int {
				pods, err := virtClient.CoreV1().Pods(tests.NamespaceTestDefault).List(tests.UnfinishedVMIPodSelector(windowsVMI))
				Expect(err).ToNot(HaveOccurred())
				return len(pods.Items)
			}, 75, 0.5).Should(Equal(0))
		})
	})
})
