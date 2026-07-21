# Terrarium Terrariuming Architecture

OpenTendril executes untrusted, Sprout-generated code inside isolated environments called "Sprouts". To support a spectrum of security requirements—ranging from local rapid development to enterprise SaaS deployments—the Stem orchestrator implements the `TerrariumProvider` contract.

This architecture is colloquially known as **Terrarium**, providing isolated soil environments for our Sprouts to safely run.

## Supported Isolation Tiers

### 1. Docker (Default)
The Docker provider is the default execution environment. It offers strong container-level isolation using standard Linux namespaces and cgroups.

- **Use Case**: Local development, trusted private repositories.
- **Provider Name**: `docker`
- **Security Features**:
  - `--network=none` (by default, preventing outbound data exfiltration)
  - CAP_DROP ALL
  - strict PID and Memory limits
- **Pros**: Fastest boot time, native local tooling integration.
- **Cons**: Shares the host Linux kernel, theoretically vulnerable to container breakouts or kernel exploits.

### 2. gVisor
The gVisor provider enhances the Docker runtime by intercepting application system calls and routing them through a userspace kernel (Sentry).

- **Use Case**: Multi-tenant cloud environments, hardened local environments.
- **Provider Name**: `gvisor`
- **Security Features**:
  - Sycall interception (defense in depth against Linux kernel exploits).
  - Uses the `runsc` Docker runtime.
- **Pros**: Excellent balance of security and compatibility. Reuses Docker's ecosystem and networking layout.
- **Cons**: Slight performance overhead for syscall-heavy workloads.

### 3. Firecracker (Hardware MicroVMs)
The Firecracker provider delivers true hardware-level virtualization using KVM. It bypasses Docker entirely, launching each Sprout in a dedicated, minimal virtual machine in milliseconds.

- **Use Case**: High-security enterprise SaaS, running highly untrusted or malicious payloads.
- **Provider Name**: `firecracker`
- **Security Features**:
  - Full hardware virtualization (KVM).
  - Zero shared host kernel space.
  - Vsock communication (no network bridge required).
- **Pros**: The highest level of security (used by AWS Lambda and Fargate).
- **Cons**: Requires KVM support on the host. Slightly slower boot times than raw containers.

---

## Bootstrapping Firecracker

Unlike Docker, Firecracker requires a Linux kernel binary (`vmlinux`) and an `ext4` filesystem image acting as the root drive. 

To easily bootstrap a Firecracker environment, use the OpenTendril CLI:

```bash
tendril terrarium init-firecracker
```

This command automatically:
1. Downloads a compatible, uncompressed Linux kernel from the official Firecracker AWS CI bucket.
2. Compiles the `stoma` binary for `linux/amd64`.
3. Provisions a minimal Alpine Linux `ext4` rootfs and injects the `stoma` as the `init` process using a privileged Docker helper.

### Configuration

Once bootstrapped, configure your OpenTendril node to use Firecracker by exporting these variables:

```bash
export TENDRIL_TERRARIUM_PROVIDER=firecracker
export TENDRIL_FC_KERNEL_PATH=~/.tendril/terrarium/vmlinux.bin
export TENDRIL_FC_ROOTFS_PATH=~/.tendril/terrarium/rootfs.ext4
```
