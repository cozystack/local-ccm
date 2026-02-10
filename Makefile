.PHONY: image image-local-ccm image-nlc all

REGISTRY ?= ghcr.io/cozystack
TAG ?= latest
PUSH ?= 1
LOAD ?= 0
PLATFORM ?= linux/amd64,linux/arm64

BUILDX_ARGS := --provenance=false --push=$(PUSH) --load=$(LOAD) \
  --cache-to type=inline \
  $(if $(strip $(PLATFORM)),--platform=$(PLATFORM))

# Build both images
image: image-local-ccm image-nlc

# Build local-ccm image
image-local-ccm:
	docker buildx build . \
		--file Dockerfile \
		--tag $(REGISTRY)/local-ccm:$(TAG) \
		--cache-from type=registry,ref=$(REGISTRY)/local-ccm:latest \
		$(BUILDX_ARGS)
	export REPOSITORY="$(REGISTRY)/local-ccm" && \
	export TAG="$(TAG)" && \
	export IMAGE="$(REGISTRY)/local-ccm:$(TAG)" && \
	yq -i '.image.repository = strenv(REPOSITORY)' charts/local-ccm/values.yaml && \
	yq -i '.image.tag = strenv(TAG)' charts/local-ccm/values.yaml && \
	yq -i '.spec.template.spec.containers[0].image = strenv(IMAGE)' deploy/daemonset.yaml

# Build node-lifecycle-controller image
image-nlc:
	docker buildx build . \
		--file Dockerfile.node-lifecycle-controller \
		--tag $(REGISTRY)/node-lifecycle-controller:$(TAG) \
		--cache-from type=registry,ref=$(REGISTRY)/node-lifecycle-controller:latest \
		$(BUILDX_ARGS)
	export REPOSITORY="$(REGISTRY)/node-lifecycle-controller" && \
	export TAG="$(TAG)" && \
	yq -i '.nodeLifecycleController.image.repository = strenv(REPOSITORY)' charts/local-ccm/values.yaml && \
	yq -i '.nodeLifecycleController.image.tag = strenv(TAG)' charts/local-ccm/values.yaml
