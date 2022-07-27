package main

import (
	"os"
	"testing"

	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/envfuncs"
)

var testenv env.Environment

func TestMain(m *testing.M) {
	testenv = env.New()
	kindClusterName := envconf.RandomName("etcdmon", 16)
	testenv.Setup(
		envfuncs.CreateKindCluster(kindClusterName),
	)
	testenv.Finish(
		envfuncs.DestroyKindCluster(kindClusterName),
	)
	os.Exit(testenv.Run(m))
}
