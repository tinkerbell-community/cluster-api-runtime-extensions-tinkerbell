package janitor

import (
	"testing"

	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// hardwareWith builds a Hardware in the given predicate state.
func hardwareWith(ownerLabels bool, userData string, provisioned bool) *tinkv1.Hardware {
	hw := &tinkv1.Hardware{ObjectMeta: metav1.ObjectMeta{Name: "hw-1", Namespace: "tinkerbell"}}
	if ownerLabels {
		hw.Labels = map[string]string{
			OwnerNameLabel:      "machine-1",
			OwnerNamespaceLabel: "tinkerbell",
		}
	}
	if userData != "" {
		hw.Spec.UserData = ptr.To(userData)
	}
	if provisioned {
		hw.Annotations = map[string]string{ProvisionedAnnotation: "true"}
	}
	return hw
}

func TestClassifyTruthTable(t *testing.T) {
	const config = "#cloud-config"
	cases := []struct {
		name string
		hw   *tinkv1.Hardware
		want Class
	}{
		{"claimed", hardwareWith(true, config, false), ClassClaimed},
		{"claimed no userData", hardwareWith(true, "", false), ClassClaimed},
		{"claimed provisioned", hardwareWith(true, config, true), ClassClaimed},
		{"claimed provisioned no userData", hardwareWith(true, "", true), ClassClaimed},
		{"never claimed", hardwareWith(false, "", false), ClassNeverClaimed},
		{"bootstrap reserved", hardwareWith(false, "", true), ClassBootstrapReserved},
		{"half released", hardwareWith(false, config, true), ClassHalfReleased},
		{"released", hardwareWith(false, config, false), ClassReleased},
	}
	for _, tc := range cases {
		if got := Classify(tc.hw); got != tc.want {
			t.Errorf("%s: Classify = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestClassifySingleOwnerLabelIsClaimed(t *testing.T) {
	// Either owner label alone means CAPT is (or was, mid-patch) involved:
	// never touch.
	hw := hardwareWith(false, "#cloud-config", false)
	hw.Labels = map[string]string{OwnerNameLabel: "machine-1"}
	if got := Classify(hw); got != ClassClaimed {
		t.Errorf("ownerName alone: Classify = %v, want Claimed", got)
	}
	hw.Labels = map[string]string{OwnerNamespaceLabel: "tinkerbell"}
	if got := Classify(hw); got != ClassClaimed {
		t.Errorf("ownerNamespace alone: Classify = %v, want Claimed", got)
	}
}

func TestClassifyWhitespaceUserDataIsEmpty(t *testing.T) {
	hw := hardwareWith(false, "  \n\t ", false)
	if got := Classify(hw); got != ClassNeverClaimed {
		t.Errorf("whitespace-only userData: Classify = %v, want NeverClaimed", got)
	}
}

// TestClassifyBootstrapNodeFixture mirrors the terraform-created bootstrap
// node exactly: never claimed, allowPXE=false, provisioned annotation
// pre-set, no userData (its config was applied over the network). It must
// fail TWO independent conjuncts.
func TestClassifyBootstrapNodeFixture(t *testing.T) {
	hw := hardwareWith(false, "", true)
	hw.Spec.Interfaces = []tinkv1.Interface{{
		Netboot: &tinkv1.Netboot{AllowPXE: ptr.To(false)},
		DHCP:    &tinkv1.DHCP{MAC: "88:a2:9e:87:76:6c"},
	}}
	if got := Classify(hw); got != ClassBootstrapReserved {
		t.Fatalf("bootstrap fixture: Classify = %v, want BootstrapReserved", got)
	}
	// Even if the annotation were lost, the missing userData still protects it.
	hw.Annotations = nil
	if got := Classify(hw); got != ClassNeverClaimed {
		t.Errorf("bootstrap fixture without annotation: Classify = %v, want NeverClaimed (still no scrub)", got)
	}
}
