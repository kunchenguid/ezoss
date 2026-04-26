package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "archive" {
		fmt.Fprintln(os.Stderr, "usage: releasecmd archive -format tar.gz|zip -source <dir> -output <path>")
		os.Exit(2)
	}

	fs := flag.NewFlagSet("archive", flag.ExitOnError)
	format := fs.String("format", "", "archive format: tar.gz or zip")
	source := fs.String("source", "", "source directory")
	output := fs.String("output", "", "output archive path")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if *source == "" || *output == "" {
		fmt.Fprintln(os.Stderr, "source and output are required")
		os.Exit(2)
	}

	var err error
	switch *format {
	case "tar.gz":
		err = writeTarGz(*source, *output)
	case "zip":
		err = writeZip(*source, *output)
	default:
		err = fmt.Errorf("unsupported archive format %q", *format)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func writeTarGz(source, output string) error {
	out, err := os.Create(output)
	if err != nil {
		return err
	}
	defer out.Close()

	gzw := gzip.NewWriter(out)
	defer gzw.Close()

	tw := tar.NewWriter(gzw)
	defer tw.Close()

	return walkFiles(source, func(path, name string, info os.FileInfo) error {
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = name
		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()

		_, err = io.Copy(tw, in)
		return err
	})
}

func writeZip(source, output string) error {
	out, err := os.Create(output)
	if err != nil {
		return err
	}
	defer out.Close()

	zw := zip.NewWriter(out)
	defer zw.Close()

	return walkFiles(source, func(path, name string, info os.FileInfo) error {
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = name
		header.Method = zip.Deflate

		entry, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}

		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()

		_, err = io.Copy(entry, in)
		return err
	})
}

func walkFiles(source string, visit func(path, name string, info os.FileInfo) error) error {
	source = filepath.Clean(source)
	base := filepath.Base(source)

	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(filepath.Join(base, rel))
		name = strings.TrimPrefix(name, "./")
		return visit(path, name, info)
	})
}
