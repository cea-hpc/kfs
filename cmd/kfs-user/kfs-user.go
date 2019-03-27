// Copyright 2018-2019 CEA/DAM/DIF
//  Contributor: Arnaud Guignard <arnaud.guignard@cea.fr>
//
// This software is governed by the CeCILL-B license under French law and
// abiding by the rules of distribution of free software.  You can  use,
// modify and/ or redistribute the software under the terms of the CeCILL-B
// license as circulated by CEA, CNRS and INRIA at the following URL
// "http://www.cecill.info".

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/cea-hpc/kfs"
)

func usage() {
	fmt.Fprintln(os.Stderr, "usage: kfs-user [OPTIONS] pattern1:/path/to/exported/fs1 [pattern2:/path/to/exported/fs2 ...]")
	fmt.Fprintln(os.Stderr, "\noptions:")
	flag.PrintDefaults()
	os.Exit(2)
}

// realpath returns the canonical path of the specified filename, eliminating
// any symbolic links encountered in the path.
func realpath(path string) (string, error) {
	path1, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	path2, err := filepath.EvalSymlinks(path1)
	if err != nil {
		return "", err
	}

	return path2, nil
}

// A limitDir is like a http.Dir but limit access to files in it or its
// sub-directories.
type limitDir struct {
	dir string
}

func newLimitDir(path string) (*limitDir, error) {
	if path == "" {
		path = "."
	}
	clean, err := realpath(path)
	if err != nil {
		return nil, err
	}
	return &limitDir{clean}, nil
}

func (d limitDir) Open(name string) (http.File, error) {
	if filepath.Separator != '/' && strings.ContainsRune(name, filepath.Separator) {
		return nil, errors.New("http: invalid character in path")
	}

	fullName := filepath.Join(d.dir, filepath.FromSlash(path.Clean("/"+name)))
	cleanPath, err := realpath(fullName)
	if err != nil {
		fmt.Printf("ERROR: realpath(%s): %v\n", fullName, err)
		return nil, errors.New("http: invalid path")
	}

	if !strings.HasPrefix(cleanPath, d.dir) {
		fmt.Printf("ERROR: %s is outside of allowed path %s: %s\n", name, d.dir, cleanPath)
		return nil, errors.New("http: invalid path")
	}

	f, err := os.Open(cleanPath)
	if err != nil {
		fmt.Printf("ERROR: opening %s: %v\n", cleanPath, err)
		return nil, errors.New("http: error opening file")
	}
	return f, nil
}

func main() {
	flag.Usage = usage
	listenFlag := flag.String("listen", "127.0.0.1:", "listening address")
	versionFlag := flag.Bool("version", false, "show version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Fprintf(os.Stderr, "kfs-user version %s\n", kfs.KfsVersion)
		os.Exit(0)
	}

	if flag.NArg() == 0 {
		fmt.Println("ERROR: no export file-system specified")
		usage()
	}

	for _, arg := range flag.Args() {
		fields := strings.SplitN(arg, ":", 2)
		if len(fields) != 2 {
			fmt.Printf("ERROR: invalid argument: %s\n", arg)
			usage()
		}
		pattern := path.Clean(fields[0])
		if pattern != "/" {
			pattern += "/"
		}
		exportedPath := fields[1]
		fmt.Printf("INFO: exporting \"%s\" to \"%s\"\n", pattern, exportedPath)

		dir, err := newLimitDir(exportedPath)
		if err != nil {
			fmt.Printf("ERROR: invalid argument: %s: %v\n", exportedPath, err)
			os.Exit(2)
		}

		http.Handle(pattern, http.StripPrefix(pattern, http.FileServer(dir)))
	}

	var srv http.Server
	idleConnsClosed := make(chan struct{})
	sigint := make(chan os.Signal, 1)

	go func() {
		signal.Notify(sigint, os.Interrupt, syscall.SIGTERM)
		<-sigint
		fmt.Println("INFO: got SIGINT/SIGTERM signal, quitting")
		if err := srv.Shutdown(context.Background()); err != nil {
			fmt.Printf("ERROR: while server shutting down: %v\n", err)
		}
		close(idleConnsClosed)
	}()

	ln, err := net.Listen("tcp", *listenFlag)
	if err != nil {
		fmt.Printf("ERROR: listening on TCP: %v\n", err)
		os.Exit(2)
	}

	listenAddr := ln.Addr().String()
	fmt.Printf("INFO: start listening on %s\n", listenAddr)

	if err := srv.Serve(ln); err != http.ErrServerClosed {
		fmt.Printf("ERROR: %v\n", err)
	}

	<-idleConnsClosed
}
