// Copyright (c) 2016 Pulcy.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package backend

import (
	"encoding/json"
	"path"

	"github.com/coreos/etcd/client"
	"github.com/juju/errgo"
	"github.com/op/go-logging"
	regapi "github.com/pulcy/registrator-api"
	"github.com/pulcy/robin-api"
	"golang.org/x/net/context"
)

const (
	servicePrefix        = "service"
	frontEndPrefix       = "frontend"
	recentWatchErrorsMax = 5
)

type BackendConfig struct {
	PublicEdgePort      int
	PrivateHttpEdgePort int
	PrivateTcpEdgePort  int
}

type etcdBackend struct {
	config            BackendConfig
	client            client.Client
	watcher           client.Watcher
	registratorAPI    regapi.API
	Logger            *logging.Logger
	prefix            string
	recentWatchErrors int
}

func NewEtcdBackend(config BackendConfig, logger *logging.Logger, c client.Client, etcdPath string) (Backend, error) {
	kAPI := client.NewKeysAPI(c)
	options := &client.WatcherOptions{
		Recursive: true,
	}
	registratorAPI, err := regapi.NewRegistratorClient(c, path.Join(etcdPath, servicePrefix), logger)
	if err != nil {
		return nil, maskAny(err)
	}
	watcher := kAPI.Watcher(etcdPath, options)
	return &etcdBackend{
		config:         config,
		client:         c,
		watcher:        watcher,
		registratorAPI: registratorAPI,
		prefix:         etcdPath,
		Logger:         logger,
	}, nil
}

// Watch for changes on a path and return where there is a change.
func (eb *etcdBackend) Watch() error {
	if eb.watcher == nil || eb.recentWatchErrors > recentWatchErrorsMax {
		eb.recentWatchErrors = 0
		kAPI := client.NewKeysAPI(eb.client)
		options := &client.WatcherOptions{
			Recursive: true,
		}
		eb.watcher = kAPI.Watcher(eb.prefix, options)
	}
	_, err := eb.watcher.Next(context.Background())
	if err != nil {
		eb.recentWatchErrors++
		return maskAny(err)
	}
	eb.recentWatchErrors = 0
	return nil
}

// Load all registered services
func (eb *etcdBackend) Services() (ServiceRegistrations, error) {
	servicesTree, err := eb.registratorAPI.Services()
	if err != nil {
		return nil, maskAny(err)
	}
	frontEndTree, err := eb.readFrontEndsTree()
	if err != nil {
		return nil, maskAny(err)
	}
	result, err := eb.mergeTrees(servicesTree, frontEndTree)
	if err != nil {
		return nil, maskAny(err)
	}
	return result, nil
}

// Load all registered front-ends
func (eb *etcdBackend) readFrontEndsTree() ([]api.FrontendRecord, error) {
	etcdPath := path.Join(eb.prefix, frontEndPrefix)
	kAPI := client.NewKeysAPI(eb.client)
	options := &client.GetOptions{
		Recursive: false,
		Sort:      false,
	}
	resp, err := kAPI.Get(context.Background(), etcdPath, options)
	if err != nil {
		return nil, maskAny(err)
	}
	list := []api.FrontendRecord{}
	if resp.Node == nil {
		return list, nil
	}
	for _, frontEndNode := range resp.Node.Nodes {
		rawJson := frontEndNode.Value
		record := api.FrontendRecord{}
		if err := json.Unmarshal([]byte(rawJson), &record); err != nil {
			eb.Logger.Errorf("Cannot unmarshal registration of %s", frontEndNode.Key)
			continue
		}
		list = append(list, record)
	}

	return list, nil
}

// mergeTrees merges the 2 trees into a single list of registrations.
func (eb *etcdBackend) mergeTrees(services []regapi.Service, frontends []api.FrontendRecord) (ServiceRegistrations, error) {
	result, err := mergeTrees(eb.Logger, eb.config, services, frontends)
	if err != nil {
		return nil, maskAny(err)
	}
	return result, nil
}

func isEtcdError(err error, code int) bool {
	cerr, ok := errgo.Cause(err).(client.Error)
	return ok && cerr.Code == code
}
