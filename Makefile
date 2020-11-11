# Copyright 2019 The OCGI Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.


REGISTRY_NAME=hub.docker.com/ocgi
GIT_COMMIT=$(shell git rev-parse "HEAD^{commit}")
VERSION=$(shell git describe --tags --abbrev=14 "${GIT_COMMIT}^{commit}" --always)
BUILD_TIME=$(shell TZ=Asia/Shanghai date +%FT%T%z)
VERSION_KEY=github.com/ocgi/carrier/pkg/version.Version
COMMIT_KEY=github.com/ocgi/carrier/pkg/version.Commit
BUILDTIME_KEY=github.com/ocgi/carrier/pkg/version.BuildTime

CMDS=build
all: test build

build: build-controller

build-controller:
	mkdir -p bin
	go fmt ./pkg/...
	go vet ./pkg/...
	CGO_ENABLED=0 GOOS=linux go build -ldflags "-X '$(VERSION_KEY)=$(VERSION)' -X '$(COMMIT_KEY)=$(GIT_COMMIT)' -X '$(BUILDTIME_KEY)=$(BUILD_TIME)'" -o ./bin/controller ./cmd/controller

container: build
	docker build -t $(REGISTRY_NAME)/carrier-controller:$(VERSION) -f $(shell if [ -e ./cmd/controller/Dockerfile ]; then echo ./cmd/controller/Dockerfile; else echo Dockerfile; fi) --label revision=$(REV) .

push: container
	docker push $(REGISTRY_NAME)/carrier-controller:$(VERSION)

test:
	go test -count=1 ./pkg/...

autogen:
	go mod vendor
	bash hack/update-codegen.sh
