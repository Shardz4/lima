// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package qemu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/lima-vm/lima/v2/pkg/limatype"
	"github.com/lima-vm/lima/v2/pkg/limatype/filenames"
	"github.com/lima-vm/lima/v2/pkg/ptr"
)

func init() {
	if os.Getenv("LIMA_TEST_MOCK_QEMU") == "1" {
		// Mock behavior for inspecting qemu features
		if len(os.Args) > 1 {
			for i, arg := range os.Args {
				if arg == "--version" {
					fmt.Println("QEMU emulator version 8.2.1")
					os.Exit(0)
				}
				if arg == "-accel" && i+1 < len(os.Args) && os.Args[i+1] == "help" {
					fmt.Println("Accelerators: kvm, hvf, whpx, nvmm, tcg")
					os.Exit(0)
				}
				if arg == "-cpu" && i+1 < len(os.Args) && os.Args[i+1] == "help" {
					fmt.Println("Available CPUs:")
					fmt.Println("  qemu64")
					fmt.Println("  max")
					fmt.Println("  host")
					os.Exit(0)
				}
			}
		}
		os.Exit(0)
	}
}

func TestSwtpmCmdline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("swtpm Unix socket mode is not supported on Windows hosts")
	}
	tmpDir := t.TempDir()

	// 1. Create a mock swtpm executable in a temporary bin directory
	binDir := filepath.Join(tmpDir, "bin")
	err := os.MkdirAll(binDir, 0o755)
	assert.NilError(t, err)

	swtpmName := "swtpm"
	if runtime.GOOS == "windows" {
		swtpmName = "swtpm.exe"
	}
	swtpmPath := filepath.Join(binDir, swtpmName)
	err = os.WriteFile(swtpmPath, []byte{}, 0o755)
	assert.NilError(t, err)

	// 2. Prepend the temporary bin directory to PATH
	oldPath := os.Getenv("PATH")
	defer os.Setenv("PATH", oldPath)
	os.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath)

	cfg := Config{
		Name:        "test-tpm",
		InstanceDir: tmpDir,
		LimaYAML:    &limatype.LimaYAML{},
	}

	exe, args, err := SwtpmCmdline(cfg)
	assert.NilError(t, err)
	assert.Equal(t, exe, swtpmPath)

	stateDir := filepath.Join(tmpDir, filenames.SwtpmDir)
	swtpmSock := filepath.Join(tmpDir, filenames.SwtpmSock)

	expectedArgs := []string{
		"socket",
		"--tpmstate", "dir=" + stateDir,
		"--ctrl", "type=unixio,path=" + swtpmSock,
		"--tpm2",
		"--log", "level=1",
	}
	assert.DeepEqual(t, args, expectedArgs)

	// Verify that state directory was created
	_, err = os.Stat(stateDir)
	assert.NilError(t, err)
}

func TestTPMQEMUArgs(t *testing.T) {
	// Setup the test executable as mock QEMU
	absSelf, err := filepath.Abs(os.Args[0])
	assert.NilError(t, err)

	os.Setenv("QEMU_SYSTEM_X86_64", filepath.ToSlash(absSelf))
	defer os.Unsetenv("QEMU_SYSTEM_X86_64")
	os.Setenv("QEMU_SYSTEM_AARCH64", filepath.ToSlash(absSelf))
	defer os.Unsetenv("QEMU_SYSTEM_AARCH64")

	os.Setenv("LIMA_TEST_MOCK_QEMU", "1")
	defer os.Unsetenv("LIMA_TEST_MOCK_QEMU")

	// We also need to configure firmware mock files so getFirmware doesn't fail
	// Candidate search path will check filepath.Dir(qemuExe)/share/edk2-xxx-code.fd
	// Since qemuExe is absSelf, we can create those files in filepath.Dir(absSelf)
	binDir := filepath.Dir(absSelf)
	localDir := filepath.Dir(binDir)

	// Create directories for firmware candidates
	shareDir1 := filepath.Join(binDir, "share")
	err = os.MkdirAll(shareDir1, 0o755)
	assert.NilError(t, err)
	shareDir2 := filepath.Join(localDir, "share", "qemu")
	err = os.MkdirAll(shareDir2, 0o755)
	assert.NilError(t, err)

	// Dummy firmware files
	dummyFiles := []string{
		filepath.Join(shareDir1, "edk2-x86_64-code.fd"),
		filepath.Join(shareDir2, "edk2-x86_64-code.fd"),
		filepath.Join(shareDir1, "edk2-aarch64-code.fd"),
		filepath.Join(shareDir2, "edk2-aarch64-code.fd"),
		filepath.Join(shareDir1, "edk2-i386-vars.fd"),
		filepath.Join(shareDir2, "edk2-i386-vars.fd"),
	}
	for _, f := range dummyFiles {
		err = os.WriteFile(f, []byte("mock-firmware-content"), 0o644)
		assert.NilError(t, err)
		defer os.Remove(f)
	}

	testCases := []struct {
		name         string
		arch         limatype.Arch
		tpmEnabled   bool
		expectedArgs []string
		excludedArgs []string
	}{
		{
			name:       "x86_64 with TPM enabled",
			arch:       limatype.X8664,
			tpmEnabled: true,
			expectedArgs: []string{
				"-chardev",
				"socket,id=chrtpm,",
				"-tpmdev",
				"emulator,id=tpm0,chardev=chrtpm",
				"-device",
				"tpm-crb,tpmdev=tpm0",
			},
		},
		{
			name:       "x86_64 with TPM disabled",
			arch:       limatype.X8664,
			tpmEnabled: false,
			excludedArgs: []string{
				"id=chrtpm",
				"emulator,id=tpm0",
				"tpm-crb",
				"tpm-tis-device",
			},
		},
		{
			name:       "aarch64 with TPM enabled",
			arch:       limatype.AARCH64,
			tpmEnabled: true,
			expectedArgs: []string{
				"-chardev",
				"socket,id=chrtpm,",
				"-tpmdev",
				"emulator,id=tpm0,chardev=chrtpm",
				"-device",
				"tpm-tis-device,tpmdev=tpm0",
			},
		},
		{
			name:       "aarch64 with TPM disabled",
			arch:       limatype.AARCH64,
			tpmEnabled: false,
			excludedArgs: []string{
				"id=chrtpm",
				"emulator,id=tpm0",
				"tpm-crb",
				"tpm-tis-device",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			// Create stub files that Cmdline might access
			err = os.WriteFile(filepath.Join(tmpDir, filenames.CIDataISO), []byte{}, 0o644)
			assert.NilError(t, err)

			cfg := Config{
				Name:        "test-tpm-vm",
				InstanceDir: tmpDir,
				LimaYAML: &limatype.LimaYAML{
					Arch: ptr.Of(tc.arch),
					CPUs: ptr.Of(1),
					Memory: ptr.Of("512MiB"),
					MountType: ptr.Of(limatype.REVSSHFS),
					Audio: limatype.Audio{
						Device: ptr.Of(""),
					},
					Video: limatype.Video{
						Display: ptr.Of("none"),
					},
					VMType: ptr.Of(limatype.QEMU),
					Firmware: limatype.Firmware{
						LegacyBIOS: ptr.Of(false),
					},
					TPM: limatype.TPM{
						Enabled: ptr.Of(tc.tpmEnabled),
					},
				},
			}

			_, args, err := Cmdline(context.Background(), cfg)
			assert.NilError(t, err)

			// Validate expected arguments are present
			for _, expected := range tc.expectedArgs {
				found := false
				for _, arg := range args {
					if strings.Contains(arg, expected) {
						found = true
						break
					}
				}
				assert.Assert(t, found, "expected arg %q to be present in %v", expected, args)
			}

			// Validate excluded arguments are not present
			for _, excluded := range tc.excludedArgs {
				for _, arg := range args {
					assert.Assert(t, !strings.Contains(arg, excluded), "did not expect arg %q to be present, but found in %v", excluded, args)
				}
			}
		})
	}
}
