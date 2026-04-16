# AGENTS.md

This file provides guidance to AI agents - e.g. Claude Code - when working with code in this repository.

## Project Overview

Kubernetes controller and mutating webhook (Kubebuilder-based) that manages persistent IPAM (IP Address Management) claims for KubeVirt virtual machines. Implements [Kubernetes Network Plumbing Working Group multi-network specification v1.3](https://github.com/k8snetworkplumbingwg/multi-net-spec/tree/master/v1.3) for IPAM extensions, working with OVN-Kubernetes CNI.

## Build & Development Commands

```bash
make build              # Build bin/manager binary
make test               # Unit tests (envtest-based, excludes e2e)
make test-e2e           # E2E tests against Kind cluster (default timeout: 1h)
make lint               # Run golangci-lint
make lint-fix           # Run golangci-lint with auto-fix
make fmt                # go fmt
make vet                # go vet
make manifests          # Generate CRDs, webhooks, RBAC via controller-gen
make generate           # Generate DeepCopy methods via controller-gen
make vendor             # go mod tidy && go mod vendor
make check-vendoring    # Verify vendor consistency (used in CI)
make docker-build       # Build container image
make deploy IMG=<image> # Deploy to cluster via Kustomize
make run                # Run controller locally against current kubeconfig
```

### Local Cluster Development

```bash
make cluster-up         # Create Kind cluster with OVN-Kubernetes
make cluster-sync       # Rebuild image, push to local registry, deploy
make cluster-down       # Tear down Kind cluster
make test-e2e           # Run e2e tests (requires cluster-up + cluster-sync first)
```

### Running a Single Test

Tests use Ginkgo v2. To run a specific test:
```bash
# Unit test in a specific package
go test ./pkg/vmnetworkscontroller/ -v -run "TestName"
# Or with Ginkgo focus
ginkgo -v --focus "description" ./pkg/vmnetworkscontroller/

# E2e (requires running cluster)
ginkgo -v --focus "description" ./test/e2e/
```

## Architecture

Two Kubernetes controllers and one webhook, all registered in `cmd/main.go`:

- **VirtualMachineReconciler** (`pkg/vmnetworkscontroller/`) — watches KubeVirt VirtualMachine objects, creates/manages IPAMClaim lifecycle, handles VM deletion with finalizers.
- **VirtualMachineInstanceReconciler** (`pkg/vminetworkscontroller/`) — watches VirtualMachineInstance objects, manages IPAM claims for running instances.
- **Pod Mutation Webhook** (`pkg/ipamclaimswebhook/`) — mutates virt-launcher pods at `/mutate-v1-pod` to request persistent IPs by reading IPAM claims and annotating pods.

### Supporting Packages

- `pkg/claims/` — IPAM claim CRUD operations, labels, finalizers
- `pkg/config/` — NAD (NetworkAttachmentDefinition) config parsing, TLS configuration
- `pkg/ips/` — IPv4/IPv6 detection, subnet separation
- `pkg/udn/` — User-Defined Networks support

## Code Generation

Run `make manifests generate` after modifying kubebuilder markers (`+kubebuilder:rbac`, `+kubebuilder:webhook`, etc.). Generated artifacts include CRD manifests, RBAC roles, and DeepCopy methods. The `dist/install.yaml` must stay up-to-date (`make build-installer`); CI validates this.

## Testing

- **Unit tests**: Ginkgo v2 + Gomega with envtest (mock Kubernetes API server). Run with `make test`.
- **E2E tests**: Ginkgo v2 against a Kind cluster with OVN-Kubernetes. Located in `test/e2e/`. Test utilities and VM/NAD composition helpers are in `test/env/`.
- CI runs `make check-vendoring`, build, lint, unit tests, and e2e tests on every PR.

## Key Environment Variables

- `IMG` — container image reference (default: `ghcr.io/kubevirt/ipam-controller`)
- `E2E_TEST_TIMEOUT` — e2e test timeout (default: `1h`)
- `KUBECONFIG` — set to `.output/kubeconfig` when using `make cluster-up`
- `CONTAINER_TOOL` - set this variable to `podman` in case you're using that container runtime

## Container Image

Multi-stage build: Go builder → `gcr.io/distroless/static:nonroot`. Runs as non-root user 65532. Multi-platform support (amd64, arm64, s390x, ppc64le).

## Design guidelines

Run `make lint` to run the go linter against the codebase.

### Code Readability: Line of Sight

Write code that's easy to scan vertically:

1. Happy Path Left-Aligned: Keep the main execution path with minimal indentation
1. Early Returns: Exit as soon as conditions are met to reduce nesting
1. Avoid else-returns: Invert conditions to return early instead of using else blocks
1. Extract Functions: Break large functions into smaller, single-purpose functions

Example:

```golang
// Good
func Process(data string) error {
    if data == "" {
        return errors.New("empty data")
    }
    if !isValid(data) {
        return errors.New("invalid data")
    }
    // happy path continues here
    return doWork(data)
}

// Avoid
func Process(data string) error {
    if data != "" {
        if isValid(data) {
            return doWork(data)
        } else {
            return errors.New("invalid data")
        }
    } else {
        return errors.New("empty data")
    }
}
```

### Code Readability: Line Length

Limit line length to 120 characters whenever possible:

- Break long function calls, struct definitions, and statements into multiple lines
- Use appropriate indentation for continuation lines
- Prioritize readability over strict adherence when necessary

### Package Organization

#### Naming:

- Use descriptive, single-word names that convey purpose
- AVOID generic names: util, common, lib, misc, helpers
- Package name should be an "elevator pitch" for its functionality

#### File Structure:

- Name the primary file after the package (e.g., network.go in package network)
- Place public APIs and important types at the top of files
- Place helper functions at the bottom of files, after where they are used
    - This applies to ALL files (production code and tests)
    - Main/exported functions first, internal helpers last
    - In test files: test functions first, helper functions at the bottom
- Utility functions should be in separate files within the package

### Error Handling

- Type-Safe Checking: Use errors.Is and errors.As instead of string comparison
- Add Context: Wrap errors with fmt.Errorf and %w to preserve the error chain
- Propagate Context: Each layer should add meaningful context about what operation failed

Example:
```golang
if err := fetchData(id); err != nil {
    return fmt.Errorf("failed to fetch data for id %s: %w", id, err)
}
```

### Testing Guidelines

#### Validate public behavior, not internals

Test observable outcomes rather than private helpers.

```golang
// Good
result := webhook.Handle(ctx, req)
Expect(result.Allowed).To(BeTrue())

// Avoid
Expect(buildAnnotation(claim)).To(Equal("..."))
```

#### Cleanup in teardown, not in scenario flow

Put resource deletion in `AfterEach`/`DeferCleanup`, not inline in `It(...)`.

```golang
// Good
BeforeEach(func() {
    Expect(k8sClient.Create(ctx, obj)).To(Succeed())
    DeferCleanup(func() {
        Expect(k8sClient.Delete(ctx, obj)).To(Succeed())
    })
})

// Avoid
It("does something", func() {
    Expect(k8sClient.Create(ctx, obj)).To(Succeed())
    // ... test logic ...
    Expect(k8sClient.Delete(ctx, obj)).To(Succeed()) // cleanup never runs if a prior Expect fails
})
```

#### Positive assertions that describe expected success

Use `To(Succeed())` on the operation itself instead of capturing and checking errors separately.

```golang
// Good
Expect(k8sClient.Update(ctx, obj)).To(Succeed())

// Avoid
err := k8sClient.Update(ctx, obj)
Expect(err).NotTo(HaveOccurred())
```

#### Clear Ginkgo narrative that reads like behavior documentation

Use `When`/`Context`/`It` blocks whose descriptions form a readable sentence.

```golang
// Good
When("persistent IP is disabled on NAD", func() {
    It("does not create an IPAMClaim", func() { ... })
})

// Avoid
Describe("reconciler", func() {
    It("handles the case where persistent IP is disabled", func() { ... })
})
```

#### Explicit input types over boolean flags

Replace boolean mode parameters with named constants or types.

```golang
// Good
createClaim(ctx, ClaimModeDryRun)
createClaim(ctx, ClaimModeApply)

// Avoid
createClaim(ctx, true)  // what does true mean?
createClaim(ctx, false)
```

### Environment Variables:

- NEVER read environment variables from packages
- ALWAYS read them in main() function
- Pass values explicitly through function parameters or configuration structs

### Function Arguments:

- Use pointer arguments when the function needs to modify the argument
- Use value arguments for read-only parameters

