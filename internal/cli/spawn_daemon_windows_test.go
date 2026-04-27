//go:build windows

package cli

import (
	"syscall"
	"testing"
)

func TestDetachedDaemonProcAttrCreatesNewProcessGroup(t *testing.T) {
	attr := detachedDaemonProcAttr()
	if attr == nil {
		t.Fatal("detachedDaemonProcAttr() returned nil; daemon would share its console group with the user's shell")
	}
	if attr.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Fatal("CreationFlags missing CREATE_NEW_PROCESS_GROUP; a Ctrl+C in the user's shell would still hit the daemon")
	}
}
