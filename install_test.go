package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallerChecksDownloadAndKeepsPreviousBinary(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("release platforms only")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("release architectures only")
	}
	for _, tc := range []struct {
		name                  string
		corrupt, wrongVersion bool
	}{{"valid", false, false}, {"checksum_failure", true, false}, {"version_failure", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "checkout with spaces")
			assets, mocks := filepath.Join(root, "assets"), filepath.Join(root, "tools")
			for _, dir := range []string{filepath.Join(root, "scripts"), filepath.Join(root, "bin"), assets, mocks} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path string, data []byte, mode os.FileMode) {
				t.Helper()
				if err := os.WriteFile(path, data, mode); err != nil {
					t.Fatal(err)
				}
			}
			script, err := os.ReadFile("scripts/install.sh")
			if err != nil {
				t.Fatal(err)
			}
			write(filepath.Join(root, "scripts/install.sh"), script, 0700)
			write(filepath.Join(root, "VERSION"), []byte(version()+"\n"), 0600)
			previous := []byte("#!/bin/sh\necho previous\n")
			write(filepath.Join(root, "bin/herdr-todo"), previous, 0700)
			binaryVersion := version()
			if tc.wrongVersion {
				binaryVersion = "wrong"
			}
			binary := []byte("#!/bin/sh\necho " + binaryVersion + "\n")
			var archive bytes.Buffer
			gz := gzip.NewWriter(&archive)
			tw := tar.NewWriter(gz)
			if err := tw.WriteHeader(&tar.Header{Name: "herdr-todo", Mode: 0755, Size: int64(len(binary))}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write(binary); err != nil {
				t.Fatal(err)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			asset := fmt.Sprintf("herdr-todo_%s_%s_%s.tar.gz", version(), runtime.GOOS, runtime.GOARCH)
			write(filepath.Join(assets, asset), archive.Bytes(), 0600)
			checksum := fmt.Sprintf("%x", sha256.Sum256(archive.Bytes()))
			if tc.corrupt {
				checksum = strings.Repeat("0", 64)
			}
			write(filepath.Join(assets, "SHA256SUMS"), []byte(checksum+"  "+asset+"\n"), 0600)
			// A local fake downloader keeps the verification independent of GitHub.
			write(filepath.Join(mocks, "curl"), []byte(`#!/bin/sh
while [ "$#" -gt 0 ]; do
    case "$1" in --output) shift; output=$1 ;; https://*) url=$1 ;; esac
    shift
done
cp "$HERDR_TEST_ASSETS/${url##*/}" "$output"
`), 0700)
			cmd := exec.Command("sh", filepath.Join(root, "scripts/install.sh"))
			cmd.Env = append(os.Environ(), "PATH="+mocks+string(os.PathListSeparator)+os.Getenv("PATH"), "HERDR_TEST_ASSETS="+assets)
			output, err := cmd.CombinedOutput()
			got, readErr := os.ReadFile(filepath.Join(root, "bin/herdr-todo"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if tc.corrupt || tc.wrongVersion {
				if err == nil || !bytes.Equal(got, previous) {
					t.Fatalf("failed install changed binary: %s %v", output, err)
				}
			} else if err != nil || !bytes.Equal(got, binary) {
				t.Fatalf("install: %s %v", output, err)
			}
			entries, err := os.ReadDir(filepath.Join(root, "bin"))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatal("installer left temporary files")
			}
		})
	}
}
