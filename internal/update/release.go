package update

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

const checksumsAssetName = "checksums.txt"

var allowInsecureDownloads bool

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func pickReleaseAssets(app, tag string, assets []releaseAsset, platform platformSpec) (releaseAsset, releaseAsset, error) {
	archiveName := releaseArchiveName(app, tag, platform)
	var archive releaseAsset
	var checksums releaseAsset
	for _, asset := range assets {
		switch asset.Name {
		case archiveName:
			archive = asset
		case checksumsAssetName:
			checksums = asset
		}
	}
	if archive.Name == "" {
		return releaseAsset{}, releaseAsset{}, fmt.Errorf("release asset not found: %s", archiveName)
	}
	if checksums.Name == "" {
		return releaseAsset{}, releaseAsset{}, fmt.Errorf("release asset not found: %s", checksumsAssetName)
	}
	return archive, checksums, nil
}

func parseChecksums(data []byte) (map[string]string, error) {
	checksums := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			return nil, fmt.Errorf("parse checksums: malformed line %q", line)
		}
		checksums[parts[1]] = parts[0]
	}
	return checksums, nil
}

func verifyChecksum(data []byte, want string) error {
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return fmt.Errorf("checksum mismatch: got %s want %s", got, want)
	}
	return nil
}

func ensureHTTPS(rawURL string) error {
	if allowInsecureDownloads {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("reject non-https url: %s", rawURL)
	}
	return nil
}
