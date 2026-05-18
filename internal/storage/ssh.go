package storage

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// InjectDropbearSources copies a statically-compiled dropbear binary into the
// VM rootfs. The static binary avoids glibc version mismatches between the
// host (Ubuntu 24.04, glibc 2.38) and guest (Ubuntu 22.04, glibc 2.35).
func InjectDropbearSources(diskPath string) error {
	mountDir, err := os.MkdirTemp("", "umu-ssh-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(mountDir)

	cmdMount := exec.Command("mount", diskPath, mountDir)
	if output, err := cmdMount.CombinedOutput(); err != nil {
		return fmt.Errorf("mount disk for ssh: %s: %w", string(output), err)
	}
	defer func() {
		exec.Command("umount", mountDir).Run()
	}()

	// Copy statically-compiled dropbear binary
	srcBin := "/usr/local/bin/dropbear-static"
	dstBin := filepath.Join(mountDir, "usr/sbin/dropbear")
	os.MkdirAll(filepath.Dir(dstBin), 0755)
	os.Remove(dstBin)

	binData, err := os.ReadFile(srcBin)
	if err != nil {
		return fmt.Errorf("read static dropbear: %w", err)
	}
	if err := os.WriteFile(dstBin, binData, 0755); err != nil {
		return fmt.Errorf("copy dropbear: %w", err)
	}

	// Create dropbear config directory
	os.MkdirAll(filepath.Join(mountDir, "etc/dropbear"), 0700)

	return nil
}

// GenerateDropbearHostKey generates an ED25519 host key for the VM.
// The key is generated on the host using the host's dropbearkey binary,
// then copied into the mounted VM rootfs.
func GenerateDropbearHostKey(diskPath string) error {
	mountDir, err := os.MkdirTemp("", "umu-sshkey-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(mountDir)

	cmdMount := exec.Command("mount", diskPath, mountDir)
	if output, err := cmdMount.CombinedOutput(); err != nil {
		return fmt.Errorf("mount disk for ssh key: %s: %w", string(output), err)
	}
	defer func() {
		exec.Command("umount", mountDir).Run()
	}()

	// Generate the host key on the host
	tmpKey, err := os.CreateTemp("", "umu-dropbear-key-")
	if err != nil {
		return fmt.Errorf("create temp key file: %w", err)
	}
	tmpKeyPath := tmpKey.Name()
	tmpKey.Close()
	os.Remove(tmpKeyPath)
	defer os.Remove(tmpKeyPath)

	cmdKey := exec.Command("/usr/bin/dropbearkey", "-t", "ed25519", "-f", tmpKeyPath)
	if output, err := cmdKey.CombinedOutput(); err != nil {
		return fmt.Errorf("generate dropbear host key: %s: %w", string(output), err)
	}

	keyDst := filepath.Join(mountDir, "etc/dropbear/dropbear_ed25519_host_key")
	os.MkdirAll(filepath.Dir(keyDst), 0700)

	keyData, err := os.ReadFile(tmpKeyPath)
	if err != nil {
		return fmt.Errorf("read generated host key: %w", err)
	}
	if err := os.WriteFile(keyDst, keyData, 0600); err != nil {
		return fmt.Errorf("write host key: %w", err)
	}

	return nil
}

// GenerateOrReuseDropbearHostKey generates a host key for the VM, reusing a
// previously generated key if one exists in /var/lib/umu/ssh-keys/{project}/.
func GenerateOrReuseDropbearHostKey(projectName, diskPath string) error {
	dataDir := os.Getenv("UMU_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/umu"
	}
	keyDir := filepath.Join(dataDir, "ssh-keys", projectName)
	keyPath := filepath.Join(keyDir, "dropbear_ed25519_host_key")

	// If a key already exists for this project, reuse it
	if existingKey, err := os.ReadFile(keyPath); err == nil {
		mountDir, err := os.MkdirTemp("", "umu-sshkey-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(mountDir)

		cmdMount := exec.Command("mount", diskPath, mountDir)
		if output, err := cmdMount.CombinedOutput(); err != nil {
			return fmt.Errorf("mount disk for ssh key: %s: %w", string(output), err)
		}
		defer func() {
			exec.Command("umount", mountDir).Run()
		}()

		keyDst := filepath.Join(mountDir, "etc/dropbear/dropbear_ed25519_host_key")
		os.MkdirAll(filepath.Dir(keyDst), 0700)
		if err := os.WriteFile(keyDst, existingKey, 0600); err != nil {
			return fmt.Errorf("write host key: %w", err)
		}
		return nil
	}

	// Generate a new key
	if err := GenerateDropbearHostKey(diskPath); err != nil {
		return err
	}

	// Save the generated key for future reuse
	os.MkdirAll(keyDir, 0700)

	mountDir, err := os.MkdirTemp("", "umu-sshkey-save-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(mountDir)

	cmdMount := exec.Command("mount", diskPath, mountDir)
	if output, err := cmdMount.CombinedOutput(); err != nil {
		return fmt.Errorf("mount disk for ssh key save: %s: %w", string(output), err)
	}
	defer func() {
		exec.Command("umount", mountDir).Run()
	}()

	keySrc := filepath.Join(mountDir, "etc/dropbear/dropbear_ed25519_host_key")
	keyData, err := os.ReadFile(keySrc)
	if err != nil {
		return fmt.Errorf("read generated host key: %w", err)
	}
	if err := os.WriteFile(keyPath, keyData, 0600); err != nil {
		return fmt.Errorf("save host key: %w", err)
	}

	return nil
}

// InjectAuthorizedKeys writes the given SSH public key into the VM's
// /root/.ssh/authorized_keys file, creating the directory if needed.
// If pubKey is empty, the function is a no-op.
func InjectAuthorizedKeys(diskPath string, pubKey string) error {
	if pubKey == "" {
		return nil
	}

	mountDir, err := os.MkdirTemp("", "umu-sshkeys-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(mountDir)

	cmdMount := exec.Command("mount", diskPath, mountDir)
	if output, err := cmdMount.CombinedOutput(); err != nil {
		return fmt.Errorf("mount disk for authorized_keys: %s: %w", string(output), err)
	}
	defer func() {
		exec.Command("umount", mountDir).Run()
	}()

	sshDir := filepath.Join(mountDir, "root/.ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return fmt.Errorf("create .ssh dir: %w", err)
	}

	authPath := filepath.Join(sshDir, "authorized_keys")
	f, err := os.OpenFile(authPath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return fmt.Errorf("open authorized_keys: %w", err)
	}
	defer f.Close()

	if _, err := fmt.Fprintln(f, pubKey); err != nil {
		return fmt.Errorf("write authorized_keys: %w", err)
	}

	return nil
}

// cp copies a file from src to dst.
func cp(src, dst string) error {
	input, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read source %s: %w", src, err)
	}
	if err := os.WriteFile(dst, input, 0644); err != nil {
		return fmt.Errorf("write dest %s: %w", dst, err)
	}
	return nil
}
