// Copyright 2018 The Cluster Monitoring Operator Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/golang/glog"
	"golang.org/x/sync/errgroup"

	cmo "github.com/openshift/cluster-monitoring-operator/pkg/operator"
)

type tags map[string]string

func (t *tags) String() string {
	m := *t
	slice := m.asSlice()
	return strings.Join(slice, ",")
}

func (t *tags) Set(value string) error {
	m := *t
	pairs := strings.Split(value, ",")
	for _, pair := range pairs {
		splitPair := strings.Split(pair, "=")
		if len(splitPair) != 2 {
			return fmt.Errorf("Pair %v is malformed. Key value pairs must be in the form of \"key=value\". Multiple pairs must be comma separated.")
		}
		imageName := splitPair[0]
		imageTag := splitPair[1]
		m[imageName] = imageTag
	}
	return nil
}

func (t tags) asSlice() []string {
	pairs := []string{}
	for name, tag := range t {
		pairs = append(pairs, name+"="+tag)
	}
	return pairs
}

func (t tags) asMap() map[string]string {
	res := make(map[string]string, len(t))
	for k, v := range t {
		res[k] = v
	}
	return res
}

func (t *tags) Type() string {
	return "map[string]string"
}

func Main() int {
	flagset := flag.CommandLine
	namespace := flagset.String("namespace", "openshift-monitoring", "Namespace to deploy and manage cluster monitoring stack in.")
	configMapName := flagset.String("configmap", "cluster-monitoring-config", "ConfigMap name to configure the cluster monitoring stack.")
	tags := tags{}
	flag.Var(&tags, "tags", "Tags to use for images.")
	flag.Parse()

	if *namespace == "" {
		fmt.Fprint(os.Stderr, "`--namespace` flag is required, but not specified.")
	}

	if *configMapName == "" {
		fmt.Fprint(os.Stderr, "`--configmap` flag is required, but not specified.")
	}

	o, err := cmo.New(*namespace, *configMapName, tags.asMap())
	if err != nil {
		fmt.Fprint(os.Stderr, err)
		return 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	wg, ctx := errgroup.WithContext(ctx)

	wg.Go(func() error { return o.Run(ctx.Done()) })

	term := make(chan os.Signal)
	signal.Notify(term, os.Interrupt, syscall.SIGTERM)

	select {
	case <-term:
		glog.V(4).Info("Received SIGTERM, exiting gracefully...")
	case <-ctx.Done():
	}

	cancel()
	if err := wg.Wait(); err != nil {
		glog.V(4).Info("Unhandled error received. Exiting...err: %s", err)
		return 1
	}

	return 0
}

func main() {
	os.Exit(Main())
}
