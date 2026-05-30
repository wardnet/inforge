package images

import "testing"

func TestIsValid(t *testing.T) {
	if !IsValid("ubuntu-24.04") {
		t.Error("ubuntu-24.04 should be valid")
	}
	if IsValid("windows-2022") {
		t.Error("windows-2022 should not be valid")
	}
	if IsValid("") {
		t.Error("empty image should not be valid")
	}
}
