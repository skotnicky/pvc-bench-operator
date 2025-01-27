# PVC Bench Operator

The PVC Bench Operator is a Kubernetes operator designed to streamline the benchmarking of Persistent Volume Claims (PVCs) within Kubernetes clusters. It automates the deployment and management of benchmarking tools, enabling users to efficiently assess the performance of their storage solutions.

## Features

- **Automated Benchmark Deployment**: Simplifies the setup of benchmarking tools for PVCs.
- **Custom Resource Definitions (CRDs)**: Introduces CRDs to manage benchmark configurations.
- **Scalability**: Supports benchmarking across multiple PVCs simultaneously.

## Prerequisites

Before deploying the PVC Bench Operator, ensure you have the following:

- **Go**: Version 1.23.0 or higher.
- **Docker**: Version 17.03 or higher.
- **Kubectl**: Version 1.11.3 or higher.
- **Kubernetes Cluster**: Access to a Kubernetes cluster running version 1.11.3 or higher.

## Installation

1. **Clone the Repository**:

   ```bash
   git clone https://github.com/skotnicky/pvc-bench-operator.git
   cd pvc-bench-operator
   ```

2. **Build and Push the Docker Image**:

   Replace `<your-registry>` with your Docker registry and `<tag>` with your desired image tag.

   ```bash
   make docker-build docker-push IMG=<your-registry>/pvc-bench-operator:<tag>
   ```

3. **Deploy the Operator to Your Kubernetes Cluster**:

   ```bash
   make deploy IMG=<your-registry>/pvc-bench-operator:<tag>
   ```

## Using the `/dist` Directory

The `/dist` directory contains pre-built manifests and helper files to facilitate quick deployment and testing of the PVC Bench Operator. Here's how to use it:

1. **Navigate to the ********************`/dist`******************** Directory**:

   ```bash
   cd dist
   ```

2. **Apply Pre-Built Kubernetes Manifests**:

   Use the `install.yaml` manifest to deploy the operator and its associated resources directly:

   ```bash
   kubectl apply -f install.yaml
   ```

   This will deploy the PVC Bench Operator using the pre-built configuration provided in the `/dist` directory.

3. **Customize Resources**:

   If needed, edit the `install.yaml` file in the `/dist` directory to suit your environment:

   ```bash
   vim install.yaml
   ```

   Adjust any configuration options, such as namespace, image tags, or resource limits, before applying the manifest.

4. **Verify Deployment**:

   Check the status of the operator to ensure it's running correctly:

   ```bash
   kubectl get pods -n <operator-namespace>
   ```

## Usage

Once deployed, you can define a `PVCBenchmark` custom resource to specify the PVCs you wish to benchmark. The operator will handle the deployment of benchmarking pods and collect performance metrics.

For detailed configuration options and examples, please refer to the [examples](https://github.com/skotnicky/pvc-bench-operator/tree/main/examples) directory in the repository.

## Get Results

After running a benchmark, you can retrieve the results using the following commands:

1. **List Benchmark Resources**:

   ```bash
   kubectl get pvcbenchmarks -n <operator-namespace>
   ```

   This will display the benchmark resources managed by the operator.

2. **Describe a Benchmark Resource**:

   ```bash
   kubectl describe pvcbenchmark <benchmark-name> -n <operator-namespace>
   ```

   This provides detailed information about the benchmark, including its status and results.

3. **Check Pod Logs**:

   For more granular details, check the logs of the benchmarking pods:

   ```bash
   kubectl logs <pod-name> -n <operator-namespace>
   ```

   Replace `<pod-name>` with the name of the pod running the benchmark.

## Contributing

Contributions are welcome! Please open an issue or submit a pull request with your improvements or bug fixes.

## License

This project is licensed under the Apache 2.0 License. See the [LICENSE](https://github.com/skotnicky/pvc-bench-operator/blob/main/LICENSE) file for details.

---
