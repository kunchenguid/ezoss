package buildinfo

import (
	"runtime/debug"
	"strings"
)

// Set via ldflags at build time:
//
//	-ldflags "-X github.com/kunchenguid/ezoss/internal/buildinfo.Version=v1.0.0
//	          -X github.com/kunchenguid/ezoss/internal/buildinfo.Commit=abc1234
//	          -X github.com/kunchenguid/ezoss/internal/buildinfo.Date=2024-01-01
//	          -X github.com/kunchenguid/ezoss/internal/buildinfo.TelemetryWebsiteID=abc123"
var (
	Version            = "dev"
	Commit             = "unknown"
	Date               = "unknown"
	TelemetryWebsiteID = ""
)

var readBuildInfo = debug.ReadBuildInfo

func CurrentVersion() string {
	if Version != "" {
		return Version
	}
	if info, ok := readBuildInfo(); ok {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			return info.Main.Version
		}
	}
	return "dev"
}

func String() string {
	parts := []string{CurrentVersion()}
	if Commit != "" && Commit != "unknown" {
		parts = append(parts, "("+Commit+")")
	}
	if Date != "" && Date != "unknown" {
		parts = append(parts, Date)
	}
	return strings.Join(parts, " ")
}
