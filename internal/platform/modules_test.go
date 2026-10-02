package platform

import "testing"

func TestModuleValidates(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatal(err)
	}
}
