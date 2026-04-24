package release

import "fmt"

const binaryName = "ezoss"

func ArchiveName(version, goos, goarch string) string {
	name := fmt.Sprintf("%s-%s-%s-%s", binaryName, version, goos, goarch)
	if goos == "windows" {
		return name + ".zip"
	}
	return name + ".tar.gz"
}

func DirectoryName(version, goos, goarch string) string {
	return fmt.Sprintf("%s-%s-%s-%s", binaryName, version, goos, goarch)
}

func BinaryName(goos string) string {
	if goos == "windows" {
		return binaryName + ".exe"
	}
	return binaryName
}
