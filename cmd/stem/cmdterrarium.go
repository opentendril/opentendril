package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
)

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
	if _, err := os.Stat(kernelPath); os.IsNotExist(err) {
		fmt.Println("Downloading Firecracker-compatible Linux kernel...")
		if err := downloadFile("https://s3.amazonaws.com/spec.ccfc.min/img/quickstart_guide/x86_64/kernels/vmlinux.bin", kernelPath); err != nil {
			fmt.Printf("Error downloading kernel: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Kernel downloaded successfully.")
	} else {
		fmt.Println("Kernel already exists, skipping download.")
	}

	agentPath := filepath.Join(terrariumDir, "sprout-agent")
	fmt.Println("Compiling sprout-agent for linux/amd64...")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", agentPath, "./cmd/sprout-agent")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("Error compiling sprout-agent: %v\n", err)
		os.Exit(1)
	}

	rootfsPath := filepath.Join(terrariumDir, "rootfs.ext4")
	fmt.Println("Building Alpine ext4 rootfs (requires Docker privilege for loop mount)...")

	dockerScript := `
set -e
echo "Installing e2fsprogs..."
apk add -q e2fsprogs
echo "Allocating 100MB disk image..."
dd if=/dev/zero of=/out/rootfs.ext4 bs=1M count=100 status=none
echo "Formatting as ext4..."
mkfs.ext4 -q /out/rootfs.ext4
echo "Mounting disk image..."
mkdir -p /mnt
mount -o loop /out/rootfs.ext4 /mnt
echo "Setting up rootfs..."
cp /out/sprout-agent /mnt/init
chmod +x /mnt/init
mkdir -p /mnt/dev /mnt/proc /mnt/sys /mnt/tmp
umount /mnt
echo "Rootfs built successfully!"
`
	dockerCmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--privileged", "-v", fmt.Sprintf("%s:/out", terrariumDir), "alpine:latest", "sh", "-c", dockerScript)
	dockerCmd.Stdout = os.Stdout
	dockerCmd.Stderr = os.Stderr
	if err := dockerCmd.Run(); err != nil {
		fmt.Printf("Error building rootfs via Docker: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\nFirecracker environment bootstrapped successfully!")
	fmt.Println("To use Firecracker, set the following environment variables (or add them to .env):")
	fmt.Printf("\nTENDRIL_TERRARIUM_PROVIDER=firecracker\n")
	fmt.Printf("TENDRIL_FC_KERNEL_PATH=%s\n", kernelPath)
	fmt.Printf("TENDRIL_FC_ROOTFS_PATH=%s\n", rootfsPath)
}

func downloadFile(url, dest string) error {
	resp, err := http.Get(url)
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
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}
