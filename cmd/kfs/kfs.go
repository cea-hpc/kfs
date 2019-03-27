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
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/cea-hpc/kfs"
	"gopkg.in/yaml.v2"
)

var (
	defaultListenAddr     = ":8080"
	defaultKeytab         = "/etc/krb5.keytab"
	defaultUserFileServer = "kfs-user"
	defaultWWWRoute       = map[string]string{
		"/": "{{HOME}}",
	}
)

// record of each user file server
var userFileServers = make(map[string]*UserFileServer)

type routesMap map[string]string

type serverConfig struct {
	GssapiLibPath  string        `yaml:"gssapi_lib_path"` // Path to gssapi shared library
	Listen         string        // Listen address [host]:port
	Keytab         string        // Path to keytab
	UserFileServer string        `yaml:"user_file_server"` // Path to user file server
	ServiceName    string        `yaml:"service_name"`     // Kerberos service name
	Realms         []string      // Kerberos realms for user authentication
	TLSCertFile    string        `yaml:"tls_cert_file"` // TLS certicate file
	TLSKeyFile     string        `yaml:"tls_key_file"`  // TLS key file
	MaxLifetime    time.Duration `yaml:"max_lifetime"`  // Maximum lifetime of user file server
	Routes         routesMap     // Web routing definition.
}

// key used in context to store application configuration
var configKey = contextKey("config")

// Config returns server configuration stored in context.
func getConfig(ctx context.Context) *serverConfig {
	return ctx.Value(configKey).(*serverConfig)
}

// Fqdn returns the host FQDN or an error if any.
func Fqdn() (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", err
	}

	addrs, err := net.LookupIP(hostname)
	if err != nil {
		return "", err
	}

	for _, addr := range addrs {
		if ipv4 := addr.To4(); ipv4 != nil {
			ip, err := ipv4.MarshalText()
			if err != nil {
				return "", err
			}
			hosts, err := net.LookupAddr(string(ip))
			switch {
			case err != nil:
				return "", err
			case len(hosts) == 0:
				return "", fmt.Errorf("Cannot find DNS address for %s", string(ip))
			}
			fqdn := hosts[0]
			return strings.TrimSuffix(fqdn, "."), nil

		}
	}

	return "", errors.New("Cannot determine FQDN")
}

func loadConfig(filename string) (*serverConfig, error) {
	yamlFile, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	cfg := &serverConfig{}

	if err := yaml.Unmarshal(yamlFile, cfg); err != nil {
		return nil, err
	}

	if cfg.Listen == "" {
		cfg.Listen = defaultListenAddr
	}

	if cfg.Keytab == "" {
		cfg.Keytab = defaultKeytab
	}

	if cfg.UserFileServer == "" {
		cfg.UserFileServer = defaultUserFileServer
	}

	if cfg.Routes == nil {
		cfg.Routes = defaultWWWRoute
	}

	if cfg.ServiceName == "" {
		hostname, err := Fqdn()
		if err != nil {
			return nil, fmt.Errorf("cannot determine hostname: %v", err)
		}
		cfg.ServiceName = fmt.Sprintf("HTTP/%s", hostname)
	}

	if cfg.TLSCertFile == "" {
		return nil, errors.New("no TLS certificate file specified in configuration")
	}

	if cfg.TLSKeyFile == "" {
		return nil, errors.New("no TLS key file specified in configuration")
	}

	if cfg.MaxLifetime < 0 {
		return nil, errors.New("maximum lifetime cannot be a negative number")
	}

	return cfg, nil
}

func internalServerError(w http.ResponseWriter) {
	http.Error(w, "Internal server error: contact your administrator.", http.StatusInternalServerError)
}

func connectHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	server := Server(ctx)
	cred := Credential(ctx)
	cfg := getConfig(ctx)

	krbusername, status, delegatedCred, err := server.Negotiate(cred, r.Header, w.Header())

outerswitch:
	switch {
	case status == http.StatusUnauthorized:
		username, pass, ok := r.BasicAuth()

		if !ok {
			w.Header().Set("WWW-Authenticate", "Negotiate")
			w.Header().Add("WWW-Authenticate", `Basic realm="Please enter your username and password."`)
			http.Error(w, "Unauthorized.", http.StatusUnauthorized)
			return
		}

		usernames := []string{}
		if !strings.Contains(username, "@") && len(cfg.Realms) != 0 {
			for _, realm := range cfg.Realms {
				usernames = append(usernames, fmt.Sprintf("%s@%s", username, realm))
			}
		} else {
			usernames = append(usernames, username)
		}

		for _, krbusername := range usernames {
			delegatedCred, err = server.AuthenticateUserWithPassword(krbusername, pass)
			if err == nil {
				break outerswitch
			}
		}
		log.Printf("ERROR: cannot authenticate user `%s` with password", username)
		http.Error(w, "Unauthorized.", http.StatusUnauthorized)
		return
	case status != http.StatusOK:
		log.Printf("ERROR: SPNEGO negotiate: %v", err)
		internalServerError(w)
		return
	}

	userInfo, err := GetUser(krbusername)
	if err != nil {
		log.Printf("ERROR: GetUser(%s): %v", krbusername, err)
		internalServerError(w)
		return
	}

	if delegatedCred.IsEmpty() {
		log.Printf("ERROR: user %s didn't delegate us their credentials", krbusername)
		internalServerError(w)
		return
	}

	credLifetime, err := GetCredLifetime(delegatedCred)
	if err != nil {
		log.Printf("ERROR: querying lifetime of user %s credential: %v", userInfo.Username, err)
		internalServerError(w)
		return
	}

	krb5ccname, err := SaveCred(userInfo, delegatedCred)
	delegatedCred.Release()
	if err != nil {
		log.Printf("ERROR: saving user %s credential: %v", userInfo.Username, err)
		internalServerError(w)
		return
	}

	fs, ok := userFileServers[userInfo.Username]
	if !ok {
		fs = NewUserFileServer(userInfo, cfg.UserFileServer, cfg.MaxLifetime, cfg.Routes)
		userFileServers[userInfo.Username] = fs
	}

	if fs.Alive {
		fs.NewCredentials(krb5ccname, credLifetime)
	} else {
		if err := fs.Start(krb5ccname, credLifetime); err != nil {
			log.Printf("[%s] ERROR: starting user file server: %v", krbusername, err)
			internalServerError(w)
			return
		}
	}

	log.Printf("[%s] %s %s %s %s\n", krbusername, r.Method, r.URL.Path, r.RemoteAddr, r.UserAgent())

	resp, err := http.Get(fmt.Sprintf("http://%s/%s", fs.Listen, r.URL.Path))
	if err != nil {
		http.Error(w, err.Error(), resp.StatusCode)
		return
	}
	defer resp.Body.Close()

	for hname, hvalue := range resp.Header {
		w.Header().Set(hname, hvalue[0])
		for _, v := range hvalue[1:] {
			w.Header().Add(hname, v)
		}
	}

	w.WriteHeader(resp.StatusCode)

	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("ERROR: io.Copy(): %v", err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: kfs [OPTIONS] /path/to/config")
	fmt.Fprintln(os.Stderr, "\noptions:")
	flag.PrintDefaults()
	os.Exit(2)
}

func main() {
	flag.Usage = usage
	versionFlag := flag.Bool("version", false, "show version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Fprintf(os.Stderr, "kfs version %s\n", kfs.KfsVersion)
		os.Exit(0)
	}

	if flag.NArg() == 0 {
		log.Fatalf("ERROR: no configuration specified")
		usage()
	}

	configFile := flag.Arg(0)
	cfg, err := loadConfig(configFile)
	if err != nil {
		log.Fatalf("ERROR: %v", err)
	}

	// save configuration in main context
	ctx := context.WithValue(context.Background(), configKey, cfg)

	srv := &http.Server{
		Addr: cfg.Listen,
	}

	idleConnsClosed := make(chan struct{})
	go func() {
		sigint := make(chan os.Signal, 1)
		signal.Notify(sigint, os.Interrupt)
		<-sigint

		// We received an interrupt signal, shut down.
		if err := srv.Shutdown(context.Background()); err != nil {
			log.Printf("ERROR: while server shutting down: %v", err)
		}
		close(idleConnsClosed)

		// Close user file servers.
		for _, fs := range userFileServers {
			fs.Shutdown()
		}
	}()

	ctx, err = WithContext(ctx, cfg.Keytab, cfg.ServiceName, cfg.GssapiLibPath)
	if err != nil {
		log.Fatalf("WithContext(): %s", err)
	}

	h := http.HandlerFunc(connectHandler)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(ctx))
	})

	log.Printf("Listening on %s", cfg.Listen)
	exitCode := 0
	if err := srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile); err != http.ErrServerClosed {
		log.Printf("ERROR: %v", err)
		exitCode = 1
	}

	<-idleConnsClosed
	os.Exit(exitCode)
}
