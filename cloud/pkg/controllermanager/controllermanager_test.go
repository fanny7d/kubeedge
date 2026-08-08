package controllermanager

import "testing"

func TestControllerManagerOptionsEnableLeaderElection(t *testing.T) {
	t.Setenv(podNamespaceEnv, "edge-system")

	opts := newControllerManagerOptions(":19001")

	if !opts.LeaderElection {
		t.Fatal("expected controller-manager leader election to be enabled")
	}
	if opts.LeaderElectionID != controllerManagerLeaderElectionID {
		t.Fatalf("LeaderElectionID = %q, want %q", opts.LeaderElectionID, controllerManagerLeaderElectionID)
	}
	if opts.LeaderElectionNamespace != "edge-system" {
		t.Fatalf("LeaderElectionNamespace = %q, want %q", opts.LeaderElectionNamespace, "edge-system")
	}
	if !opts.LeaderElectionReleaseOnCancel {
		t.Fatal("expected controller-manager to release leadership on shutdown")
	}
	if opts.HealthProbeBindAddress != ":19001" {
		t.Fatalf("HealthProbeBindAddress = %q, want %q", opts.HealthProbeBindAddress, ":19001")
	}
}

func TestControllerManagerLeaderElectionNamespaceFallsBackToSystemNamespace(t *testing.T) {
	t.Setenv(podNamespaceEnv, "")

	if got := controllerManagerLeaderElectionNamespace(); got != "kubeedge" {
		t.Fatalf("controllerManagerLeaderElectionNamespace() = %q, want %q", got, "kubeedge")
	}
}

func TestControllerManagerLeaderElectionNamespaceUsesPodNamespace(t *testing.T) {
	t.Setenv(podNamespaceEnv, "custom-kubeedge")

	if got := controllerManagerLeaderElectionNamespace(); got != "custom-kubeedge" {
		t.Fatalf("controllerManagerLeaderElectionNamespace() = %q, want %q", got, "custom-kubeedge")
	}
}
