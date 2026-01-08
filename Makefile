.PHONY: image
REGISTRY ?= ghcr.io/cozystack
TAG ?= latest
PUSH ?= 1
LOAD ?= 0
PLATFORM ?= linux/amd64,linux/arm64

BUILDX_ARGS := --provenance=false --push=$(PUSH) --load=$(LOAD) \
  --cache-from type=registry,ref=$(REGISTRY)/local-ccm:latest \
  --cache-to type=inline \
  $(if $(strip $(PLATFORM)),--platform=$(PLATFORM))

image:
	docker buildx build . \
		--tag $(REGISTRY)/local-ccm:$(TAG) \
		$(BUILDX_ARGS)
	export REPOSITORY="$(REGISTRY)/local-ccm" && \
	export TAG="$(TAG)" && \
	export IMAGE="$(REGISTRY)/local-ccm:$(TAG)" && \
	yq -i '.image.repository = strenv(REPOSITORY)' charts/local-ccm/values.yaml && \
	yq -i '.image.tag = strenv(TAG)' charts/local-ccm/values.yaml && \
	yq -i '.spec.template.spec.containers[0].image = strenv(IMAGE)' deploy/daemonset.yaml
