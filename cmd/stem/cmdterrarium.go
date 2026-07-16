package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
)

// Pinned digests for downloaded bootstrap artifacts. These are
// trust-on-first-use pins: each records the sha256 of the bytes the upstream
// URL served when the pin was taken, so any later download that differs —
// substitution, truncation, silent drift — fails closed instead of being
// booted as a guest. A pin does NOT prove provenance: upstream was trusted
// once, at pin time. To move to a new upstream version, verify the new bytes
// deliberately and update the pin in the same change.
const (
	firecrackerKernelURL    = "https://s3.amazonaws.com/spec.ccfc.min/img/quickstart_guide/x86_64/kernels/vmlinux.bin"
	firecrackerKernelSHA256 = "ea5e7d5cf494a8c4ba043259812fc018b44880d70bcbbfc4d57d2760631b1cd6"

	// Statically linked busybox provides the guest userland (sh, echo,
	// sleep, ...) that sprout-agent executes commands with. The rootfs has
	// no distribution inside it, so without this every guest command would
	// fail to resolve.
	busyboxURL    = "https://busybox.net/downloads/binaries/1.35.0-x86_64-linux-musl/busybox"
	busyboxSHA256 = "6e123e7f3202a8c1e9b1f94d8941580a25135382b99e8d3e34fb858bba311348"
)

// busyboxApplets are the command names linked to busybox inside the guest's
// /bin. Busybox dispatches on argv[0], so each symlink is a working tool.
var busyboxApplets = []string{"sh", "echo", "sleep", "cat", "ls", "mkdir", "rm", "cp", "mv", "true", "false", "env", "uname"}

func runTerrariumCmd(ctx context.Context, args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: tendril terrarium <command>")
		fmt.Println("Commands:")
		fmt.Println("  init-firecracker   Bootstrap a Firecracker microVM rootfs and kernel")
		os.Exit(1)
	}

	switch args[0] {
	case "init-firecracker":
		runInitFirecracker(ctx)
	default:
		fmt.Printf("Unknown terrarium command: %s\n", args[0])
		os.Exit(1)
	}
}

func runInitFirecracker(ctx context.Context) {
	fmt.Println("Initializing Firecracker MVP terrarium environment...")

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Printf("Error getting current directory: %v\n", err)
		os.Exit(1)
	}

	terrariumDir := filepath.Join(cwd, ".tendril", "terrarium")
	if err := os.MkdirAll(terrariumDir, 0755); err != nil {
		fmt.Printf("Error creating terrarium directory: %v\n", err)
		os.Exit(1)
	}

	kernelPath := filepath.Join(terrariumDir, "vmlinux.bin")
	if err := ensureVerifiedDownload(ctx, firecrackerKernelURL, firecrackerKernelSHA256, kernelPath, "kernel"); err != nil {
		fmt.Printf("Error obtaining kernel: %v\n", err)
		os.Exit(1)
	}

	busyboxPath := filepath.Join(terrariumDir, "busybox")
	if err := ensureVerifiedDownload(ctx, busyboxURL, busyboxSHA256, busyboxPath, "busybox"); err != nil {
		fmt.Printf("Error obtaining busybox: %v\n", err)
		os.Exit(1)
	}

	rootfsPath := filepath.Join(terrariumDir, "rootfs.ext4")
	if err := buildRootfs(ctx, busyboxPath, rootfsPath); err != nil {
		fmt.Printf("Error building rootfs: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\nFirecracker environment bootstrapped successfully!")
	fmt.Println("To use Firecracker, set the following environment variables (or add them to .env):")
	fmt.Printf("\nTENDRIL_TERRARIUM_PROVIDER=firecracker\n")
	fmt.Printf("TENDRIL_FC_KERNEL_PATH=%s\n", kernelPath)
	fmt.Printf("TENDRIL_FC_ROOTFS_PATH=%s\n", rootfsPath)
}

// buildRootfs assembles the guest root filesystem: sprout-agent as /init,
// busybox with its applet links under /bin, and the mount-point directories
// the guest expects. The image is populated with `mkfs.ext4 -d`, which reads
// a staging directory straight into the filesystem it creates — no loop
// mount, no root, and no privileged container. The previous implementation
// spent `docker run --privileged` to loop-mount the image, escalating to
// bootstrap the very isolation layer whose purpose is avoiding escalation.
func buildRootfs(ctx context.Context, busyboxPath, rootfsPath string) error {
	stagingDir, err := os.MkdirTemp("", "opentendril-rootfs-*")
	if err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	defer os.RemoveAll(stagingDir)

	fmt.Println("Compiling sprout-agent for linux/amd64...")
	initPath := filepath.Join(stagingDir, "init")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", initPath, "./cmd/sprout-agent")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("compile sprout-agent: %w", err)
	}
	if err := os.Chmod(initPath, 0o755); err != nil {
		return fmt.Errorf("chmod init: %w", err)
	}

	for _, dir := range []string{"bin", "dev", "proc", "sys", "tmp", "root"} {
		if err := os.MkdirAll(filepath.Join(stagingDir, dir), 0o755); err != nil {
			return fmt.Errorf("create staging %s: %w", dir, err)
		}
	}

	if err := copyFile(busyboxPath, filepath.Join(stagingDir, "bin", "busybox"), 0o755); err != nil {
		return fmt.Errorf("stage busybox: %w", err)
	}
	for _, applet := range busyboxApplets {
		if err := os.Symlink("busybox", filepath.Join(stagingDir, "bin", applet)); err != nil {
			return fmt.Errorf("link busybox applet %s: %w", applet, err)
		}
	}

	fmt.Println("Building ext4 rootfs image (unprivileged, via mkfs.ext4 -d)...")
	if err := os.RemoveAll(rootfsPath); err != nil {
		return fmt.Errorf("remove old rootfs: %w", err)
	}
	image, err := os.Create(rootfsPath)
	if err != nil {
		return fmt.Errorf("create rootfs image: %w", err)
	}
	if err := image.Truncate(100 * 1024 * 1024); err != nil {
		_ = image.Close()
		return fmt.Errorf("allocate rootfs image: %w", err)
	}
	if err := image.Close(); err != nil {
		return fmt.Errorf("close rootfs image: %w", err)
	}

	mkfsPath, err := lookPathMkfsExt4()
	if err != nil {
		return err
	}
	// -F: operate on a regular file without prompting; -d: populate the new
	// filesystem from the staging directory.
	mkfsCmd := exec.CommandContext(ctx, mkfsPath, "-F", "-q", "-d", stagingDir, rootfsPath)
	mkfsCmd.Stdout = os.Stdout
	mkfsCmd.Stderr = os.Stderr
	if err := mkfsCmd.Run(); err != nil {
		_ = os.Remove(rootfsPath)
		return fmt.Errorf("mkfs.ext4: %w", err)
	}

	fmt.Println("Rootfs built successfully!")
	return nil
}

// lookPathMkfsExt4 finds mkfs.ext4 in PATH, falling back to the sbin
// directories that are commonly absent from an unprivileged user's PATH even
// when e2fsprogs is installed.
func lookPathMkfsExt4() (string, error) {
	if path, err := exec.LookPath("mkfs.ext4"); err == nil {
		return path, nil
	}
	for _, candidate := range []string{"/usr/sbin/mkfs.ext4", "/sbin/mkfs.ext4"} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("mkfs.ext4 not found (install e2fsprogs)")
}

// ensureVerifiedDownload guarantees dest contains exactly the pinned bytes:
// an existing file is re-hashed (a stale or tampered copy fails, it is never
// trusted just for existing), and a fresh download is hashed before it is
// moved into place, so a mismatch leaves no usable file behind.
func ensureVerifiedDownload(ctx context.Context, url, wantSHA256, dest, label string) error {
	if _, err := os.Stat(dest); err == nil {
		gotSHA256, err := fileSHA256(dest)
		if err != nil {
			return fmt.Errorf("hash existing %s: %w", label, err)
		}
		if gotSHA256 == wantSHA256 {
			fmt.Printf("%s already present and verified, skipping download.\n", label)
			return nil
		}
		return fmt.Errorf("existing %s at %s has sha256 %s, want %s; refusing to use it — remove the file and re-run", label, dest, gotSHA256, wantSHA256)
	}

	fmt.Printf("Downloading %s...\n", label)
	partialPath := dest + ".partial"
	if err := downloadFile(ctx, url, partialPath); err != nil {
		_ = os.Remove(partialPath)
		return fmt.Errorf("download %s: %w", label, err)
	}

	gotSHA256, err := fileSHA256(partialPath)
	if err != nil {
		_ = os.Remove(partialPath)
		return fmt.Errorf("hash downloaded %s: %w", label, err)
	}
	if gotSHA256 != wantSHA256 {
		_ = os.Remove(partialPath)
		return fmt.Errorf("downloaded %s has sha256 %s, want %s; refusing to keep it", label, gotSHA256, wantSHA256)
	}

	if err := os.Rename(partialPath, dest); err != nil {
		_ = os.Remove(partialPath)
		return fmt.Errorf("move verified %s into place: %w", label, err)
	}
	fmt.Printf("%s downloaded and verified.\n", label)
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(src, dest string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func downloadFile(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
