package main

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestPrivateURLDescriptor(t *testing.T) {
	for _, tc := range []struct {
		sddl    string
		private bool
	}{
		{"O:BA" + privateURLACL, true},
		{"O:SYD:P(A;;FA;;;BA)(A;;FA;;;SY)", true},
		{"O:BUD:P(A;;FA;;;SY)(A;;FA;;;BA)", false},
		{"O:BAD:(A;;FA;;;SY)(A;;FA;;;BA)", false},
		{"O:BAD:P(A;;FA;;;SY)(A;;FA;;;BU)", false},
	} {
		descriptor, err := windows.SecurityDescriptorFromString(tc.sddl)
		if err != nil {
			t.Fatal(err)
		}
		if private := restrictedURLDescriptor(descriptor); private != tc.private {
			t.Errorf("descriptor %q is restricted: got %t, want %t", tc.sddl, private, tc.private)
		}
	}
}
