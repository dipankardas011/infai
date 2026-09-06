Name:           infai
Version:        0.24.1
Release:        0
Summary:        Manage and launch local inference servers from the terminal
License:        Apache-2.0
URL:            https://github.com/dipankardas011/infai
Source0:        infai_%{version}_linux_amd64.tar.gz
Source1:        infai_%{version}_linux_arm64.tar.gz
ExclusiveArch:  x86_64 aarch64

%description
Terminal UI for managing and launching local llama.cpp and vLLM inference
servers.

%prep
%ifarch x86_64
tar -xzf %{SOURCE0}
%else
tar -xzf %{SOURCE1}
%endif

%build

%install
install -D -m 0755 infai %{buildroot}%{_bindir}/infai

%files
%license LICENSE
%doc README.md
%{_bindir}/infai

%changelog
* Sun Sep 06 2026 Dipankar Das <dipankardas011@users.noreply.github.com> - 0.24.1-0
- Package upstream release 0.24.1
