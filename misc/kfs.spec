# disable debug package creation because of a bug when producing debuginfo
# packages: http://fedoraproject.org/wiki/PackagingDrafts/Go#Debuginfo
%global debug_package %{nil}
%global import_path   github.com/cea-hpc/%{name}

Name:           kfs
Version:        0.1.1
Release:        1%{?dist}
Summary:        Kerberos HTTPS user file server
License:        CeCILL-B
Source:         https://%{import_path}/archive/v%{version}/%{name}-%{version}.tar.gz
ExclusiveArch:  %{?go_arches:%{go_arches}}%{!?go_arches:%{ix86} x86_64 %{arm} aarch64}
BuildRequires:  %{?go_compiler:compiler(go-compiler)}%{!?go_compiler:golang}
BuildRequires:  krb5-devel
Requires:       krb5-libs

Requires(post):   systemd
Requires(preun):  systemd
Requires(postun): systemd
BuildRequires:    systemd

%description
%{summary}

This package provides a Kerberos HTTPS server. It authenticates users with
Kerberos and serves files from their kerberized user directory (or
directories). URL patterns can be defined to access different directories.

%prep
%setup -q

%build
# set up temporary build gopath, and put our directory there
mkdir -p ./_build/src/github.com/cea-hpc
ln -s $(pwd) ./_build/src/%{import_path}

export GOPATH=$(pwd)/_build:%{gopath}
make

# adjust path to libgssapi
sed -i 's|^#gssapi_lib_path.*|gssapi_lib_path: "/usr/lib64/libgssapi_krb5.so.2"|' config/kfs.yaml

%install
make install DESTDIR=%{buildroot} prefix=%{_prefix} mandir=%{_mandir}
install -d -m 0755 %{buildroot}%{_sysconfdir}/kfs
install -p -m 0644 config/kfs.yaml %{buildroot}%{_sysconfdir}/kfs
install -d -m 0755  %{buildroot}%{_unitdir}
install -p -m 0644 misc/kfs.service %{buildroot}%{_unitdir}

%files
%doc Licence_CeCILL-B_V1-en.txt Licence_CeCILL-B_V1-fr.txt README.asciidoc
%{_unitdir}/kfs.service
%config(noreplace) %{_sysconfdir}/kfs/kfs.yaml
%{_sbindir}/kfs
%{_sbindir}/kfs-user

%post
%systemd_post %{name}.service
exit 0

%preun
%systemd_preun %{name}.service
exit 0

%postun
%systemd_postun_with_restart %{name}.service
exit 0

%changelog
* Thu Jul 20 2023 Cyril Servant <cyril.servant@cea.fr> - 0.1.1-1
- remove duplicate 'Summary' entry in specfile
- kfs 0.1.1

* Tue Sep 04 2018 Arnaud Guignard <arnaud.guignard@cea.fr> - 0.1.0-1
- kfs 0.1.0
