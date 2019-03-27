KFS_GIT_URL	?= github.com/cea-hpc/kfs

prefix		?= /usr
sbindir		?= $(prefix)/sbin
datarootdir	?= $(prefix)/share
mandir		?= $(datarootdir)/man

GO		?= go

KFS_SRC		= $(wildcard cmd/kfs/*.go)
KFS_USER_SRC	= $(wildcard cmd/kfs-user/*.go)

EXE		= $(addprefix bin/, kfs kfs-user)

all: exe

exe: $(EXE)

bin/kfs: $(KFS_SRC)
	$(GO) build -o $@ $(KFS_GIT_URL)/cmd/kfs

bin/kfs-user: $(KFS_USER_SRC)
	$(GO) build -o $@ $(KFS_GIT_URL)/cmd/kfs-user

install: install-binaries

install-binaries: $(EXE)
	install -d $(DESTDIR)$(sbindir)
	install -p -m 0755 bin/kfs $(DESTDIR)$(sbindir)
	install -p -m 0755 bin/kfs-user $(DESTDIR)$(sbindir)

clean:
	rm -f $(EXE)

.PHONY: all exe install install-binaries clean
