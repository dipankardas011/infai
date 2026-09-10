Name:           infaiw
Version:        0.3.0
Release:        0
Summary:        Agent and workflow engine for infai
License:        MIT
URL:            https://github.com/dipankardas011/infai
Source0:        infaiw_%{version}_linux_amd64.tar.gz
Source1:        infaiw_%{version}_linux_arm64.tar.gz
ExclusiveArch:  x86_64 aarch64

%description
Agent and workflow engine for infai.

%prep
%ifarch x86_64
tar -xzf %{SOURCE0}
%else
tar -xzf %{SOURCE1}
%endif

%build

%install
install -D -m 0755 infaiw %{buildroot}%{_bindir}/infaiw

%files
%{_bindir}/infaiw

%changelog
* Sun Sep 06 2026 Dipankar Das <dipankardas011@users.noreply.github.com> - 0.3.0-0
- Initial package
