%{!?_tbound_version:%global _tbound_version 0.1.0}
%{!?_tbound_rpm_arch:%global _tbound_rpm_arch x86_64}
%{!?_tbound_srcfile:%global _tbound_srcfile tbound-%{_tbound_version}-linux-amd64.tar.gz}

Name:           tbound
Version:        %{_tbound_version}
Release:        1%{?dist}
Summary:        Host-side supervisor for the Pi coding agent
License:        MIT
URL:            https://github.com/faetschi/trust-boundary
Source0:        %{_tbound_srcfile}
BuildArch:      %{_tbound_rpm_arch}
Requires:       nodejs >= 22.19

%description
tbound runs the Pi coding agent as an untrusted worker and keeps authorization,
execution and evidence outside it. This package installs the tbound supervisor
CLI and the tbound-doctor preflight tool. Governed `tbound serve --pi` additionally
needs a signed host profile and the Podman/crun containment stack.

%prep
%setup -q -c -T
tar -xzf %{SOURCE0} -C .

%build
: # binaries are prebuilt in the release tarball

%install
rm -rf %{buildroot}
mkdir -p %{buildroot}%{_bindir} %{buildroot}%{_libexecdir}/tbound
install -m 0755 bin/tbound %{buildroot}%{_bindir}/tbound
install -m 0755 bin/tbound-doctor %{buildroot}%{_bindir}/tbound-doctor
cp -R runtime %{buildroot}%{_libexecdir}/tbound/runtime
if [ -d share/completions ]; then
  mkdir -p %{buildroot}%{_datadir}/tbound/completions
  cp share/completions/* %{buildroot}%{_datadir}/tbound/completions/
fi

%files
%{_bindir}/tbound
%{_bindir}/tbound-doctor
%{_libexecdir}/tbound
%{_datadir}/tbound

%changelog
* Wed Oct 08 2026 tbound <noreply@tbound.dev> - %{version}-1
- Initial package
