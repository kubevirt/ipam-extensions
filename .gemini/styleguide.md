# Code Review Style Guide

This is a Kubebuilder-based Kubernetes controller and mutating webhook that manages
persistent IPAM claims for KubeVirt virtual machines. It implements the multi-network
de-facto standard v1.3 for IPAM extensions, working with OVN-Kubernetes CNI.

## Architecture

- VirtualMachineReconciler (`pkg/vmnetworkscontroller/`) — watches KubeVirt VM objects,
  creates/manages IPAMClaim lifecycle, handles VM deletion with finalizers.
- VirtualMachineInstanceReconciler (`pkg/vminetworkscontroller/`) — watches VMI objects,
  manages IPAM claims for running instances.
- Pod Mutation Webhook (`pkg/ipamclaimswebhook/`) — mutates virt-launcher pods to request
  persistent IPs by reading IPAM claims and annotating pods.

## Code Readability: Line of Sight

Prefer code that reads vertically with minimal nesting:

- Keep the happy path left-aligned with minimal indentation.
- Use early returns to exit as soon as conditions are met.
- Avoid else-returns: invert conditions and return early instead.
- Break large functions into smaller, single-purpose functions.

Prefer:

```go
func Process(data string) error {
    if data == "" {
        return errors.New("empty data")
    }
    if !isValid(data) {
        return errors.New("invalid data")
    }
    return doWork(data)
}
```

Over deeply nested if-else blocks.

## Line Length

Lines should not exceed 120 characters. Flag lines that are significantly longer.

## Package Organization

- Package names must be descriptive and single-word — avoid `util`, `common`, `lib`, `misc`, `helpers`.
- Primary file should be named after the package (e.g., `network.go` in package `network`).
- In all files: exported/main functions first, internal helpers at the bottom.
- This applies to test files too: test functions first, helper functions at the bottom.

## Error Handling

- Use `errors.Is` and `errors.As` for error type checking — never compare error strings.
- Wrap errors with `fmt.Errorf` and `%w` to preserve the error chain.
- Each layer should add context about what operation failed.

## Dependency Management

- Environment variables must only be read in `main()` and passed explicitly via parameters
  or config structs. Flag any package that reads `os.Getenv` or `os.LookupEnv` directly.
- Use pointer arguments only when the function needs to modify the argument.
- Use value arguments for read-only parameters.

## Security

- Apply the principle of least privilege for RBAC markers and service account permissions.
- Validate all input at system boundaries (user input, external API responses).

## Testing

- Unit tests use Ginkgo v2 + Gomega with envtest. New logic should have test coverage.
- Flag test files that place helper functions before the test functions (wrong order).

## Code Generation

- If kubebuilder markers (`+kubebuilder:rbac`, `+kubebuilder:webhook`, etc.) are modified,
  remind the author to run `make manifests generate`.
- If `dist/install.yaml` may be affected, remind the author to run `make build-installer`.

## Vendor

- If `go.mod` or `go.sum` are changed, the vendor directory must also be updated
  (`make vendor`). CI validates this with `make check-vendoring`.
