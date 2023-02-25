// Copyright 2022 Google LLC
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

package workload_test

import (
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/cloud-sql-proxy-operator/internal/api/v1alpha1"
	"github.com/GoogleCloudPlatform/cloud-sql-proxy-operator/internal/workload"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

func podWorkload() *workload.PodWorkload {
	return &workload.PodWorkload{Pod: &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{Kind: "Deployment", APIVersion: "apps/v1"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "busybox", Labels: map[string]string{"app": "hello"}},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "busybox", Image: "busybox"}},
		},
	}}
}

func simpleAuthProxy(name, connectionString string) *v1alpha1.AuthProxyWorkload {
	return authProxyWorkload(name, []v1alpha1.InstanceSpec{{
		ConnectionString: connectionString,
	}})
}

func authProxyWorkload(name string, instances []v1alpha1.InstanceSpec) *v1alpha1.AuthProxyWorkload {
	return authProxyWorkloadFromSpec(name, v1alpha1.AuthProxyWorkloadSpec{
		Workload: v1alpha1.WorkloadSelectorSpec{
			Kind: "Deployment",
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "hello"},
			},
		},
		Instances: instances,
	})
}
func authProxyWorkloadFromSpec(name string, spec v1alpha1.AuthProxyWorkloadSpec) *v1alpha1.AuthProxyWorkload {
	proxy := &v1alpha1.AuthProxyWorkload{
		TypeMeta:   metav1.TypeMeta{Kind: "AuthProxyWorkload", APIVersion: v1alpha1.GroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Generation: 1},
		Spec:       spec,
	}
	proxy.Spec.Workload = v1alpha1.WorkloadSelectorSpec{
		Kind: "Deployment",
		Selector: &metav1.LabelSelector{
			MatchLabels:      map[string]string{"app": "hello"},
			MatchExpressions: nil,
		},
	}

	return proxy
}

func findContainer(wl *workload.PodWorkload, name string) (corev1.Container, error) {
	for i := range wl.Pod.Spec.Containers {
		c := &wl.Pod.Spec.Containers[i]
		if c.Name == name {
			return *c, nil
		}
	}
	return corev1.Container{}, fmt.Errorf("no container found with name %s", name)
}

func findEnvVar(wl *workload.PodWorkload, containerName, envName string) (corev1.EnvVar, error) {
	container, err := findContainer(wl, containerName)
	if err != nil {
		return corev1.EnvVar{}, err
	}
	for i := 0; i < len(container.Env); i++ {
		if container.Env[i].Name == envName {
			return container.Env[i], nil
		}
	}
	return corev1.EnvVar{}, fmt.Errorf("no envvar named %v on container %v", envName, containerName)
}

func hasArg(wl *workload.PodWorkload, containerName, argValue string) (bool, error) {
	container, err := findContainer(wl, containerName)
	if err != nil {
		return false, err
	}
	for i := 0; i < len(container.Command); i++ {
		if container.Command[i] == argValue {
			return true, nil
		}
	}
	for i := 0; i < len(container.Args); i++ {
		if container.Args[i] == argValue {
			return true, nil
		}
	}
	return false, nil
}

func logPodSpec(t *testing.T, wl *workload.PodWorkload) {
	podSpecYaml, err := yaml.Marshal(wl.Pod.Spec)
	if err != nil {
		t.Errorf("unexpected error while marshaling PodSpec to yaml, %v", err)
	}
	t.Logf("PodSpec: %s", string(podSpecYaml))
}

func configureProxies(u *workload.Updater, wl *workload.PodWorkload, proxies []*v1alpha1.AuthProxyWorkload) error {
	l := &v1alpha1.AuthProxyWorkloadList{Items: make([]v1alpha1.AuthProxyWorkload, len(proxies))}
	for i := 0; i < len(proxies); i++ {
		l.Items[i] = *proxies[i]
	}
	apws := u.FindMatchingAuthProxyWorkloads(l, wl, nil)
	err := u.ConfigureWorkload(wl, apws)
	return err
}

func TestUpdatePodWorkload(t *testing.T) {
	var (
		wantsName               = "instance1"
		wantsPort         int32 = 8080
		wantContainerName       = "csql-default-" + wantsName
		wantsInstanceName       = "project:server:db"
		wantsInstanceArg        = fmt.Sprintf("%s?port=%d", wantsInstanceName, wantsPort)
		u                       = workload.NewUpdater("cloud-sql-proxy-operator/dev")
	)
	var err error

	// Create a pod
	wl := podWorkload()

	// ensure that the deployment only has one container before
	// updating the deployment.
	if len(wl.Pod.Spec.Containers) != 1 {
		t.Fatalf("got %v, wants 1. deployment containers length", len(wl.Pod.Spec.Containers))
	}

	// Create a AuthProxyWorkload that matches the deployment
	proxy := simpleAuthProxy(wantsName, wantsInstanceName)
	proxy.Spec.Instances[0].Port = ptr(wantsPort)

	// Update the container with new markWorkloadNeedsUpdate
	err = configureProxies(u, wl, []*v1alpha1.AuthProxyWorkload{proxy})
	if err != nil {
		t.Fatal(err)
	}

	// test that there are now 2 containers
	if want, got := 2, len(wl.Pod.Spec.Containers); want != got {
		t.Fatalf("got %v want %v, number of deployment containers", got, want)
	}

	t.Logf("Containers: {%v}", wl.Pod.Spec.Containers)

	// test that the container has the proper name following the conventions
	foundContainer, err := findContainer(wl, wantContainerName)
	if err != nil {
		t.Fatal(err)
	}

	// test that the container args have the expected args
	if gotArg, err := hasArg(wl, wantContainerName, wantsInstanceArg); err != nil || !gotArg {
		t.Errorf("wants connection string arg %v but it was not present in proxy container args %v",
			wantsInstanceArg, foundContainer.Args)
	}

}

func TestUpdateWorkloadFixedPort(t *testing.T) {
	var (
		wantsInstanceName = "project:server:db"
		wantsPort         = int32(5555)
		wantContainerArgs = []string{
			fmt.Sprintf("%s?port=%d", wantsInstanceName, wantsPort),
		}
		wantWorkloadEnv = map[string]string{
			"DB_HOST": "127.0.0.1",
			"DB_PORT": strconv.Itoa(int(wantsPort)),
		}
		u = workload.NewUpdater("cloud-sql-proxy-operator/dev")
	)

	// Create a pod
	wl := podWorkload()
	wl.Pod.Spec.Containers[0].Ports =
		[]corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}

	// Create a AuthProxyWorkload that matches the deployment
	csqls := []*v1alpha1.AuthProxyWorkload{
		authProxyWorkload("instance1", []v1alpha1.InstanceSpec{{
			ConnectionString: wantsInstanceName,
			Port:             &wantsPort,
			PortEnvName:      "DB_PORT",
			HostEnvName:      "DB_HOST",
		}}),
	}

	// ensure that the new container does not exist
	if len(wl.Pod.Spec.Containers) != 1 {
		t.Fatalf("got %v, wants 1. deployment containers length", len(wl.Pod.Spec.Containers))
	}

	// update the containers
	err := configureProxies(u, wl, csqls)
	if err != nil {
		t.Fatal(err)
	}

	// ensure that the new container does not exist
	if len(wl.Pod.Spec.Containers) != 2 {
		t.Fatalf("got %v, wants 1. deployment containers length", len(wl.Pod.Spec.Containers))
	}

	// test that the instancename matches the new expected instance name.
	csqlContainer, err := findContainer(wl, fmt.Sprintf("csql-default-%s", csqls[0].GetName()))
	if err != nil {
		t.Fatal(err)
	}

	// test that port cli args are set correctly
	assertContainerArgsContains(t, csqlContainer.Args, wantContainerArgs)

	// Test that workload has the right env vars
	for wantKey, wantValue := range wantWorkloadEnv {
		gotEnvVar, err := findEnvVar(wl, "busybox", wantKey)
		if err != nil {
			t.Error(err)
			logPodSpec(t, wl)
		} else if gotEnvVar.Value != wantValue {
			t.Errorf("got %v, wants %v workload env var %v", gotEnvVar, wantValue, wantKey)
		}
	}

}

func TestWorkloadNoPortSet(t *testing.T) {
	var (
		wantsInstanceName = "project:server:db"
		wantsPort         = int32(5000)
		wantContainerArgs = []string{
			fmt.Sprintf("%s?port=%d", wantsInstanceName, wantsPort),
		}
		wantWorkloadEnv = map[string]string{
			"DB_HOST": "127.0.0.1",
			"DB_PORT": strconv.Itoa(int(wantsPort)),
		}
	)
	u := workload.NewUpdater("cloud-sql-proxy-operator/dev")

	// Create a pod
	wl := podWorkload()
	wl.Pod.Spec.Containers[0].Ports =
		[]corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}

	// Create a AuthProxyWorkload that matches the deployment
	csqls := []*v1alpha1.AuthProxyWorkload{
		authProxyWorkload("instance1", []v1alpha1.InstanceSpec{{
			ConnectionString: wantsInstanceName,
			PortEnvName:      "DB_PORT",
			HostEnvName:      "DB_HOST",
		}}),
	}

	// ensure that the new container does not exist
	if len(wl.Pod.Spec.Containers) != 1 {
		t.Fatalf("got %v, wants 1. deployment containers length", len(wl.Pod.Spec.Containers))
	}

	// update the containers
	err := configureProxies(u, wl, csqls)
	if err != nil {
		t.Fatal(err)
	}

	// ensure that the new container does not exist
	if len(wl.Pod.Spec.Containers) != 2 {
		t.Fatalf("got %v, wants 1. deployment containers length", len(wl.Pod.Spec.Containers))
	}

	// test that the instancename matches the new expected instance name.
	csqlContainer, err := findContainer(wl, fmt.Sprintf("csql-default-%s", csqls[0].GetName()))
	if err != nil {
		t.Fatal(err)
	}

	// test that port cli args are set correctly
	assertContainerArgsContains(t, csqlContainer.Args, wantContainerArgs)

	// Test that workload has the right env vars
	for wantKey, wantValue := range wantWorkloadEnv {
		gotEnvVar, err := findEnvVar(wl, "busybox", wantKey)
		if err != nil {
			t.Error(err)
			logPodSpec(t, wl)
		} else if gotEnvVar.Value != wantValue {
			t.Errorf("got %v, wants %v workload env var %v", gotEnvVar, wantValue, wantKey)
		}
	}

}

func TestContainerImageChanged(t *testing.T) {
	var (
		wantsInstanceName = "project:server:db"
		wantImage         = "custom-image:latest"
		u                 = workload.NewUpdater("cloud-sql-proxy-operator/dev")
	)

	// Create a pod
	wl := podWorkload()
	wl.Pod.Spec.Containers[0].Ports =
		[]corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}

	// Create a AuthProxyWorkload that matches the deployment
	csqls := []*v1alpha1.AuthProxyWorkload{
		simpleAuthProxy("instance1", wantsInstanceName),
	}
	csqls[0].Spec.AuthProxyContainer = &v1alpha1.AuthProxyContainerSpec{Image: wantImage}

	// update the containers
	err := configureProxies(u, wl, csqls)
	if err != nil {
		t.Fatal(err)
	}

	// ensure that the new container exists
	if len(wl.Pod.Spec.Containers) != 2 {
		t.Fatalf("got %v, wants 1. deployment containers length", len(wl.Pod.Spec.Containers))
	}

	// test that the instancename matches the new expected instance name.
	csqlContainer, err := findContainer(wl, fmt.Sprintf("csql-default-%s", csqls[0].GetName()))
	if err != nil {
		t.Fatal(err)
	}

	// test that image was set
	if csqlContainer.Image != wantImage {
		t.Errorf("got %v, want %v for proxy container image", csqlContainer.Image, wantImage)
	}

}

func TestContainerImageEmpty(t *testing.T) {
	var (
		wantsInstanceName = "project:server:db"
		wantImage         = workload.DefaultProxyImage
		u                 = workload.NewUpdater("cloud-sql-proxy-operator/dev")
	)
	// Create a AuthProxyWorkload that matches the deployment

	// create an AuthProxyContainer that has a value, but Image is empty.
	p1 := simpleAuthProxy("instance1", wantsInstanceName)
	p1.Spec.AuthProxyContainer = &v1alpha1.AuthProxyContainerSpec{MaxConnections: ptr(int64(5))}

	// create an AuthProxyContainer where AuthProxyContainer is nil
	p2 := simpleAuthProxy("instance1", wantsInstanceName)
	p2.Spec.AuthProxyContainer = nil

	tests := []struct {
		name  string
		proxy *v1alpha1.AuthProxyWorkload
	}{
		{name: "Image is empty", proxy: p1},
		{name: "AuthProxyContainer is nil", proxy: p2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Create a pod
			wl := podWorkload()
			wl.Pod.Spec.Containers[0].Ports =
				[]corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}
			csqls := []*v1alpha1.AuthProxyWorkload{test.proxy}

			// update the containers
			err := configureProxies(u, wl, csqls)
			if err != nil {
				t.Fatal(err)
			}

			// ensure that the new container exists
			if len(wl.Pod.Spec.Containers) != 2 {
				t.Fatalf("got %v, wants 1. deployment containers length", len(wl.Pod.Spec.Containers))
			}

			// test that the instancename matches the new expected instance name.
			csqlContainer, err := findContainer(wl, fmt.Sprintf("csql-default-%s", csqls[0].GetName()))
			if err != nil {
				t.Fatal(err)
			}

			// test that image was set
			if csqlContainer.Image != wantImage {
				t.Fatalf("got %v, want %v for proxy container image", csqlContainer.Image, wantImage)
			}

		})
	}
}

func TestContainerReplaced(t *testing.T) {
	var (
		wantsInstanceName = "project:server:db"
		wantContainer     = &corev1.Container{
			Name: "sample", Image: "debian:latest", Command: []string{"/bin/bash"},
		}
		u = workload.NewUpdater("cloud-sql-proxy-operator/dev")
	)

	// Create a pod
	wl := podWorkload()
	wl.Pod.Spec.Containers[0].Ports =
		[]corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}

	// Create a AuthProxyWorkload that matches the deployment
	csqls := []*v1alpha1.AuthProxyWorkload{simpleAuthProxy("instance1", wantsInstanceName)}
	csqls[0].Spec.AuthProxyContainer = &v1alpha1.AuthProxyContainerSpec{Container: wantContainer}

	// update the containers
	err := configureProxies(u, wl, csqls)
	if err != nil {
		t.Fatal(err)
	}

	// ensure that the new container exists
	if len(wl.Pod.Spec.Containers) != 2 {
		t.Fatalf("got %v, wants 1. deployment containers length", len(wl.Pod.Spec.Containers))
	}

	// test that the instancename matches the new expected instance name.
	csqlContainer, err := findContainer(wl, fmt.Sprintf("csql-default-%s", csqls[0].GetName()))
	if err != nil {
		t.Fatal(err)
	}

	// test that image was set
	if csqlContainer.Image != wantContainer.Image {
		t.Errorf("got %v, want %v for proxy container image", csqlContainer.Image, wantContainer.Image)
	}
	// test that image was set
	if !reflect.DeepEqual(csqlContainer.Command, wantContainer.Command) {
		t.Errorf("got %v, want %v for proxy container command", csqlContainer.Command, wantContainer.Command)
	}

}

func ptr[T int | int32 | int64 | string | bool](i T) *T {
	return &i
}

func TestResourcesFromSpec(t *testing.T) {
	var (
		wantsInstanceName = "project:server:db"
		wantResources     = &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				"cpu":    resource.MustParse("4.0"),
				"memory": resource.MustParse("4Gi"),
			},
		}

		u = workload.NewUpdater("cloud-sql-proxy-operator/dev")
	)

	// Create a pod
	wl := podWorkload()
	wl.Pod.Spec.Containers[0].Ports =
		[]corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}

	// Create a AuthProxyWorkload that matches the deployment
	csqls := []*v1alpha1.AuthProxyWorkload{simpleAuthProxy("instance1", wantsInstanceName)}
	csqls[0].Spec.AuthProxyContainer = &v1alpha1.AuthProxyContainerSpec{Resources: wantResources}

	// update the containers
	err := configureProxies(u, wl, csqls)
	if err != nil {
		t.Fatal(err)
	}

	// ensure that the new container exists
	if len(wl.Pod.Spec.Containers) != 2 {
		t.Fatalf("got %v, wants 1. deployment containers length", len(wl.Pod.Spec.Containers))
	}

	// test that the instancename matches the new expected instance name.
	csqlContainer, err := findContainer(wl, fmt.Sprintf("csql-default-%s", csqls[0].GetName()))
	if err != nil {
		t.Fatal(err)
	}

	// test that resources was set
	if !reflect.DeepEqual(csqlContainer.Resources.Requests, wantResources.Requests) {
		t.Errorf("got %v, want %v for proxy container command", csqlContainer.Resources.Requests, wantResources.Requests)
	}

}

func TestProxyCLIArgs(t *testing.T) {
	wantTrue := true
	wantFalse := false

	var wantPort int32 = 5000

	testcases := []struct {
		desc                 string
		proxySpec            v1alpha1.AuthProxyWorkloadSpec
		wantProxyArgContains []string
		wantErrorCodes       []string
		wantWorkloadEnv      map[string]string
		dontWantEnvSet       []string
	}{
		{
			desc: "default cli config",
			proxySpec: v1alpha1.AuthProxyWorkloadSpec{
				Instances: []v1alpha1.InstanceSpec{{
					ConnectionString: "hello:world:db",
					Port:             &wantPort,
					PortEnvName:      "DB_PORT",
				}},
			},
			wantWorkloadEnv: map[string]string{
				"CSQL_PROXY_STRUCTURED_LOGS": "true",
				"CSQL_PROXY_HEALTH_CHECK":    "true",
				"CSQL_PROXY_HTTP_PORT":       fmt.Sprintf("%d", workload.DefaultHealthCheckPort),
				"CSQL_PROXY_HTTP_ADDRESS":    "0.0.0.0",
				"CSQL_PROXY_USER_AGENT":      "cloud-sql-proxy-operator/dev",
			},
		},
		{
			desc: "port explicitly set",
			proxySpec: v1alpha1.AuthProxyWorkloadSpec{
				Instances: []v1alpha1.InstanceSpec{{
					ConnectionString: "hello:world:db",
					Port:             &wantPort,
					PortEnvName:      "DB_PORT",
				}},
			},
			wantProxyArgContains: []string{"hello:world:db?port=5000"},
		},
		{
			desc: "port implicitly set and increments",
			proxySpec: v1alpha1.AuthProxyWorkloadSpec{
				Instances: []v1alpha1.InstanceSpec{{
					ConnectionString: "hello:world:one",
					PortEnvName:      "DB_PORT",
				},
					{
						ConnectionString: "hello:world:two",
						PortEnvName:      "DB_PORT_2",
					}},
			},
			wantProxyArgContains: []string{
				fmt.Sprintf("hello:world:one?port=%d", workload.DefaultFirstPort),
				fmt.Sprintf("hello:world:two?port=%d", workload.DefaultFirstPort+1)},
		},
		{
			desc: "env name conflict causes error",
			proxySpec: v1alpha1.AuthProxyWorkloadSpec{
				Instances: []v1alpha1.InstanceSpec{{
					ConnectionString: "hello:world:one",
					PortEnvName:      "DB_PORT",
				},
					{
						ConnectionString: "hello:world:two",
						PortEnvName:      "DB_PORT",
					}},
			},
			wantProxyArgContains: []string{
				fmt.Sprintf("hello:world:one?port=%d", workload.DefaultFirstPort),
				fmt.Sprintf("hello:world:two?port=%d", workload.DefaultFirstPort+1)},
			wantErrorCodes: []string{v1alpha1.ErrorCodeEnvConflict},
		},
		{
			desc: "auto-iam-authn set",
			proxySpec: v1alpha1.AuthProxyWorkloadSpec{
				Instances: []v1alpha1.InstanceSpec{{
					ConnectionString: "hello:world:one",
					PortEnvName:      "DB_PORT",
					AutoIAMAuthN:     &wantTrue,
				},
					{
						ConnectionString: "hello:world:two",
						PortEnvName:      "DB_PORT_2",
						AutoIAMAuthN:     &wantFalse,
					}},
			},
			wantProxyArgContains: []string{
				fmt.Sprintf("hello:world:one?auto-iam-authn=true&port=%d", workload.DefaultFirstPort),
				fmt.Sprintf("hello:world:two?auto-iam-authn=false&port=%d", workload.DefaultFirstPort+1)},
		},
		{
			desc: "private-ip set",
			proxySpec: v1alpha1.AuthProxyWorkloadSpec{
				Instances: []v1alpha1.InstanceSpec{{
					ConnectionString: "hello:world:one",
					PortEnvName:      "DB_PORT",
					PrivateIP:        &wantTrue,
				},
					{
						ConnectionString: "hello:world:two",
						PortEnvName:      "DB_PORT_2",
						PrivateIP:        &wantFalse,
					}},
			},
			wantProxyArgContains: []string{
				fmt.Sprintf("hello:world:one?port=%d&private-ip=true", workload.DefaultFirstPort),
				fmt.Sprintf("hello:world:two?port=%d&private-ip=false", workload.DefaultFirstPort+1)},
		},
		{
			desc: "global flags",
			proxySpec: v1alpha1.AuthProxyWorkloadSpec{
				AuthProxyContainer: &v1alpha1.AuthProxyContainerSpec{
					SQLAdminAPIEndpoint: "https://example.com",
					Telemetry: &v1alpha1.TelemetrySpec{
						HTTPPort: ptr(int32(9092)),
					},
					AdminServer: &v1alpha1.AdminServerSpec{
						EnableAPIs: []string{"Debug", "QuitQuitQuit"},
						Port:       int32(9091),
					},
					MaxConnections:  ptr(int64(10)),
					MaxSigtermDelay: ptr(int64(20)),
				},
				Instances: []v1alpha1.InstanceSpec{{
					ConnectionString: "hello:world:one",
					Port:             ptr(int32(5000)),
				}},
			},
			wantProxyArgContains: []string{
				fmt.Sprintf("hello:world:one?port=%d", workload.DefaultFirstPort),
			},
			wantWorkloadEnv: map[string]string{
				"CSQL_PROXY_SQLADMIN_API_ENDPOINT": "https://example.com",
				"CSQL_PROXY_HTTP_PORT":             "9092",
				"CSQL_PROXY_ADMIN_PORT":            "9091",
				"CSQL_PROXY_DEBUG":                 "true",
				"CSQL_PROXY_QUITQUITQUIT":          "true",
				"CSQL_PROXY_HEALTH_CHECK":          "true",
				"CSQL_PROXY_MAX_CONNECTIONS":       "10",
				"CSQL_PROXY_MAX_SIGTERM_DELAY":     "20",
			},
		},
		{
			desc: "No admin port enabled when AdminServerSpec is nil",
			proxySpec: v1alpha1.AuthProxyWorkloadSpec{
				AuthProxyContainer: &v1alpha1.AuthProxyContainerSpec{},
				Instances: []v1alpha1.InstanceSpec{{
					ConnectionString: "hello:world:one",
					Port:             ptr(int32(5000)),
				}},
			},
			wantProxyArgContains: []string{
				fmt.Sprintf("hello:world:one?port=%d", workload.DefaultFirstPort),
			},
			wantWorkloadEnv: map[string]string{
				"CSQL_PROXY_HEALTH_CHECK": "true",
			},
			dontWantEnvSet: []string{"CSQL_PROXY_DEBUG", "CSQL_PROXY_ADMIN_PORT"},
		},
		{
			desc: "port conflict with other instance causes error",
			proxySpec: v1alpha1.AuthProxyWorkloadSpec{
				Instances: []v1alpha1.InstanceSpec{{
					ConnectionString: "hello:world:one",
					PortEnvName:      "DB_PORT_1",
					Port:             ptr(int32(8081)),
				},
					{
						ConnectionString: "hello:world:two",
						PortEnvName:      "DB_PORT_2",
						Port:             ptr(int32(8081)),
					}},
			},
			wantProxyArgContains: []string{
				fmt.Sprintf("hello:world:one?port=%d", 8081),
				fmt.Sprintf("hello:world:two?port=%d", 8081)},
			wantErrorCodes: []string{v1alpha1.ErrorCodePortConflict},
		},
		{
			desc: "port conflict with workload container",
			proxySpec: v1alpha1.AuthProxyWorkloadSpec{
				Instances: []v1alpha1.InstanceSpec{{
					ConnectionString: "hello:world:one",
					PortEnvName:      "DB_PORT_1",
					Port:             ptr(int32(8080)),
				}},
			},
			wantProxyArgContains: []string{
				fmt.Sprintf("hello:world:one?port=%d", 8080)},
			wantErrorCodes: []string{v1alpha1.ErrorCodePortConflict},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.desc, func(t *testing.T) {
			u := workload.NewUpdater("cloud-sql-proxy-operator/dev")

			// Create a pod
			wl := &workload.PodWorkload{Pod: &corev1.Pod{
				TypeMeta:   metav1.TypeMeta{Kind: "Deployment", APIVersion: "apps/v1"},
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "busybox", Labels: map[string]string{"app": "hello"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "busybox", Image: "busybox",
						Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}}},
				},
			}}

			// Create a AuthProxyWorkload that matches the deployment
			csqls := []*v1alpha1.AuthProxyWorkload{authProxyWorkloadFromSpec("instance1", tc.proxySpec)}

			// ensure valid
			err := csqls[0].ValidateCreate()
			if err != nil {
				t.Fatal("Invalid AuthProxyWorkload resource", err)
			}

			// update the containers
			updateErr := configureProxies(u, wl, csqls)

			if len(tc.wantErrorCodes) > 0 {
				assertErrorCodeContains(t, updateErr, tc.wantErrorCodes)
				return
			}

			// ensure that the new container exists
			if len(wl.Pod.Spec.Containers) != 2 {
				t.Fatalf("got %v, wants 2. deployment containers length", len(wl.Pod.Spec.Containers))
			}

			// test that the instancename matches the new expected instance name.
			csqlContainer, err := findContainer(wl, fmt.Sprintf("csql-default-%s", csqls[0].GetName()))
			if err != nil {
				t.Fatal(err)
			}

			// test that port cli args are set correctly
			assertContainerArgsContains(t, csqlContainer.Args, tc.wantProxyArgContains)

			// Test that workload has the right env vars
			for wantKey, wantValue := range tc.wantWorkloadEnv {
				gotEnvVar, err := findEnvVar(wl, csqlContainer.Name, wantKey)
				if err != nil {
					t.Error(err)
					continue
				}

				if gotEnvVar.Value != wantValue {
					t.Errorf("got %v, wants %v workload env var %v", gotEnvVar, wantValue, wantKey)
				}
			}
			for _, dontWantKey := range tc.dontWantEnvSet {
				gotEnvVar, err := findEnvVar(wl, csqlContainer.Name, dontWantKey)
				if err != nil {
					continue
				}
				t.Errorf("got env %v=%v, wants no env var set", dontWantKey, gotEnvVar)
			}

		})
	}

}

func assertErrorCodeContains(t *testing.T, gotErr error, wantErrors []string) {
	if gotErr == nil {
		if len(wantErrors) > 0 {
			t.Errorf("got missing errors, wants errors with codes %v", wantErrors)
		}
		return
	}
	gotError, ok := gotErr.(*workload.ConfigError)
	if !ok {
		t.Errorf("got an error %v, wants error of type *internal.ConfigError", gotErr)
		return
	}

	errs := gotError.DetailedErrors()

	for i := 0; i < len(wantErrors); i++ {
		wantArg := wantErrors[i]
		found := false
		for j := 0; j < len(errs) && !found; j++ {
			if wantArg == errs[j].ErrorCode {
				found = true
			}
		}
		if !found {
			t.Errorf("missing error, wants error with code %v, got error %v", wantArg, gotError)
		}
	}

	for i := 0; i < len(errs); i++ {
		gotErr := errs[i]
		found := false
		for j := 0; j < len(wantErrors) && !found; j++ {
			if gotErr.ErrorCode == wantErrors[j] {
				found = true
			}
		}
		if !found {
			t.Errorf("got unexpected error %v", gotErr)
		}
	}

}

func assertContainerArgsContains(t *testing.T, gotArgs, wantArgs []string) {
	for i := 0; i < len(wantArgs); i++ {
		wantArg := wantArgs[i]
		found := false
		for j := 0; j < len(gotArgs) && !found; j++ {
			if wantArg == gotArgs[j] {
				found = true
			}
		}
		if !found {
			t.Errorf("missing argument, wants argument %v, got arguments %v", wantArg, gotArgs)
		}
	}
}

func TestPodTemplateAnnotations(t *testing.T) {

	var (
		now = metav1.Now()

		wantAnnotations = map[string]string{
			"cloudsql.cloud.google.com/instance1": "1",
			"cloudsql.cloud.google.com/instance2": "2",
		}

		u = workload.NewUpdater("cloud-sql-proxy-operator/dev")
	)

	// Create a pod
	wl := podWorkload()
	wl.Pod.Spec.Containers[0].Ports =
		[]corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}

	// Create a AuthProxyWorkload that matches the deployment
	csqls := []*v1alpha1.AuthProxyWorkload{
		simpleAuthProxy("instance1", "project:server:db"),
		simpleAuthProxy("instance2", "project:server2:db2"),
		simpleAuthProxy("instance3", "project:server3:db3")}

	csqls[0].ObjectMeta.Generation = 1
	csqls[1].ObjectMeta.Generation = 2
	csqls[2].ObjectMeta.Generation = 3
	csqls[2].ObjectMeta.DeletionTimestamp = &now

	// update the containers
	err := configureProxies(u, wl, csqls)
	if err != nil {
		t.Fatal(err)
	}

	// test that annotation was set properly
	if !reflect.DeepEqual(wl.PodTemplateAnnotations(), wantAnnotations) {
		t.Errorf("got %v, want %v for proxy container command", wl.PodTemplateAnnotations(), wantAnnotations)
	}

}

func TestPodAnnotation(t *testing.T) {
	now := metav1.Now()
	server := &v1alpha1.AuthProxyWorkload{ObjectMeta: metav1.ObjectMeta{Name: "instance1", Generation: 1}}
	deletedServer := &v1alpha1.AuthProxyWorkload{ObjectMeta: metav1.ObjectMeta{Name: "instance2", Generation: 2, DeletionTimestamp: &now}}

	var testcases = []struct {
		name  string
		r     *v1alpha1.AuthProxyWorkload
		wantK string
		wantV string
	}{
		{
			name:  "instance1",
			r:     server,
			wantK: "cloudsql.cloud.google.com/instance1",
			wantV: "1",
		}, {
			name:  "instance2",
			r:     deletedServer,
			wantK: "cloudsql.cloud.google.com/instance2",
			wantV: fmt.Sprintf("2-deleted-%s", now.Format(time.RFC3339)),
		},
	}

	for _, tc := range testcases {
		gotK, gotV := workload.PodAnnotation(tc.r)
		if tc.wantK != gotK {
			t.Errorf("got %v, want %v for key", gotK, tc.wantK)
		}
		if tc.wantV != gotV {
			t.Errorf("got %v, want %v for value", gotV, tc.wantV)
		}
	}
}

func TestWorkloadUnixVolume(t *testing.T) {
	var (
		wantsInstanceName    = "project:server:db"
		wantsInstanceName2   = "project:server:db2"
		wantsUnixSocketPath  = "/mnt/db/server"
		wantsUnixSocketPath2 = "/mnt/db/server2"
		wantUnixMountDir     = "/mnt/db"
		wantContainerArgs    = []string{
			fmt.Sprintf("%s?unix-socket-path=%s", wantsInstanceName, wantsUnixSocketPath),
			fmt.Sprintf("%s?unix-socket-path=%s", wantsInstanceName2, wantsUnixSocketPath2),
		}
		wantWorkloadEnv = map[string]string{
			"DB_SOCKET_PATH": wantsUnixSocketPath,
		}
		u = workload.NewUpdater("authproxyworkload/dev")
	)

	// Create a pod
	wl := podWorkload()
	wl.Pod.Spec.Containers[0].Ports =
		[]corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}

	// Create a AuthProxyWorkload that matches the deployment
	csqls := []*v1alpha1.AuthProxyWorkload{
		authProxyWorkload("instance1", []v1alpha1.InstanceSpec{{
			ConnectionString:      wantsInstanceName,
			UnixSocketPath:        wantsUnixSocketPath,
			UnixSocketPathEnvName: "DB_SOCKET_PATH",
		}, {
			ConnectionString:      wantsInstanceName2,
			UnixSocketPath:        wantsUnixSocketPath2,
			UnixSocketPathEnvName: "DB_SOCKET_PATH2",
		}}),
	}

	// update the containers
	err := configureProxies(u, wl, csqls)
	if err != nil {
		t.Fatal(err)
	}

	// ensure that the new container exists
	if len(wl.Pod.Spec.Containers) != 2 {
		t.Fatalf("got %v, wants 1. deployment containers length", len(wl.Pod.Spec.Containers))
	}

	// test that the instancename matches the new expected instance name.
	csqlContainer, err := findContainer(wl, fmt.Sprintf("csql-default-%s", csqls[0].GetName()))
	if err != nil {
		t.Fatal(err)
	}

	// test that port cli args are set correctly
	assertContainerArgsContains(t, csqlContainer.Args, wantContainerArgs)

	// Test that workload has the right env vars
	for wantKey, wantValue := range wantWorkloadEnv {
		gotEnvVar, err := findEnvVar(wl, "busybox", wantKey)
		if err != nil {
			t.Error(err)
			logPodSpec(t, wl)
		} else if gotEnvVar.Value != wantValue {
			t.Errorf("got %v, wants %v workload env var %v", gotEnvVar, wantValue, wantKey)

		}
	}

	// test that Volume exists
	if want, got := 1, len(wl.Pod.Spec.Volumes); want != got {
		t.Fatalf("got %v, wants %v. PodSpec.Volumes", got, want)
	}

	// test that Volume mount exists on busybox
	busyboxContainer, err := findContainer(wl, "busybox")
	if err != nil {
		t.Fatal(err)
	}
	if want, got := 1, len(busyboxContainer.VolumeMounts); want != got {
		t.Fatalf("got %v, wants %v. Busybox Container.VolumeMounts", got, want)
	}
	if want, got := wantUnixMountDir, busyboxContainer.VolumeMounts[0].MountPath; want != got {
		t.Fatalf("got %v, wants %v. Busybox Container.VolumeMounts.MountPath", got, want)
	}
	if want, got := wl.Pod.Spec.Volumes[0].Name, busyboxContainer.VolumeMounts[0].Name; want != got {
		t.Fatalf("got %v, wants %v. Busybox Container.VolumeMounts.MountPath", got, want)
	}

}
