SHELL := /bin/bash
BUILD_DIR := _build
LOAD_OR_DEFAULT = $(shell cat $(BUILD_DIR)/$(1) 2>/dev/null || echo "$(2)")

# input variables
GIT_REF    ?= $(call LOAD_OR_DEFAULT,GIT_REF,v0.7.2-v1.1.5)
GIT_URL    ?= $(call LOAD_OR_DEFAULT,GIT_URL,https://github.com/mesosphere/kubernetes.git)
DOCKER_ORG ?= $(call LOAD_OR_DEFAULT,DOCKER_ORG,mesosphere)
KUBE_ROOT  ?= $(call LOAD_OR_DEFAULT,KUBE_ROOT,)
SUDO       ?= $(call LOAD_OR_DEFAULT,SUDO,)
POSTFIX    ?= $(call LOAD_OR_DEFAULT,POSTFIX,-alpha)

# compute VERSION string depending on whether
# - the given GIT_REF_MINOR tag is on the current HEAD and is not dirty
# - the BUILD_DIR does not exist yet.
# NOTE: Do not persist the VERSION. It must be re-computed on every make run.
GIT_REF_MINOR = $(lastword $(subst -, ,$(GIT_REF)))
GIT_REF_MAJOR = $(firstword $(subst -, ,$(GIT_REF)))
ifeq ($(USER),)
        USER := nobody
endif
ifneq ($(wildcard $(BUILD_DIR)),)
	UNCLEAN_INFIX := -unclean
endif
ifeq ($(KUBE_ROOT),)
	VERSION := $(USER)-$(shell git describe --match $(GIT_REF_MAJOR) --always --tags --dirty)$(UNCLEAN_INFIX)-$(GIT_REF_MINOR)
else
	VERSION := $(USER)-$(GIT_REF)-dev
endif

# compute the Docker image tag
# NOTE: As VERSION, do not persist DOCKER_IMAGE
DOCKER_REPO = $(DOCKER_ORG)/kubernetes
DOCKER_IMAGE = $(DOCKER_REPO):$(VERSION)$(POSTFIX)
DOCKER_IMAGE_FILE = $(BUILD_DIR)/built-docker-image

# kubernetes binaries distributed in the Docker container
ifeq ($(KUBE_ROOT),)
	K8S_ROOT = $(BUILD_DIR)/kubernetes
else
	K8S_ROOT = $(KUBE_ROOT)
endif
OS_EXT = $(if $(filter $(1),windows),.exe,)
K8S_BINARIES_SOURCE_DIR = $(K8S_ROOT)/_output/dockerized/bin/$(1)/amd64
K8S_KUBECTL_OSES = linux windows darwin
K8S_KUBECTL_SOURCE = $(foreach OS,$(K8S_KUBECTL_OSES),$(call K8S_BINARIES_SOURCE_DIR,$(OS))/kubectl$(call OS_EXT,$(OS)))
K8S_KUBECTL_DEST = $(foreach OS,$(K8S_KUBECTL_OSES),$(BUILD_DIR)/$(OS)/kubectl$(call OS_EXT,$(OS)).bz2)
K8S_KUBECTL_META = $(BUILD_DIR)/kubectl-meta.json
K8S_BINARIES_SOURCE = $(K8S_KUBECTL_SOURCE) $(addsuffix /km,$(call K8S_BINARIES_SOURCE_DIR,linux))
K8S_BINARIES_DEST = $(BUILD_DIR)/linux/km $(K8S_KUBECTL_DEST)

# etcd distributed in the Docker container
ETCD_IMAGE = etcd
ETCD_TAG = 2.0.12
ETCD_TAR = $(ETCD_IMAGE)-v$(ETCD_TAG)-linux-amd64.tar.gz
ETCD_BINARIES = etcd etcdctl
ETCD_BINARIES_SOURCE_DIR = $(BUILD_DIR)/$(ETCD_IMAGE)-v$(ETCD_TAG)-linux-amd64
ETCD_BINARIES_DEST = $(addprefix $(BUILD_DIR)/,$(ETCD_BINARIES))
ETCD_SOURCE = https://github.com/coreos/etcd/releases/download/v$(ETCD_TAG)/$(ETCD_TAR)

# kubernetes addons: kube-dns and kube-ui
KUBE_DNS_TEMPLATES = $(BUILD_DIR)/skydns-rc.yaml.in $(BUILD_DIR)/skydns-svc.yaml.in
KUBE_DNS_TEMPLATES_SRC = $(patsubst $(BUILD_DIR)/%,${K8S_ROOT}/cluster/addons/dns/%,$(KUBE_DNS_TEMPLATES))

KUBE_UI_TEMPLATES = $(BUILD_DIR)/kube-ui-rc.yaml $(BUILD_DIR)/kube-ui-svc.yaml
KUBE_UI_TEMPLATES_SRC = $(patsubst $(BUILD_DIR)/%,${K8S_ROOT}/cluster/addons/kube-ui/%,$(KUBE_UI_TEMPLATES))

# github-release utility to upload release assets to Github
GITHUB_RELEASE = $(shell pwd)/$(BUILD_DIR)/go/bin/github-release

# meta targets
.PHONY: clean banner build check_dirty checkout clone test push deps persist_variables delete_docker_image_file check_github_token github-release-assets
.INTERMEDIATE: build_kubernetes copy_kubernetes_binaries download_etcd

all: build

clean:
	$(SUDO) rm -rf $(BUILD_DIR)

banner:
	@if [ -n "$(KUBE_ROOT)" ]; then \
		echo "KUBE_ROOT=$(KUBE_ROOT)"; \
	else \
		echo "GIT_URL=$(GIT_URL)" && \
		echo "GIT_REF=$(GIT_REF)"; \
	fi
	@echo "VERSION=$(VERSION)"
	@echo "DOCKER_IMAGE=$(DOCKER_IMAGE)"
	@echo

$(BUILD_DIR):
	mkdir -p $(BUILD_DIR)

# persist all input variables such that they are available for non-build targets
persist_variables: $(BUILD_DIR)
	@echo "$(GIT_REF)" > $(BUILD_DIR)/GIT_REF
	@echo "$(GIT_URL)" > $(BUILD_DIR)/GIT_URL
	@echo "$(DOCKER_ORG)" > $(BUILD_DIR)/DOCKER_ORG
	@echo "$(KUBE_ROOT)" > $(BUILD_DIR)/KUBE_ROOT
	@echo "$(SUDO)" > $(BUILD_DIR)/SUDO
	@echo "$(POSTFIX)" > $(BUILD_DIR)/POSTFIX

# clone the given kubernetes git ref
clone: $(BUILD_DIR)
	@cd $(BUILD_DIR) && \
	if [ ! -d "kubernetes" ]; then \
		git clone --branch $(GIT_REF) $(GIT_URL) kubernetes && \
		touch rebuild; \
	else \
		cd kubernetes && \
		PREV=$$(git rev-parse HEAD) && \
		git fetch && git checkout $(GIT_REF) && \
		if [ "$$PREV" != "$$(git rev-parse HEAD)" ]; then touch ../rebuild; fi; \
	fi

# behave differently for a set or unset KUBE_ROOT
ifeq ($(KUBE_ROOT),)
checkout: clone

# check that everything in the repo is commited
check_dirty:
	@cd $(BUILD_DIR)/kubernetes && \
	if [ -n "$$(git status --porcelain)" ]; then \
		echo "unclean $(BUILD_DIR)/kubernetes checkout" && \
		exit 1; \
	fi

# cross-compile the kubernetes binaries inside Docker
build_kubernetes: $(BUILD_DIR)/rebuild
	@rm -f $(BUILD_DIR)/.version
	cd "${K8S_ROOT}" && KUBERNETES_CONTRIB=mesos build/run.sh hack/build-cross.sh
	@mkdir -p $(BUILD_DIR) && echo "$(VERSION)" > $(BUILD_DIR)/.version
else
checkout:
	@test -d $(KUBE_ROOT)
check_dirty:
build_kubernetes:
	@echo "Build linux, windows and darwin amd64 binaries in ${K8S_ROOT}:"
	@echo
	@echo "    KUBERNETES_CONTRIB=mesos build/run.sh hack/build-cross.sh"
	@echo
	@exit 1

$(BUILD_DIR)/.version: $(BUILD_DIR)
	@echo "$(VERSION)" > $(BUILD_DIR)/.version
endif

$(K8S_BINARIES_SOURCE): build_kubernetes
$(K8S_KUBECTL_SOURCE): build_kubernetes

# copy km and kubectl from the kubernetes _output directory to _build
SHA256 = $$(python -c "import hashlib, sys; print hashlib.sha256(sys.stdin.read()).hexdigest();" <"$(1)")
copy_kubernetes_binaries: $(K8S_BINARIES_SOURCE)
	echo -n "{" > $(K8S_KUBECTL_META)
	set -x; SEP=""; for OS in $(K8S_KUBECTL_OSES); do \
		mkdir -p $(BUILD_DIR)/$$OS && \
		if [ "$$OS" == windows ]; then KUBECTL=kubectl.exe; else KUBECTL=kubectl; fi && \
		cp -pv $(call K8S_BINARIES_SOURCE_DIR,$$OS)/$$KUBECTL $(BUILD_DIR)/$$OS && \
		echo "$$SEP" >> $(K8S_KUBECTL_META) && \
		echo -n "  \"$$OS-amd64\": {\"file\":\"$$OS/$$KUBECTL\", \"sha256\":\"$(call SHA256,$(BUILD_DIR)/$$OS/$$KUBECTL)\"}" >> $(K8S_KUBECTL_META) && \
		SEP="," && \
		bzip2 -f -k $(BUILD_DIR)/$$OS/$$KUBECTL; \
	done
	echo "$$SEP" >> $(K8S_KUBECTL_META)
	echo "}" >> $(K8S_KUBECTL_META)
	cp -pv $(call K8S_BINARIES_SOURCE_DIR,linux)/km $(BUILD_DIR)/linux
	touch $(K8S_BINARIES_DEST)
$(K8S_BINARIES_DEST): copy_kubernetes_binaries

# create executor overlay tar with nsenter and socat
OVERLAY_DIR ?= $(PWD)/$(BUILD_DIR)/overlay-target
$(BUILD_DIR)/sandbox-overlay.tar.gz:
	mkdir -p "$(OVERLAY_DIR)"
	docker run --rm -v "$(OVERLAY_DIR):/target" jpetazzo/nsenter
	docker run --rm -v "$(OVERLAY_DIR):/target" mesosphere/kubernetes-socat
	mkdir -p "$(BUILD_DIR)/overlay/bin"
	cp -a "$(OVERLAY_DIR)/"{nsenter,socat} "$(BUILD_DIR)/overlay/bin"
	chmod +x "$(BUILD_DIR)/overlay/bin/"*
	cd "$(BUILD_DIR)/overlay" && tar -czvf "../sandbox-overlay.tar.gz" .

# download etcd and extract it to _output/etcd-...
$(BUILD_DIR)/$(ETCD_TAR):
	cd $(BUILD_DIR) && curl -L -O $(ETCD_SOURCE) && tar xzvf $(ETCD_TAR)
	cp -pv $(addprefix $(ETCD_BINARIES_SOURCE_DIR)/,$(ETCD_BINARIES)) $(BUILD_DIR)
	touch $(ETCD_BINARIES_DEST)
$(ETCD_BINARIES_DEST): $(BUILD_DIR)/$(ETCD_TAR)

# copy the kube-dns specs from the kubernetes repo to _build
$(KUBE_DNS_TEMPLATES): $(KUBE_DNS_TEMPLATES_SRC)
	cp ${K8S_ROOT}/cluster/addons/dns/$(shell basename $@) $@
$(KUBE_DNS_TEMPLATES_SRC):

# copy the kube-ui specs from the kubernetes repo to build_
$(KUBE_UI_TEMPLATES): $(KUBE_UI_TEMPLATES_SRC)
	cp ${K8S_ROOT}/cluster/addons/kube-ui/$(shell basename $@) $@
$(KUBE_UI_TEMPLATES_SRC):

# package build targets
delete_docker_image_file:
	@rm -f "$(DOCKER_IMAGE_FILE)"
deps: $(BUILD_DIR) $(ETCD_BINARIES_DEST) $(BUILD_DIR)/$(S6_TAR) $(K8S_BINARIES_DEST) $(K8S_KUBECTL_SOURCE) $(BUILD_DIR)/.version $(KUBE_DNS_TEMPLATES) $(KUBE_UI_TEMPLATES) $(BUILD_DIR)/sandbox-overlay.tar.gz
build: banner persist_variables delete_docker_image_file checkout check_dirty deps
	docker build -t $(DOCKER_IMAGE) .
	docker tag -f $(DOCKER_IMAGE) $(DOCKER_REPO):latest
	@echo $(DOCKER_IMAGE) > $(DOCKER_IMAGE_FILE)

# push the Docker image to the Docker hub
push:
	@if [ ! -f $(DOCKER_IMAGE_FILE) ]; then \
		echo "No docker image ready to push" && \
		exit 1; \
	else \
		echo "Going to push the docker image $$(< $(DOCKER_IMAGE_FILE))" && \
		if tty -s; then \
			echo && \
			echo -n "Continue? [Y/n] " && read YES && if [ -n "$$YES" -a "$$YES" != Y -a "$$YES" != y ]; then exit 1; fi; \
		fi && \
		docker push $$(< $(DOCKER_IMAGE_FILE)); \
	fi

# run the kubernetes unit and integration tests inside Docker
test: checkout
	cd "${K8S_ROOT}" && KUBERNETES_CONTRIB=mesos build/run.sh hack/test-go.sh
	cd "${K8S_ROOT}" && KUBERNETES_CONTRIB=mesos build/run.sh hack/test-integration.sh

# install github-release, a tool to upload github release assets
$(GITHUB_RELEASE):
	@mkdir -p $(BUILD_DIR)/go
	export GOPATH=$$(pwd)/$(BUILD_DIR)/go && \
	go get github.com/aktau/github-release

# check that the environment variable GITHUB_TOKEN is set, needed to uploaded release assets
check_github_token:
	@if [ -z "$$GITHUB_TOKEN" ]; then \
		echo "To create releases please set GITHUB_TOKEN" && \
		exit 1; \
	fi

# upload the kubectl tar.gz for linux/amd64 and darwin/amd64 to github release given by VERSION
github-release-assets: $(GITHUB_RELEASE) check_github_token $(K8S_KUBECTL_DEST) $(BUILD_DIR)/.version
	@VERSION=$$(< $(BUILD_DIR)/.version) && \
	echo "Releasing kubectl tar.gz's as github assets for tag $${VERSION} on $(GIT_URL)" && \
	if tty -s; then \
			echo && \
			echo -n "Continue? [Y/n] " && read YES && if [ -n "$$YES" -a "$$YES" != Y -a "$$YES" != y ]; then exit 1; fi; \
	fi && \
	if $(GITHUB_RELEASE) info -u mesosphere --repo kubernetes | sed -n '/releases:/,$$p' | grep -q "$${VERSION},"; then \
		echo "Found $${VERSION} release"; \
	else \
		$(GITHUB_RELEASE) release -u mesosphere --repo kubernetes --tag $${VERSION} --name "Kubernetes on Mesos $${VERSION}" --draft && \
		echo "Creating $${VERSION} draft release"; \
	fi && \
	for OS in $(K8S_KUBECTL_OSES); do \
		BUILD_DIR=$$PWD/$(BUILD_DIR) && \
		cd "$$BUILD_DIR/$$OS" && \
		TAR=kubectl-$${VERSION}-$$OS-amd64.tgz && \
		tar -czf $$BUILD_DIR/$$TAR "kubectl$$(if [ $$OS = windows ]; then echo -n .exe; fi)" && \
		cd - &>/dev/null && \
		echo "Uploading $$TAR as github asset for tag $${VERSION}" && \
		$(GITHUB_RELEASE) upload -u mesosphere --repo kubernetes --tag $${VERSION} --file $(BUILD_DIR)/$$TAR --name $$TAR || exit 1; \
	done && \
	echo && \
	URL="https://github.com/mesosphere/kubernetes/releases/tag/$${VERSION}" && \
	if ! curl -f -q "$$URL" &>/dev/null; then \
		URL="https://github.com/mesosphere/kubernetes/releases"; \
	fi && \
	echo "Goto $$URL to edit the release details"
