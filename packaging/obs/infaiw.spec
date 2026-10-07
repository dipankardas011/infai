Name:           infaiw
Version:        0.1.0
Release:        0
Summary:        Agent and workflow engine for infai
License:        MIT
URL:            https://github.com/dipankardas011/infai
Source0:        infaiw_%{version}_linux_amd64.tar.gz
Source1:        infaiw_%{version}_linux_arm64.tar.gz
Source2:        infaiw.service
ExclusiveArch:  x86_64 aarch64
BuildRequires:  systemd-rpm-macros

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
install -D -m 0644 %{SOURCE2} %{buildroot}%{_userunitdir}/infaiw.service

%preun
%systemd_user_preun infaiw.service

%postun
%systemd_user_postun infaiw.service

%files
%{_bindir}/infaiw
%{_userunitdir}/infaiw.service

%changelog
* Sun Sep 06 2026 Dipankar Das <dipankardas011@users.noreply.github.com> - 0.1.0-0
- Initial package
