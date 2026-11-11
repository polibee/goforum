package ads

import "testing"

func TestPlacementsAndURLValidation(t *testing.T) {
	if _, ok := PlacementByKey("sidebar"); !ok {
		t.Fatal("sidebar placement should exist")
	}
	if _, ok := PlacementByKey("unknown"); ok {
		t.Fatal("unknown placement should not exist")
	}
	for _, value := range []string{"https://example.com", "http://example.com", "/about"} {
		if !validURL(value) {
			t.Fatalf("%q should be valid", value)
		}
	}
	for _, value := range []string{"javascript:alert(1)", "example.com", ""} {
		if validURL(value) {
			t.Fatalf("%q should be invalid", value)
		}
	}
}

func TestAdLabels(t *testing.T) {
	ad := Ad{Slot: "footer", Format: "html", Status: "pending_review"}
	if ad.SlotLabel() == "" || ad.FormatLabel() == "" || ad.StatusLabel() == "" {
		t.Fatalf("labels empty: %+v", ad)
	}
}
