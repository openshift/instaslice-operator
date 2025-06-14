# Dynamic Accelerator Slicer (DAS) Operator

Dynamic Accelerator Slicer (DAS) provides on-demand partitioning of accelerators in Kubernetes clusters.
It currently includes a reference implementation for NVIDIA MIG but is designed to support other
partitioning technologies such as NVIDIA MPS, AMD or Intel GPUs.

## Project Goals

- Allocate accelerator slices when pods request them
- Maximize hardware utilization by releasing slices when pods finish
- Integrate with Kubernetes scheduling, quota and admission systems
- **TODO:** Expand list with additional goals or requirements



## Contributing

Contributions are welcome! Please open issues or pull requests. Ensure `go test ./...`
passes before submitting a patch.

## License

This project is licensed under the Apache 2.0 License.
