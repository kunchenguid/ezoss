//go:build !windows

package cli

import "testing"

func TestDetachedDaemonProcAttrSetsSetsid(t *testing.T) {
	attr := detachedDaemonProcAttr()
	if attr == nil {
		t.Fatal("detachedDaemonProcAttr() returned nil; daemon would inherit user's controlling TTY")
	}
	if !attr.Setsid {
		t.Fatal("Setsid = false; daemon's git/gh children could open /dev/tty and leak credential prompts into user's terminal")
	}
}
