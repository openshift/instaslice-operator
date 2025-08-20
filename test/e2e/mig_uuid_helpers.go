package e2e

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
)

func migUuidPodSpec() corev1.PodSpec {
	image := "nvidia/cuda:12.4.1-devel-ubuntu22.04"
	command := []string{"sh", "-c"}
	args := []string{`
echo "=== MIG UUID Verification Test (Pod: $HOSTNAME) ==="
echo "NVIDIA_VISIBLE_DEVICES=$NVIDIA_VISIBLE_DEVICES"
echo "CUDA_VISIBLE_DEVICES=$CUDA_VISIBLE_DEVICES"

cat > /tmp/print_mig_uuid.cu << 'EOF'
#include <cuda.h>
#include <cuda_runtime.h>
#include <stdio.h>
#include <stdlib.h>

__global__ void vecAdd(const float* A, const float* B, float* C, int N) {
    int i = blockIdx.x * blockDim.x + threadIdx.x;
    if (i < N) C[i] = A[i] + B[i];
}

int main() {
    int dev = 0;
    cudaSetDevice(dev);
    CUresult cr = cuInit(0);
    if (cr != CUDA_SUCCESS) {
        printf("ERROR: cuInit failed: %d\n", (int)cr);
        return 1;
    }
    CUdevice cudev;
    cr = cuDeviceGet(&cudev, dev);
    if (cr != CUDA_SUCCESS) {
        printf("ERROR: cuDeviceGet failed: %d\n", (int)cr);
        return 1;
    }
    CUuuid u2;
    cr = cuDeviceGetUuid_v2(&u2, cudev);
    if (cr != CUDA_SUCCESS) {
        printf("ERROR: cuDeviceGetUuid_v2 failed: %d\n", (int)cr);
        return 1;
    }
    printf("MIG_SLICE_UUID=MIG-");
    for (int i = 0; i < 16; i++) {
        printf("%02x", (unsigned char)u2.bytes[i]);
        if (i == 3 || i == 5 || i == 7 || i == 9) printf("-");
    }
    printf("\n");
    
    char* nvidia_visible = getenv("NVIDIA_VISIBLE_DEVICES");
    char* cuda_visible = getenv("CUDA_VISIBLE_DEVICES");
    printf("NVIDIA_VISIBLE_DEVICES=%s\n", nvidia_visible ? nvidia_visible : "NOT_SET");
    printf("CUDA_VISIBLE_DEVICES=%s\n", cuda_visible ? cuda_visible : "NOT_SET");
    printf("POD_HOSTNAME=%s\n", getenv("HOSTNAME"));
    
    const int N = 1 << 16;
    const size_t size = N * sizeof(float);
    float *h_A = (float*)malloc(size);
    float *h_B = (float*)malloc(size);
    float *h_C = (float*)malloc(size);
    if (!h_A || !h_B || !h_C) {
        printf("ERROR: Host allocation failed\n");
        return 1;
    }
    for (int i = 0; i < N; ++i) { h_A[i] = 1.0f; h_B[i] = 2.0f; }
    
    float *d_A = nullptr, *d_B = nullptr, *d_C = nullptr;
    if (cudaMalloc(&d_A, size) != cudaSuccess ||
        cudaMalloc(&d_B, size) != cudaSuccess ||
        cudaMalloc(&d_C, size) != cudaSuccess) {
        printf("ERROR: Device allocation failed\n");
        return 1;
    }
    cudaError_t err;
    err = cudaMemcpy(d_A, h_A, size, cudaMemcpyHostToDevice);
    if (err != cudaSuccess) { printf("ERROR: memcpy H2D A: %s\n", cudaGetErrorString(err)); return 1; }
    err = cudaMemcpy(d_B, h_B, size, cudaMemcpyHostToDevice);
    if (err != cudaSuccess) { printf("ERROR: memcpy H2D B: %s\n", cudaGetErrorString(err)); return 1; }
    int threads = 256; int blocks = (N + threads - 1) / threads;
    vecAdd<<<blocks, threads>>>(d_A, d_B, d_C, N);
    err = cudaGetLastError();
    if (err != cudaSuccess) {
        printf("ERROR: Kernel launch failed: %s\n", cudaGetErrorString(err));
        return 1;
    }
    err = cudaMemcpy(h_C, d_C, size, cudaMemcpyDeviceToHost);
    if (err != cudaSuccess) { printf("ERROR: memcpy D2H C: %s\n", cudaGetErrorString(err)); return 1; }
    bool ok = true;
    for (int i = 0; i < 10; ++i) {
        if (h_C[i] != 3.0f) { ok = false; break; }
    }
    printf("VECTOR_ADD_STATUS=%s\n", ok ? "OK" : "FAIL");
    
    cudaFree(d_A); cudaFree(d_B); cudaFree(d_C);
    free(h_A); free(h_B); free(h_C);
    return 0;
}
EOF

nvcc -o /tmp/print_mig_uuid /tmp/print_mig_uuid.cu -lcuda
/tmp/print_mig_uuid

while true; do
  echo "Pod $HOSTNAME is running with MIG slice access..."
  sleep 300
done
`}

	return corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:    "mig-uuid-printer",
				Image:   image,
				Command: command,
				Args:    args,
				Env: []corev1.EnvVar{
					{
						Name: "NODE_NAME",
						ValueFrom: &corev1.EnvVarSource{
							FieldRef: &corev1.ObjectFieldSelector{
								FieldPath: "spec.nodeName",
							},
						},
					},
				},
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceName("nvidia.com/mig-1g.5gb"): resource.MustParse("1"),
					},
				},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: ptr.To(false),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
				},
			},
		},
	}
}

func migUuidDualPodSpec() corev1.PodSpec {
	image := "nvidia/cuda:12.4.1-devel-ubuntu22.04"
	command := []string{"sh", "-c"}
	args := []string{`
echo "=== MIG UUID Dual-Slice Test (Pod: $HOSTNAME) ==="
echo "NVIDIA_VISIBLE_DEVICES=$NVIDIA_VISIBLE_DEVICES"
echo "CUDA_VISIBLE_DEVICES=$CUDA_VISIBLE_DEVICES"

cat > /tmp/print_all_mig_uuid.cu << 'EOF'
#include <cuda.h>
#include <cuda_runtime.h>
#include <stdio.h>
#include <stdlib.h>

__global__ void vecAdd(const float* A, const float* B, float* C, int N) {
    int i = blockIdx.x * blockDim.x + threadIdx.x;
    if (i < N) C[i] = A[i] + B[i];
}

static void print_uuid(const unsigned char bytes[16]) {
    for (int i = 0; i < 16; i++) {
        printf("%02x", bytes[i]);
        if (i == 3 || i == 5 || i == 7 || i == 9) printf("-");
    }
}

int main() {
    int count = 0;
    cudaError_t cerr = cudaGetDeviceCount(&count);
    if (cerr != cudaSuccess) {
        printf("ERROR: cudaGetDeviceCount failed: %s\n", cudaGetErrorString(cerr));
        return 1;
    }
    printf("CUDA_DEVICE_COUNT=%d\n", count);

    CUresult cr = cuInit(0);
    if (cr != CUDA_SUCCESS) {
        printf("ERROR: cuInit failed: %d\n", (int)cr);
        return 1;
    }

    for (int dev = 0; dev < count; ++dev) {
        cudaSetDevice(dev);
        CUdevice cudev;
        cr = cuDeviceGet(&cudev, dev);
        if (cr != CUDA_SUCCESS) {
            printf("ERROR: cuDeviceGet(%d) failed: %d\n", dev, (int)cr);
            continue;
        }
        CUuuid uuid;
        cr = cuDeviceGetUuid_v2(&uuid, cudev);
        if (cr != CUDA_SUCCESS) {
            printf("ERROR: cuDeviceGetUuid_v2(%d) failed: %d\n", dev, (int)cr);
            continue;
        }
        printf("MIG_DEVICE_%d_UUID=MIG-", dev);
        print_uuid((const unsigned char*)uuid.bytes);
        printf("\n");

        const int N = 1 << 16;
        const size_t size = N * sizeof(float);
        float *h_A = (float*)malloc(size);
        float *h_B = (float*)malloc(size);
        float *h_C = (float*)malloc(size);
        if (!h_A || !h_B || !h_C) {
            printf("ERROR: Host allocation failed on device %d\n", dev);
            return 1;
        }
        for (int i = 0; i < N; ++i) { h_A[i] = 1.0f; h_B[i] = 2.0f; }
        float *d_A = nullptr, *d_B = nullptr, *d_C = nullptr;
        if (cudaMalloc(&d_A, size) != cudaSuccess ||
            cudaMalloc(&d_B, size) != cudaSuccess ||
            cudaMalloc(&d_C, size) != cudaSuccess) {
            printf("ERROR: Device allocation failed on device %d\n", dev);
            return 1;
        }
        cerr = cudaMemcpy(d_A, h_A, size, cudaMemcpyHostToDevice);
        if (cerr != cudaSuccess) { printf("ERROR: memcpy H2D A on dev %d: %s\n", dev, cudaGetErrorString(cerr)); return 1; }
        cerr = cudaMemcpy(d_B, h_B, size, cudaMemcpyHostToDevice);
        if (cerr != cudaSuccess) { printf("ERROR: memcpy H2D B on dev %d: %s\n", dev, cudaGetErrorString(cerr)); return 1; }
        int threads = 256; int blocks = (N + threads - 1) / threads;
        vecAdd<<<blocks, threads>>>(d_A, d_B, d_C, N);
        cerr = cudaGetLastError();
        if (cerr != cudaSuccess) { printf("ERROR: Kernel launch on dev %d: %s\n", dev, cudaGetErrorString(cerr)); return 1; }
        cerr = cudaMemcpy(h_C, d_C, size, cudaMemcpyDeviceToHost);
        if (cerr != cudaSuccess) { printf("ERROR: memcpy D2H C on dev %d: %s\n", dev, cudaGetErrorString(cerr)); return 1; }
        bool ok = true; for (int i = 0; i < 10; ++i) { if (h_C[i] != 3.0f) { ok = false; break; } }
        printf("VECTOR_ADD_STATUS_DEV_%d=%s\n", dev, ok ? "OK" : "FAIL");
        cudaFree(d_A); cudaFree(d_B); cudaFree(d_C);
        free(h_A); free(h_B); free(h_C);
    }

    const char* nvv = getenv("NVIDIA_VISIBLE_DEVICES");
    const char* cvv = getenv("CUDA_VISIBLE_DEVICES");
    printf("NVIDIA_VISIBLE_DEVICES=%s\n", nvv ? nvv : "NOT_SET");
    printf("CUDA_VISIBLE_DEVICES=%s\n", cvv ? cvv : "NOT_SET");
    printf("POD_HOSTNAME=%s\n", getenv("HOSTNAME"));
    return 0;
}
EOF

nvcc -o /tmp/print_all_mig_uuid /tmp/print_all_mig_uuid.cu -lcuda
/tmp/print_all_mig_uuid

while true; do
  echo "Pod $HOSTNAME is running with two MIG slices..."
  sleep 300
done
`}

	return corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:    "mig-uuid-printer",
				Image:   image,
				Command: command,
				Args:    args,
				Env: []corev1.EnvVar{
					{
						Name: "NODE_NAME",
						ValueFrom: &corev1.EnvVarSource{
							FieldRef: &corev1.ObjectFieldSelector{
								FieldPath: "spec.nodeName",
							},
						},
					},
				},
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceName("nvidia.com/mig-1g.5gb"): resource.MustParse("2"),
					},
				},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: ptr.To(false),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
				},
			},
		},
	}
}

func migUuidMulticontainerPodSpec() corev1.PodSpec {
	containerA := corev1.Container{
		Name:  "gpu-a",
		Image: "nvidia/cuda:12.4.1-devel-ubuntu22.04",
		Command: []string{"sh", "-c"},
		Args: []string{`
echo "=== Multi-Container MIG Test (Container: gpu-a, Pod: $HOSTNAME) ==="
echo "NVIDIA_VISIBLE_DEVICES=$NVIDIA_VISIBLE_DEVICES"
echo "CUDA_VISIBLE_DEVICES=$CUDA_VISIBLE_DEVICES"

cat > /tmp/print_mig_uuid.cu << 'EOF'
#include <cuda.h>
#include <cuda_runtime.h>
#include <stdio.h>
#include <stdlib.h>

__global__ void vecAdd(const float* A, const float* B, float* C, int N) {
    int i = blockIdx.x * blockDim.x + threadIdx.x;
    if (i < N) C[i] = A[i] + B[i];
}

static void print_uuid(const unsigned char bytes[16]) {
    for (int i = 0; i < 16; i++) {
        printf("%02x", bytes[i]);
        if (i == 3 || i == 5 || i == 7 || i == 9) printf("-");
    }
}

int main() {
    int count = 0;
    cudaError_t cerr = cudaGetDeviceCount(&count);
    if (cerr != cudaSuccess) { printf("ERROR: cudaGetDeviceCount: %s\n", cudaGetErrorString(cerr)); return 1; }
    printf("CUDA_DEVICE_COUNT=%d\n", count);

    int dev = 0;
    cudaSetDevice(dev);

    CUresult cr = cuInit(0);
    if (cr != CUDA_SUCCESS) { printf("ERROR: cuInit: %d\n", (int)cr); return 1; }
    CUdevice cudev; cr = cuDeviceGet(&cudev, dev);
    if (cr != CUDA_SUCCESS) { printf("ERROR: cuDeviceGet: %d\n", (int)cr); return 1; }
    CUuuid uuid; cr = cuDeviceGetUuid_v2(&uuid, cudev);
    if (cr != CUDA_SUCCESS) { printf("ERROR: cuDeviceGetUuid_v2: %d\n", (int)cr); return 1; }
    printf("MIG_SLICE_UUID=MIG-"); print_uuid((const unsigned char*)uuid.bytes); printf("\n");

    const int N = 1 << 16; const size_t sz = N * sizeof(float);
    float *hA=(float*)malloc(sz), *hB=(float*)malloc(sz), *hC=(float*)malloc(sz);
    for (int i=0;i<N;++i){hA[i]=1.0f;hB[i]=2.0f;}
    float *dA=nullptr,*dB=nullptr,*dC=nullptr;
    if (cudaMalloc(&dA,sz)!=cudaSuccess||cudaMalloc(&dB,sz)!=cudaSuccess||cudaMalloc(&dC,sz)!=cudaSuccess){printf("ERROR: cudaMalloc\n");return 1;}
    cudaMemcpy(dA,hA,sz,cudaMemcpyHostToDevice); cudaMemcpy(dB,hB,sz,cudaMemcpyHostToDevice);
    int threads=256, blocks=(N+threads-1)/threads; vecAdd<<<blocks,threads>>>(dA,dB,dC,N);
    cerr = cudaGetLastError(); if (cerr!=cudaSuccess){printf("ERROR: kernel: %s\n", cudaGetErrorString(cerr)); return 1;}
    cudaMemcpy(hC,dC,sz,cudaMemcpyDeviceToHost);
    bool ok=true; for(int i=0;i<10;++i){ if(hC[i]!=3.0f){ok=false;break;} }
    printf("VECTOR_ADD_STATUS=%s\n", ok?"OK":"FAIL");
    cudaFree(dA); cudaFree(dB); cudaFree(dC); free(hA); free(hB); free(hC);
    return 0;
}
EOF

nvcc -o /tmp/print_mig_uuid /tmp/print_mig_uuid.cu -lcuda
/tmp/print_mig_uuid

while true; do echo "gpu-a running..."; sleep 300; done
`},
		Env: []corev1.EnvVar{
			{
				Name: "NODE_NAME",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "spec.nodeName",
					},
				},
			},
		},
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceName("nvidia.com/mig-1g.5gb"): resource.MustParse("1"),
			},
		},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
	}

	containerB := corev1.Container{
		Name:  "gpu-b",
		Image: "nvidia/cuda:12.4.1-devel-ubuntu22.04",
		Command: []string{"sh", "-c"},
		Args: []string{`
echo "=== Multi-Container MIG Test (Container: gpu-b, Pod: $HOSTNAME) ==="
echo "NVIDIA_VISIBLE_DEVICES=$NVIDIA_VISIBLE_DEVICES"
echo "CUDA_VISIBLE_DEVICES=$CUDA_VISIBLE_DEVICES"

cat > /tmp/print_mig_uuid.cu << 'EOF'
#include <cuda.h>
#include <cuda_runtime.h>
#include <stdio.h>
#include <stdlib.h>

__global__ void vecAdd(const float* A, const float* B, float* C, int N) {
    int i = blockIdx.x * blockDim.x + threadIdx.x;
    if (i < N) C[i] = A[i] + B[i];
}

static void print_uuid(const unsigned char bytes[16]) {
    for (int i = 0; i < 16; i++) {
        printf("%02x", bytes[i]);
        if (i == 3 || i == 5 || i == 7 || i == 9) printf("-");
    }
}

int main() {
    int count = 0;
    cudaError_t cerr = cudaGetDeviceCount(&count);
    if (cerr != cudaSuccess) { printf("ERROR: cudaGetDeviceCount: %s\n", cudaGetErrorString(cerr)); return 1; }
    printf("CUDA_DEVICE_COUNT=%d\n", count);

    int dev = 0;
    cudaSetDevice(dev);

    CUresult cr = cuInit(0);
    if (cr != CUDA_SUCCESS) { printf("ERROR: cuInit: %d\n", (int)cr); return 1; }
    CUdevice cudev; cr = cuDeviceGet(&cudev, dev);
    if (cr != CUDA_SUCCESS) { printf("ERROR: cuDeviceGet: %d\n", (int)cr); return 1; }
    CUuuid uuid; cr = cuDeviceGetUuid_v2(&uuid, cudev);
    if (cr != CUDA_SUCCESS) { printf("ERROR: cuDeviceGetUuid_v2: %d\n", (int)cr); return 1; }
    printf("MIG_SLICE_UUID=MIG-"); print_uuid((const unsigned char*)uuid.bytes); printf("\n");

    const int N = 1 << 16; const size_t sz = N * sizeof(float);
    float *hA=(float*)malloc(sz), *hB=(float*)malloc(sz), *hC=(float*)malloc(sz);
    for (int i=0;i<N;++i){hA[i]=1.0f;hB[i]=2.0f;}
    float *dA=nullptr,*dB=nullptr,*dC=nullptr;
    if (cudaMalloc(&dA,sz)!=cudaSuccess||cudaMalloc(&dB,sz)!=cudaSuccess||cudaMalloc(&dC,sz)!=cudaSuccess){printf("ERROR: cudaMalloc\n");return 1;}
    cudaMemcpy(dA,hA,sz,cudaMemcpyHostToDevice); cudaMemcpy(dB,hB,sz,cudaMemcpyHostToDevice);
    int threads=256, blocks=(N+threads-1)/threads; vecAdd<<<blocks,threads>>>(dA,dB,dC,N);
    cerr = cudaGetLastError(); if (cerr!=cudaSuccess){printf("ERROR: kernel: %s\n", cudaGetErrorString(cerr)); return 1;}
    cudaMemcpy(hC,dC,sz,cudaMemcpyDeviceToHost);
    bool ok=true; for(int i=0;i<10;++i){ if(hC[i]!=3.0f){ok=false;break;} }
    printf("VECTOR_ADD_STATUS=%s\n", ok?"OK":"FAIL");
    cudaFree(dA); cudaFree(dB); cudaFree(dC); free(hA); free(hB); free(hC);
    return 0;
}
EOF

nvcc -o /tmp/print_mig_uuid /tmp/print_mig_uuid.cu -lcuda
/tmp/print_mig_uuid

while true; do echo "gpu-b running..."; sleep 300; done
`},
		Env: []corev1.EnvVar{
			{
				Name: "NODE_NAME",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "spec.nodeName",
					},
				},
			},
		},
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceName("nvidia.com/mig-1g.5gb"): resource.MustParse("1"),
			},
		},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
	}

	return corev1.PodSpec{
		Containers: []corev1.Container{containerA, containerB},
	}
}

func verifyMigUuidInLogs(ctx context.Context, namespace, podName, containerName string) (string, error) {
	var req *rest.Request
	if containerName != "" {
		req = kubeClient.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{Container: containerName})
	} else {
		req = kubeClient.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{})
	}
	
	out, err := req.Do(ctx).Raw()
	if err != nil {
		return "", err
	}

	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "MIG_SLICE_UUID=") {
			return strings.TrimPrefix(line, "MIG_SLICE_UUID="), nil
		}
	}
	return "", fmt.Errorf("MIG_SLICE_UUID not found in logs")
}

func verifyVectorAddSuccess(ctx context.Context, namespace, podName, containerName string) (bool, error) {
	var req *rest.Request
	if containerName != "" {
		req = kubeClient.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{Container: containerName})
	} else {
		req = kubeClient.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{})
	}
	
	out, err := req.Do(ctx).Raw()
	if err != nil {
		return false, err
	}

	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "VECTOR_ADD_STATUS=") {
			status := strings.TrimPrefix(line, "VECTOR_ADD_STATUS=")
			return status == "OK", nil
		}
	}
	return false, fmt.Errorf("VECTOR_ADD_STATUS not found in logs")
}