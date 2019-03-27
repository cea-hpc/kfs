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
	"fmt"
	"math/rand"
	"os"
	"os/user"
	"strconv"
	"strings"
	"time"

	"github.com/cea-hpc/gssapi"
	"github.com/cea-hpc/gssapi/spnego"
)

type contextKey string

func (key contextKey) String() string {
	return fmt.Sprintf("kfs/%s", string(key))
}

const (
	serverKey     = contextKey("server")
	credentialKey = contextKey("credential")
)

// Server returns the spnego.KerberizedServer instance stored in context.
func Server(ctx context.Context) spnego.KerberizedServer {
	return ctx.Value(serverKey).(spnego.KerberizedServer)
}

// Credential returns the Kerberos server credentials stored in context.
func Credential(ctx context.Context) *gssapi.CredId {
	return ctx.Value(credentialKey).(*gssapi.CredId)
}

// WithContext adds to the provided context all needed Kerberos parameters: a
// spnego.KerberizedServer instance and the server Kerberos credentials.
// It returns the new context or an error if any.
func WithContext(ctx context.Context, keytab, spn, gssapiLib string) (context.Context, error) {
	gss, err := gssapi.Load(&gssapi.Options{
		LibPath:    gssapiLib,
		Krb5Ktname: keytab,
	})
	if err != nil {
		return ctx, err
	}

	server := spnego.KerberizedServer{Lib: gss}
	ctx = context.WithValue(ctx, serverKey, server)

	cred, err := server.AcquireCred(spn)
	if err != nil {
		return ctx, err
	}

	return context.WithValue(ctx, credentialKey, cred), nil
}

// GetUser returns the user infos deduced from the provided Kerberos username
// (ie. login@REALM) or an error if any.
func GetUser(krbusername string) (*user.User, error) {
	username := strings.SplitN(krbusername, "@", 2)[0]
	return user.Lookup(username)
}

var allowedChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// GetKRB5CCNAME generates a pseudo-random filename in /tmp to store Kerberos
// credentials.
func GetKRB5CCNAME(userInfo *user.User) string {
	krb5ccname := fmt.Sprintf("/tmp/krb5cc_%s_", userInfo.Uid)
	nchars := int32(len(allowedChars))
	for i := 0; i < 10; i++ {
		krb5ccname += string(allowedChars[rand.Int31n(nchars)])
	}
	return krb5ccname
}

// SaveCred saves Kerberos credentials in a file. It returns the name of the
// file or an error if any.
func SaveCred(userInfo *user.User, cred *gssapi.CredId) (string, error) {
	krb5ccname := GetKRB5CCNAME(userInfo)
	if err := cred.Store(krb5ccname); err != nil {
		return "", err
	}

	uid, _ := strconv.Atoi(userInfo.Uid)
	gid, _ := strconv.Atoi(userInfo.Gid)
	if err := os.Chown(krb5ccname, uid, gid); err != nil {
		return "", err
	}

	if err := os.Chmod(krb5ccname, 0600); err != nil {
		return "", err
	}

	return krb5ccname, nil
}

// GetCredLifetime returns the lifetime of the provided credentials or an error
// if any.
func GetCredLifetime(cred *gssapi.CredId) (time.Duration, error) {
	name, lifetime, _, mechanisms, err := cred.Lib.InquireCred(cred)
	name.Release()
	mechanisms.Release()
	return lifetime, err
}
