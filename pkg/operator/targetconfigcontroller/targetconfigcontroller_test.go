package targetconfigcontroller

import (
	"testing"

	opv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/vmware-vsphere-csi-driver-operator/assets"
	"github.com/openshift/vmware-vsphere-csi-driver-operator/pkg/operator/testlib"
	"github.com/openshift/vmware-vsphere-csi-driver-operator/pkg/operator/utils"
	iniv1 "gopkg.in/ini.v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestApplyClusterCSIDriverChange(t *testing.T) {
	tests := []struct {
		name               string
		clusterCSIDriver   *opv1.ClusterCSIDriver
		operatorObj        *testlib.FakeDriverInstance
		expectedTopology   string
		configFileName     string
		expectedDatacenter string
		expectError        bool
	}{
		{
			name:               "when driver does not have topology enabled",
			clusterCSIDriver:   testlib.GetClusterCSIDriver(false),
			operatorObj:        testlib.MakeFakeDriverInstance(),
			expectedDatacenter: "Datacenter",
			expectedTopology:   "",
		},
		{
			name:               "when driver does have topology enabled",
			clusterCSIDriver:   testlib.GetClusterCSIDriver(true),
			operatorObj:        testlib.MakeFakeDriverInstance(),
			expectedDatacenter: "Datacenter",
			expectedTopology:   "k8s-zone,k8s-region",
		},
		{
			name:             "when configuration has more than one vcenter",
			clusterCSIDriver: testlib.GetClusterCSIDriver(true),
			operatorObj:      testlib.MakeFakeDriverInstance(),
			configFileName:   "multiple_vc.ini",
			expectError:      true,
		},
		{
			name:               "when configuration has more than one datacenter",
			clusterCSIDriver:   testlib.GetClusterCSIDriver(true),
			operatorObj:        testlib.MakeFakeDriverInstance(),
			configFileName:     "multiple_dc.ini",
			expectedDatacenter: "Datacentera, DatacenterB",
			expectedTopology:   "k8s-zone,k8s-region",
		},
	}

	for i := range tests {
		tc := tests[i]
		t.Run(tc.name, func(t *testing.T) {
			infra := testlib.GetInfraObject()
			commonApiClient := testlib.NewFakeClients([]runtime.Object{}, tc.operatorObj, infra)
			testlib.AddClusterCSIDriverClient(commonApiClient, tc.clusterCSIDriver)
			stopCh := make(chan struct{})
			defer close(stopCh)

			go testlib.StartFakeInformer(commonApiClient, stopCh)
			if err := testlib.AddInitialObjects([]runtime.Object{tc.clusterCSIDriver}, commonApiClient); err != nil {
				t.Fatalf("error adding initial objects: %v", err)
			}

			cloudConfigBytes, err := assets.ReadFile("vsphere_cloud_config.yaml")
			if err != nil {
				t.Fatalf("error reading vsphere cloud config file: %v", err)
			}

			csiConfigBytes, err := assets.ReadFile("csi_cloud_config.ini")
			if err != nil {
				t.Fatalf("error reading csi configuration file: %v", err)
			}

			testlib.WaitForSync(commonApiClient, stopCh)

			configMapInformer := commonApiClient.KubeInformers.InformersFor(cloudConfigNamespace).Core().V1().ConfigMaps()
			infraInformer := commonApiClient.ConfigInformers.Config().V1().Infrastructures()
			ctrl := &TargetConfigController{
				name:                   "VMwareVSphereDriverTargetConfigController",
				targetNamespace:        utils.DefaultNamespace,
				manifest:               cloudConfigBytes,
				csiConfigManifest:      csiConfigBytes,
				kubeClient:             commonApiClient.KubeClient,
				operatorClient:         commonApiClient.OperatorClient,
				configMapLister:        configMapInformer.Lister(),
				infraLister:            infraInformer.Lister(),
				clusterCSIDriverLister: commonApiClient.ClusterCSIDriverInformer.Lister(),
			}
			legacyVsphereConfig, err := testlib.GetLegacyVSphereConfig(tc.configFileName)
			if err != nil {
				t.Fatalf("error loading legacy vsphere config: %v", err)
			}

			configMap, err := ctrl.applyClusterCSIDriverChange(infra, legacyVsphereConfig, tc.clusterCSIDriver)

			// if we expected error and we got some, we should stop running this test
			if tc.expectError && err != nil {
				return
			}

			if tc.expectError && err == nil {
				t.Fatal("Expected error got none")
			}
			if err != nil {
				t.Fatalf("error creating configmap: %v", err)
			}

			configMapIni := configMap.Data["cloud.conf"]
			csiConfig, err := iniv1.Load([]byte(configMapIni))
			if err != nil {
				t.Fatalf("error loading result ini: %v", err)
			}

			labelSection, err := csiConfig.Section("Labels").GetKey("topology-categories")
			if tc.expectedTopology == "" && labelSection != nil {
				t.Fatalf("unexpected topology found %v", labelSection)
			}
			if tc.expectedTopology != "" {
				if labelSection == nil || labelSection.String() != tc.expectedTopology {
					t.Fatalf("expected topology %v, unexpected topology found %v", tc.expectedTopology, labelSection)
				}
			}
			datacenters, err := csiConfig.Section("VirtualCenter \"foobar.lan\"").GetKey("datacenters")
			if datacenters.String() != tc.expectedDatacenter {
				t.Fatalf("expected datacenter to be %s, got %s", tc.expectedDatacenter, datacenters.String())
			}

		})
	}

}
