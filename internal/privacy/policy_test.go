package privacy

import "testing"

func TestPolicyDisablesDurableExcerptWithoutDeletingIdentityEvidence(t *testing.T) {
	policy, err := NewPolicy(PolicyVersionV1, AnchorExcerptNone, 14)
	if err != nil {
		t.Fatalf("NewPolicy() error = %v", err)
	}
	if policy.StoresAnchorExcerpt() || policy.SupportsReattachment() {
		t.Fatal("none retention still permits durable excerpt reattachment")
	}
	if policy.Allows(ValueSourceExcerpt, DestinationAnchorExcerpt) {
		t.Fatal("source excerpt crossed disabled anchor-excerpt boundary")
	}
	if !policy.Allows(ValueIdentifier, DestinationDurableMetadata) {
		t.Fatal("stable identifier was rejected from durable metadata")
	}
}

func TestPolicyRejectsSensitiveDestinations(t *testing.T) {
	policy := DefaultPolicy()
	if policy.Allows(ValueCredential, DestinationProviderContext) {
		t.Fatal("credential admitted to provider context")
	}
	if policy.Allows(ValueCommandOutput, DestinationOperationalLog) {
		t.Fatal("raw command output admitted to operational log")
	}
	if policy.Allows(ValueSourceExcerpt, DestinationOperationalLog) {
		t.Fatal("source excerpt admitted to operational log")
	}
}

func TestNormalizeClassUsesClosedVocabulary(t *testing.T) {
	if _, err := NormalizeClass("source_excerpt"); err != nil {
		t.Fatalf("NormalizeClass() error = %v", err)
	}
	if _, err := NormalizeClass("prompt-body"); err == nil {
		t.Fatal("unknown privacy class accepted")
	}
}

func TestSensitiveValueCarriesOnlyItsPrivacyClass(t *testing.T) {
	value, err := NewSensitiveValue(ValuePrompt)
	if err != nil {
		t.Fatalf("NewSensitiveValue() error = %v", err)
	}
	if value.Class() != ValuePrompt || !DefaultPolicy().Allowed(value, DestinationProviderContext) {
		t.Fatalf("sensitive value class was not authorized")
	}
}

func TestProtectedRootEvidenceDoesNotConfuseRepairWithHistoricalPrivacy(t *testing.T) {
	secure := ProtectedRootEvidence{State: ProtectedRootSecure, OwnerOnlyAtCreation: true, CurrentlyOwnerOnly: true}
	if err := secure.Validate(); err != nil {
		t.Fatalf("secure evidence error = %v", err)
	}
	weak := ProtectedRootEvidence{State: ProtectedRootLegacyWeak, CurrentlyOwnerOnly: true, RepairEligible: true}
	if err := weak.Validate(); err != nil {
		t.Fatalf("legacy evidence error = %v", err)
	}
	if (ProtectedRootEvidence{State: ProtectedRootSecure, CurrentlyOwnerOnly: true}).Validate() == nil {
		t.Fatal("secure evidence omitted creation proof")
	}
}
