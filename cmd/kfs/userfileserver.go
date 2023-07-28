// Copyright 2018-2023 CEA/DAM/DIF
//  Contributor: Arnaud Guignard <arnaud.guignard@cea.fr>
//
// This software is governed by the CeCILL-B license under French law and
// abiding by the rules of distribution of free software.  You can  use,
// modify and/ or redistribute the software under the terms of the CeCILL-B
// license as circulated by CEA, CNRS and INRIA at the following URL
// "http://www.cecill.info".

package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strconv"
	"syscall"
	"time"
)

// Regexps used.
var (
	listenAddressRegexp   = regexp.MustCompile("start listening on (.*)$")
	specialPatternsRegexp = regexp.MustCompile("{{(HOME|USER)}}")
)

// UserFileServer represents a www user file server started with the rights of
// the user.
type UserFileServer struct {
	Listen      string        // listening address
	Alive       bool          // is the server alive?
	credentials string        // credentials
	eol         time.Time     // end of life
	user        *user.User    // owner of process
	timer       *time.Timer   // timer user for shutting down at end of life
	cmd         *exec.Cmd     // user file server command
	cmdPath     string        // path to user file server binary
	maxLifetime time.Duration // max lifetime of server
	routes      routesMap     // web routes
}

// NewUserFileServer returns a new UserFileServer instance initialized with
// user infos, path to the use file server binary and web routes.
func NewUserFileServer(userInfo *user.User, userFileServerPath string, lifetime time.Duration, routes routesMap) *UserFileServer {
	return &UserFileServer{
		Listen:      "",
		Alive:       false,
		credentials: "",
		user:        userInfo,
		timer:       nil,
		cmd:         nil,
		cmdPath:     userFileServerPath,
		maxLifetime: lifetime,
		routes:      routes,
	}
}

func replace(s string, u *user.User) string {
	switch s {
	case "{{HOME}}":
		return u.HomeDir
	case "{{USER}}":
		return u.Username
	}
	return s
}

// Start starts a new HTTP file server as the already defined user. The server
// will listen on localhost on a kernel determined port. It will use the
// provided Kerberos credentials and will live for the provided lifetime.
func (u *UserFileServer) Start(credentials string, lifetime time.Duration) error {
	// Set credentials
	u.NewCredentials(credentials, lifetime)

	// Shutdown the client after lifetime.
	go func() {
		<-u.timer.C
		u.Log("INFO: end of life")
		u.Shutdown()
	}()

	args := make([]string, len(u.routes))
	i := 0
	for pattern, exportedPath := range u.routes {
		args[i] = fmt.Sprintf("%s:%s", pattern,
			specialPatternsRegexp.ReplaceAllStringFunc(exportedPath, func(src string) string {
				return replace(src, u.user)
			}))
		i++
	}

	u.cmd = exec.Command(u.cmdPath, args...)

	// Set user credentials to process.
	uid, _ := strconv.ParseUint(u.user.Uid, 10, 32)
	gid, _ := strconv.ParseUint(u.user.Gid, 10, 32)
	u.cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid:         uint32(uid),
			Gid:         uint32(gid),
			NoSetGroups: false,
		},
	}

	// Will use a pipe to read stdout.
	stdout, err := u.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("setting stdout pipe: %v", err)
	}
	// Read stdout line by line.
	in := bufio.NewScanner(stdout)

	if err := u.cmd.Start(); err != nil {
		return fmt.Errorf("starting command: %v", err)
	}

	// Use a channel to notify when the server is ready.
	started := make(chan struct{})

	go func() {
		// Copy command: there could be a zombie process otherwise if
		// the command is stopped but still running waiting for a
		// download to complete and another command is started during
		// that period. We'll then do the Wait() on the new command
		// instead of the old one, not reaping the first process.
		cmd := u.cmd

		for in.Scan() {
			line := in.Text()
			u.Log(line)
			matches := listenAddressRegexp.FindStringSubmatch(line)
			if matches != nil && matches[1] != "" {
				u.Listen = matches[1]
				u.Alive = true
				close(started)
			}
		}

		if err := in.Err(); err != nil {
			u.Log("ERROR: reading user file server output: %v", err)
		}

		if err := cmd.Wait(); err != nil {
			u.Log("ERROR: waiting for user process to complete: %v", err)
		}

		u.Alive = false
	}()

	select {
	case <-time.After(time.Second):
		u.Shutdown()
		return fmt.Errorf("server not started after 1s")
	case <-started:
		return nil
	}
}

// Shutdown stops the file server and removes the credentials.
func (u *UserFileServer) Shutdown() {
	u.Alive = false
	u.RemoveCredentials()
	u.cmd.Process.Signal(os.Interrupt)
}

// NewCredentials removes the old credentials (if any), then stores the new
// credentials in a file and increases the server lifetime.
func (u *UserFileServer) NewCredentials(credentials string, credLifetime time.Duration) {
	lifetime := credLifetime
	if u.maxLifetime > 0 && u.maxLifetime < credLifetime {
		lifetime = u.maxLifetime
	}
	u.eol = time.Now().Add(lifetime)
	u.Log("set end of life of user file server to %s", u.eol.Format(time.RFC3339))
	if u.timer != nil {
		u.timer.Reset(lifetime)
	} else {
		u.timer = time.NewTimer(lifetime)
	}
	u.RemoveCredentials()
	u.credentials = credentials
}

// RemoveCredentials removes the file where Kerberos credentials were stored.
func (u *UserFileServer) RemoveCredentials() {
	if u.credentials != "" {
		if err := os.Remove(u.credentials); err != nil {
			u.Log("ERROR: cannot remove %s: %v", u.credentials, err)
		}
		u.credentials = ""
	}
}

// Log logs a message with a header indicating the user name.
func (u *UserFileServer) Log(format string, v ...interface{}) {
	newFormat := fmt.Sprintf("[%s] %s", u.user.Username, format)
	log.Printf(newFormat, v...)
}
