SHELL := /bin/bash
IMG ?= vllm/vllm-openai:v0.30.0

.PHONY: 10-k3s 11-gpu-runtime 13-image 14-verify-gpu 13-vllm 15-smoke

# --- Stage 1: basic env setup---
10-k3s:
	@echo "=== Stage 1: basic env setup ==="
	bash ./scripts/10-k3s.sh

11-gpu-runtime:
	@echo "=== Stage 2: GPU runtime setup ==="
	bash ./scripts/11-gpu-runtime.sh

12-verify-gpu:
	@echo "=== Stage 3: verify GPU runtime ==="
	bash ./scripts/12-verify-gpu.sh

13-image:
	@echo "=== Stage 4: build image ==="
	docker pull $(IMG)
	docker save $(IMG) | sudo k3s ctr image import -

14-vllm:
	@echo "=== Stage 5: deploy vllm ==="
	kubectl apply -f deploy/manifests/vllm/vllm-qwen7b-awq.yaml

15-smoke:
	@echo "=== Stage 6: smoke test ==="
	python3 ./scripts/smoke_stream.py

20-baseline:
	@echo "=== baseline testing ==="
	bash ./scripts/20-baseline.sh