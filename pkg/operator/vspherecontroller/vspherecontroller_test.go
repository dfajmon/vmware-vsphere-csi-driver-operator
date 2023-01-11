package vspherecontroller

import (
	"context"
	"fmt"
	"testing"
	"time"

	opv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/vmware-vsphere-csi-driver-operator/pkg/operator/testlib"
	"github.com/openshift/vmware-vsphere-csi-driver-operator/pkg/operator/utils"
	"github.com/openshift/vmware-vsphere-csi-driver-operator/pkg/operator/vclib"
	"github.com/openshift/vmware-vsphere-csi-driver-operator/pkg/operator/vspherecontroller/checks"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	testControllerName = "VMwareVSphereController"
)

func newVsphereController(apiClients *utils.APIClient) *VSphereController {
	kubeInformers := apiClients.KubeInformers
	ocpConfigInformer := apiClients.ConfigInformers
	configMapInformer := kubeInformers.InformersFor(cloudConfigNamespace).Core().V1().ConfigMaps()

	infraInformer := ocpConfigInformer.Config().V1().Infrastructures()
	scInformer := kubeInformers.InformersFor("").Storage().V1().StorageClasses()
	csiDriverLister := kubeInformers.InformersFor("").Storage().V1().CSIDrivers().Lister()
	csiNodeLister := kubeInformers.InformersFor("").Storage().V1().CSINodes().Lister()
	nodeLister := apiClients.NodeInformer.Lister()
	rc := events.NewInMemoryRecorder(testControllerName)

	c := &VSphereController{
		name:            testControllerName,
		targetNamespace: defaultNamespace,
		kubeClient:      apiClients.KubeClient,
		operatorClient:  apiClients.OperatorClient,
		configMapLister: configMapInformer.Lister(),
		secretLister:    apiClients.SecretInformer.Lister(),
		csiNodeLister:   csiNodeLister,
		scLister:        scInformer.Lister(),
		csiDriverLister: csiDriverLister,
		nodeLister:      nodeLister,
		apiClients:      *apiClients,
		eventRecorder:   rc,
		vSphereChecker:  newVSphereEnvironmentChecker(),
		infraLister:     infraInformer.Lister(),
	}
	c.controllers = []conditionalController{}
	c.storageClassController = &dummyStorageClassController{syncCalled: 0}
	return c
}

type dummyStorageClassController struct {
	syncCalled int
}

func (c *dummyStorageClassController) Sync(ctx context.Context, connection *vclib.VSphereConnection, apiDeps checks.KubeAPIInterface) error {
	c.syncCalled += 1
	return nil
}

func TestSync(t *testing.T) {
	tests := []struct {
		name                         string
		clusterCSIDriverObject       *testlib.FakeDriverInstance
		initialObjects               []runtime.Object
		configObjects                runtime.Object
		vcenterVersion               string
		startingNodeHardwareVersions []string
		finalNodeHardwareVersions    []string
		expectedConditions           []opv1.OperatorCondition
		expectError                  error
		failVCenterConnection        bool
		operandStarted               bool
		storageClassCreated          bool
	}{
		{
			name:                         "when all configuration is right",
			clusterCSIDriverObject:       testlib.MakeFakeDriverInstance(),
			vcenterVersion:               "7.0.2",
			startingNodeHardwareVersions: []string{"vmx-15", "vmx-15"},
			initialObjects:               []runtime.Object{testlib.GetConfigMap(), testlib.GetSecret()},
			configObjects:                runtime.Object(testlib.GetInfraObject()),
			expectedConditions: []opv1.OperatorCondition{
				{
					Type:   testControllerName + opv1.OperatorStatusTypeAvailable,
					Status: opv1.ConditionTrue,
				},
				{
					Type:   testControllerName + opv1.OperatorStatusTypeUpgradeable,
					Status: opv1.ConditionTrue,
				},
			},
			operandStarted:      true,
			storageClassCreated: true,
		},
		{
			name:                         "when we can't connect to vcenter",
			clusterCSIDriverObject:       testlib.MakeFakeDriverInstance(),
			vcenterVersion:               "7.0.2",
			startingNodeHardwareVersions: []string{"vmx-15", "vmx-15"},
			initialObjects:               []runtime.Object{testlib.GetConfigMap(), testlib.GetSecret()},
			configObjects:                runtime.Object(testlib.GetInfraObject()),
			failVCenterConnection:        true,
			expectedConditions: []opv1.OperatorCondition{
				{
					Type:   testControllerName + opv1.OperatorStatusTypeAvailable,
					Status: opv1.ConditionTrue,
				},
				{
					Type:   testControllerName + opv1.OperatorStatusTypeUpgradeable,
					Status: opv1.ConditionUnknown,
				},
			},
			operandStarted:      false,
			storageClassCreated: false,
		},
		{
			name:                         "when we can't connect to vcenter but CSI driver was installed previously, degrade cluster",
			clusterCSIDriverObject:       testlib.MakeFakeDriverInstance(),
			vcenterVersion:               "7.0.2",
			startingNodeHardwareVersions: []string{"vmx-15", "vmx-15"},
			initialObjects:               []runtime.Object{testlib.GetConfigMap(), testlib.GetSecret(), testlib.GetCSIDriver(true /*withOCPAnnotation*/)},
			configObjects:                runtime.Object(testlib.GetInfraObject()),
			failVCenterConnection:        true,
			expectError:                  fmt.Errorf("can't talk to vcenter"),
			operandStarted:               true,
			storageClassCreated:          false,
		},
		{
			name:                         "when vcenter version is older, block upgrades",
			clusterCSIDriverObject:       testlib.MakeFakeDriverInstance(),
			startingNodeHardwareVersions: []string{"vmx-15", "vmx-15"},
			initialObjects:               []runtime.Object{testlib.GetConfigMap(), testlib.GetSecret()},
			configObjects:                runtime.Object(testlib.GetInfraObject()),
			expectedConditions: []opv1.OperatorCondition{
				{
					Type:   testControllerName + opv1.OperatorStatusTypeAvailable,
					Status: opv1.ConditionTrue,
				},
				{
					Type:   testControllerName + opv1.OperatorStatusTypeUpgradeable,
					Status: opv1.ConditionFalse,
				},
			},
			operandStarted:      false,
			storageClassCreated: false,
		},
		{
			name:                         "when vcenter version is older but csi driver exists, degrade cluster",
			clusterCSIDriverObject:       testlib.MakeFakeDriverInstance(),
			startingNodeHardwareVersions: []string{"vmx-15", "vmx-15"},
			initialObjects:               []runtime.Object{testlib.GetConfigMap(), testlib.GetSecret(), testlib.GetCSIDriver(true)},
			configObjects:                runtime.Object(testlib.GetInfraObject()),
			expectError:                  fmt.Errorf("found older vcenter version, expected is 6.7.3"),
			operandStarted:               true,
			storageClassCreated:          false,
		},
		{
			name:                         "when all configuration is right, but an existing upstream CSI driver exists",
			clusterCSIDriverObject:       testlib.MakeFakeDriverInstance(),
			vcenterVersion:               "7.0.2",
			startingNodeHardwareVersions: []string{"vmx-15", "vmx-15"},
			initialObjects:               []runtime.Object{testlib.GetConfigMap(), testlib.GetSecret(), testlib.GetCSIDriver(false)},
			configObjects:                runtime.Object(testlib.GetInfraObject()),
			expectedConditions: []opv1.OperatorCondition{
				{
					Type:   testControllerName + opv1.OperatorStatusTypeAvailable,
					Status: opv1.ConditionTrue,
				},
				{
					Type:   testControllerName + opv1.OperatorStatusTypeUpgradeable,
					Status: opv1.ConditionTrue,
				},
			},
			operandStarted:      false,
			storageClassCreated: false,
		},
		{
			name:                         "when all configuration is right, but an existing upstream CSI node object exists",
			clusterCSIDriverObject:       testlib.MakeFakeDriverInstance(),
			vcenterVersion:               "7.0.2",
			startingNodeHardwareVersions: []string{"vmx-15", "vmx-15"},
			initialObjects:               []runtime.Object{testlib.GetConfigMap(), testlib.GetSecret(), testlib.GetCSINode()},
			configObjects:                runtime.Object(testlib.GetInfraObject()),
			expectedConditions: []opv1.OperatorCondition{
				{
					Type:   testControllerName + opv1.OperatorStatusTypeAvailable,
					Status: opv1.ConditionTrue,
				},
				{
					Type:   testControllerName + opv1.OperatorStatusTypeUpgradeable,
					Status: opv1.ConditionTrue,
				},
			},
			operandStarted:      false,
			storageClassCreated: false,
		},
		{
			name:                         "when node hw-version was old first and got upgraded",
			clusterCSIDriverObject:       testlib.MakeFakeDriverInstance(),
			initialObjects:               []runtime.Object{testlib.GetConfigMap(), testlib.GetSecret()},
			vcenterVersion:               "7.0.2",
			startingNodeHardwareVersions: []string{"vmx-13", "vmx-15"},
			finalNodeHardwareVersions:    []string{"vmx-15", "vmx-15"},
			configObjects:                runtime.Object(testlib.GetInfraObject()),
			expectedConditions: []opv1.OperatorCondition{
				{
					Type:   testControllerName + opv1.OperatorStatusTypeAvailable,
					Status: opv1.ConditionTrue,
				},
				{
					Type:   testControllerName + opv1.OperatorStatusTypeUpgradeable,
					Status: opv1.ConditionTrue,
				},
			},
			operandStarted:      true,
			storageClassCreated: false,
		},
	}

	for i := range tests {
		test := tests[i]
		t.Run(test.name, func(t *testing.T) {
			nodes := defaultNodes()
			for _, node := range nodes {
				test.initialObjects = append(test.initialObjects, runtime.Object(node))
			}

			commonApiClient := testlib.NewFakeClients(test.initialObjects, test.clusterCSIDriverObject, test.configObjects)
			stopCh := make(chan struct{})
			defer close(stopCh)

			go testlib.StartFakeInformer(commonApiClient, stopCh)
			if err := testlib.AddInitialObjects(test.initialObjects, commonApiClient); err != nil {
				t.Fatalf("error adding initial objects: %v", err)
			}

			testlib.WaitForSync(commonApiClient, stopCh)

			ctrl := newVsphereController(commonApiClient)
			scController := ctrl.storageClassController.(*dummyStorageClassController)

			var cleanUpFunc func()
			var conn *vclib.VSphereConnection
			var connError error
			conn, cleanUpFunc, connError = setupSimulator(defaultModel)
			if test.vcenterVersion != "" {
				customizeVCenterVersion(test.vcenterVersion, test.vcenterVersion, conn)
			}
			ctrl.vsphereConnectionFunc = makeVsphereConnectionFunc(conn, test.failVCenterConnection, connError)
			defer func() {
				if cleanUpFunc != nil {
					cleanUpFunc()
				}
			}()
			err := setHardwareVersionsFunc(nodes, conn, test.startingNodeHardwareVersions)()
			if err != nil {
				t.Fatalf("error setting hardware version for node %s", nodes[0].Name)
			}

			// Set esxi version of the only host.
			err = customizeHostVersion(defaultHostId, "7.0.2")
			if err != nil {
				t.Fatalf("Failed to customize host: %s", err)
			}

			err = ctrl.sync(context.TODO(), factory.NewSyncContext("vsphere-controller", ctrl.eventRecorder))
			if test.expectError == nil && err != nil {
				t.Fatalf("Unexpected error that could degrade cluster: %+v", err)
			}

			// check storageclass results
			if test.storageClassCreated && scController.syncCalled == 0 {
				t.Fatalf("expected storageclass to be created")
			}

			if !test.storageClassCreated && scController.syncCalled > 0 {
				t.Fatalf("unexpected storageclass created")
			}

			if test.expectError != nil && err == nil {
				t.Fatalf("expected cluster to be degraded with: %v, got none", test.expectError)
			}
			// if hardware version changes between the syncs lets rerun sync again
			if len(test.finalNodeHardwareVersions) > 0 {
				err = adjustConditionsAndResync(setHardwareVersionsFunc(nodes, conn, test.finalNodeHardwareVersions), ctrl)
			}

			_, status, _, err := ctrl.operatorClient.GetOperatorState()
			if err != nil {
				t.Errorf("failed to get operator state: %+v", err)
			}
			for i := range test.expectedConditions {
				expectedCondition := test.expectedConditions[i]
				matchingCondition := testlib.GetMatchingCondition(status.Conditions, expectedCondition.Type)
				if matchingCondition == nil {
					t.Fatalf("found no matching condition for: %s", expectedCondition.Type)
				}
				if matchingCondition.Status != expectedCondition.Status {
					t.Fatalf("for condition %s: expected status: %v, got: %v", expectedCondition.Type, expectedCondition.Status, matchingCondition.Status)
				}
			}

			if test.operandStarted != ctrl.operandControllerStarted {
				t.Fatalf("expected operandStarted to be %v, got %v", test.operandStarted, ctrl.operandControllerStarted)
			}
		})
	}
}

func setHardwareVersionsFunc(nodes []*v1.Node, conn *vclib.VSphereConnection, hardwareVersions []string) func() error {
	return func() error {
		for i := range nodes {
			err := setHWVersion(conn, nodes[i], hardwareVersions[i])
			if err != nil {
				return err
			}
		}
		return nil
	}
}

func adjustConditionsAndResync(modifierFunc func() error, ctrl *VSphereController) error {
	err := modifierFunc()
	if err != nil {
		return err
	}
	envChecker, _ := ctrl.vSphereChecker.(*vSphereEnvironmentCheckerComposite)
	envChecker.nextCheck = time.Now()
	return ctrl.sync(context.TODO(), factory.NewSyncContext("vsphere-controller", ctrl.eventRecorder))
}

func makeVsphereConnectionFunc(conn *vclib.VSphereConnection, failConnection bool, connError error) func() (*vclib.VSphereConnection, checks.ClusterCheckResult, bool) {
	return func() (*vclib.VSphereConnection, checks.ClusterCheckResult, bool) {
		if failConnection {
			err := fmt.Errorf("connection to vcenter failed")
			result := checks.ClusterCheckResult{
				CheckError:  err,
				Action:      checks.CheckActionBlockUpgrade,
				CheckStatus: checks.CheckStatusVSphereConnectionFailed,
				Reason:      fmt.Sprintf("Failed to connect to vSphere: %v", err),
			}
			return nil, result, false
		} else {
			if connError != nil {
				return nil, checks.MakeGenericVCenterAPIError(connError), false
			}
			return conn, checks.MakeClusterCheckResultPass(), false
		}
	}

}

func TestAddUpgradeableBlockCondition(t *testing.T) {
	controllerName := "VSphereController"
	conditionType := controllerName + opv1.OperatorStatusTypeUpgradeable

	tests := []struct {
		name              string
		clusterCSIDriver  *testlib.FakeDriverInstance
		clusterResult     checks.ClusterCheckResult
		expectedCondition opv1.OperatorCondition
		conditionModified bool
	}{
		{
			name:             "when no existing condition is found, should add condition",
			clusterCSIDriver: testlib.MakeFakeDriverInstance(),
			clusterResult:    testlib.GetTestClusterResult(checks.CheckStatusVSphereConnectionFailed),
			expectedCondition: opv1.OperatorCondition{
				Type:   conditionType,
				Status: opv1.ConditionFalse,
				Reason: string(checks.CheckStatusVSphereConnectionFailed),
			},
			conditionModified: true,
		},
		{
			name: "when an existing condition is found, should not modify condition",
			clusterCSIDriver: testlib.MakeFakeDriverInstance(func(instance *testlib.FakeDriverInstance) *testlib.FakeDriverInstance {
				instance.Status.Conditions = []opv1.OperatorCondition{
					{
						Type:   conditionType,
						Status: opv1.ConditionFalse,
						Reason: string(checks.CheckStatusVSphereConnectionFailed),
					},
				}
				return instance
			}),
			clusterResult: testlib.GetTestClusterResult(checks.CheckStatusVSphereConnectionFailed),
			expectedCondition: opv1.OperatorCondition{
				Type:   conditionType,
				Status: opv1.ConditionFalse,
				Reason: string(checks.CheckStatusVSphereConnectionFailed),
			},
			conditionModified: false,
		},
		{
			name: "when an existing condition is found not has different reason, should modify condition",
			clusterCSIDriver: testlib.MakeFakeDriverInstance(func(instance *testlib.FakeDriverInstance) *testlib.FakeDriverInstance {
				instance.Status.Conditions = []opv1.OperatorCondition{
					{
						Type:   conditionType,
						Status: opv1.ConditionFalse,
						Reason: string(checks.CheckStatusDeprecatedVCenter),
					},
				}
				return instance
			}),
			clusterResult: testlib.GetTestClusterResult(checks.CheckStatusVSphereConnectionFailed),
			expectedCondition: opv1.OperatorCondition{
				Type:   conditionType,
				Status: opv1.ConditionFalse,
				Reason: string(checks.CheckStatusVSphereConnectionFailed),
			},
			conditionModified: true,
		},
	}

	for i := range tests {
		test := tests[i]
		t.Run(test.name, func(t *testing.T) {
			commonApiClient := testlib.NewFakeClients([]runtime.Object{}, test.clusterCSIDriver, testlib.GetInfraObject())
			stopCh := make(chan struct{})
			defer close(stopCh)

			go testlib.StartFakeInformer(commonApiClient, stopCh)
			if err := testlib.AddInitialObjects([]runtime.Object{}, commonApiClient); err != nil {
				t.Fatalf("error adding initial objects: %v", err)
			}

			testlib.WaitForSync(commonApiClient, stopCh)

			ctrl := newVsphereController(commonApiClient)
			condition, modified := ctrl.addUpgradeableBlockCondition(test.clusterResult, controllerName, &test.clusterCSIDriver.Status, opv1.ConditionFalse)
			if modified != test.conditionModified {
				t.Fatalf("expected modified condition to be %v, got %v", test.conditionModified, modified)
			}
			if condition.Type != test.expectedCondition.Type ||
				condition.Status != test.expectedCondition.Status ||
				condition.Reason != test.expectedCondition.Reason {
				t.Fatalf("expected condition to be %+v, got %+v", test.expectedCondition, condition)
			}
		})

	}
}
